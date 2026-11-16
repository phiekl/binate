// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
)

// twoCommits makes a repository with two commits differing in every way a
// tree can, and returns them.
func twoCommits(t *testing.T) (*testRepo, ID, ID) {
	t.Helper()
	r := newTestRepo(t, SHA1)
	r.write(testFiles)
	first := r.commit()
	r.git("rm", "-q", "empty")
	r.write(map[string]string{
		"a.txt":     "hello, changed\n",
		"new.txt":   "new\n",
		"run.sh":    "#!/bin/sh\n", // no longer executable
		"link":      "->dir",
		"dir/sub/d": strings.Repeat("large\n", 99999) + "LARGE\n", // same size
	})
	if err := os.Chmod(filepath.Join(r.dir, "run.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git("add", "-A")
	// A submodule, which only has a commit's name in the tree.
	r.git("update-index", "--add", "--cacheinfo", "160000,"+first.String()+",sub")
	r.git("commit", "-q", "-m", "second")
	return r, first, r.id("HEAD")
}

func TestRoot(t *testing.T) {
	r, _, second := twoCommits(t)
	repo := r.open()
	defer repo.Close()
	root, err := NewRoot(repo, second, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	var _ source.Root = root

	if got := root.Path("dir/b.txt"); got != "HEAD:dir/b.txt" {
		t.Errorf("path %q", got)
	}
	names, err := root.ReadDir("")
	slices.Sort(names)
	if err != nil || strings.Join(names, " ") != "a.txt dir link new.txt run.sh sub" {
		t.Errorf("ReadDir: %q, %v", names, err)
	}
	for _, tc := range []struct {
		rel    string
		mode   uint32
		size   int64
		target string
	}{
		{"", unix.S_IFDIR | 0o755, 0, ""},
		{"a.txt", unix.S_IFREG | 0o644, 15, ""},
		{"run.sh", unix.S_IFREG | 0o644, 10, ""},
		{"dir", unix.S_IFDIR | 0o755, 0, ""},
		{"dir/sub/c.bin", unix.S_IFREG | 0o644, 3, ""},
		{"dir/sub/d", unix.S_IFREG | 0o644, 600000, ""},
		{"link", unix.S_IFLNK | 0o777, 3, "dir"},
	} {
		e, err := root.Entry(tc.rel, false)
		if err != nil || e.Mode != tc.mode || e.Size != tc.size || e.Target != tc.target || !e.Mtime.IsZero() {
			t.Errorf("Entry(%q): %+v, %v", tc.rel, e, err)
		}
	}
	for _, tc := range []struct {
		rel  string
		want error
	}{
		{"nope", unix.ENOENT},
		{"dir/nope", unix.ENOENT},
		{"nope/x", unix.ENOENT},
		{"a.txt/x", unix.ENOTDIR},
		{"sub", errSubmodule},
	} {
		if e, err := root.Entry(tc.rel, false); e.Mode != 0 || !errors.Is(err, tc.want) {
			t.Errorf("Entry(%q): %+v, %v", tc.rel, e, err)
		}
	}
	if _, err := root.Entry("link", true); !errors.Is(err, errFollow) {
		t.Errorf("Entry followed a symlink: %v", err)
	}

	f, err := root.Open("dir/b.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || string(data) != "b\n" {
		t.Errorf("Open: %q, %v", data, err)
	}
	if fi, _ := f.Stat(); fi.Size() != 2 || fi.Name() != "b.txt" || fi.Mode() != 0o644 || fi.IsDir() {
		t.Errorf("Stat of the open file: %v", fi)
	}
	for _, rel := range []string{"", "dir", "link", "sub"} {
		if f, err := root.Open(rel, false); f != nil || !errors.Is(err, files.ErrNotRegular) {
			t.Errorf("Open(%q): %v, %v", rel, f, err)
		}
	}
	if _, err := root.Open("nope", false); !errors.Is(err, unix.ENOENT) {
		t.Errorf("Open of a missing file: %v", err)
	}

	st, err := root.Stat("link")
	if err != nil || st.Mode != unix.S_IFLNK|0o777 || st.Target != "dir" {
		t.Errorf("Stat: %+v, %v", st, err)
	}

	id, ok := root.ContentID("a.txt")
	if want := r.id("HEAD:a.txt"); !ok || id.Scheme != "git-sha1" || id.Sum != string(want.Bytes()) {
		t.Errorf("ContentID: %+v, %v", id, ok)
	}
	for _, rel := range []string{"", "dir", "link", "sub", "nope"} {
		if _, ok := root.ContentID(rel); ok {
			t.Errorf("ContentID(%q) of no file", rel)
		}
	}
}

// scanned scans left and right, comparing compare, and returns the mark
// of each entry by path.
func scanned(t *testing.T, left, right source.Root, compare scan.Attrs) map[string]rune {
	t.Helper()
	s := scan.Start(left, right, scan.Options{Compare: compare}, nil)
	done := make(chan struct{})
	go func() { s.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the scan did not finish")
	}
	marks := map[string]rune{}
	var walk func(n *scan.Node)
	walk = func(n *scan.Node) {
		if n.Parent != nil {
			marks[n.Path()] = n.Mark()
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(s.Root)
	return marks
}

func TestScanCommits(t *testing.T) {
	r, first, second := twoCommits(t)
	repo := r.open()
	defer repo.Close()
	left, err := NewRoot(repo, first, "first")
	if err != nil {
		t.Fatal(err)
	}
	right, err := NewRoot(repo, second, "second")
	if err != nil {
		t.Fatal(err)
	}
	got := scanned(t, left, right, scan.DefaultAttrs)
	want := map[string]rune{
		"a.txt": '|', "empty": '<', "new.txt": '>', "run.sh": '~', "link": '|',
		"sub": '>',
		"dir": '=', "dir/b.txt": '=', "dir/sub": '=', "dir/sub/c.bin": '=', "dir/sub/d": '|',
	}
	for p, m := range want {
		if got[p] != m {
			t.Errorf("%s: %c, want %c", p, got[p], m)
		}
	}
	if len(got) != len(want) {
		t.Errorf("entries %v", got)
	}
}

// A commit compared with its checkout differs in nothing git records.
func TestScanCheckout(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(testFiles)
	commit := r.commit()
	repo := r.open()
	defer repo.Close()
	tree, err := NewRoot(repo, commit, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	got := scanned(t, tree, source.Disk(r.dir), scan.Left|scan.Type|scan.Content|scan.Target|scan.Mode)
	for p, m := range got {
		switch {
		case p == ".git" || strings.HasPrefix(p, ".git/"):
		case m != '=':
			t.Errorf("%s: %c", p, m)
		}
	}
	if got["dir/sub/d"] != '=' {
		t.Errorf("entries %v", got)
	}
}
