// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	"pxy.se/go/binate/internal/source"
)

func TestIndexRoot(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{
		"same":         "committed",
		"staged":       "committed",
		"partly":       "committed",
		"dir/sub/deep": "committed",
		"link":         "->same",
		"removed":      "committed",
		"conflict":     "base\n",
	})
	r.commit()
	r.git("branch", "other")
	r.write(map[string]string{"conflict": "ours\n"})
	r.commit()
	r.git("checkout", "-q", "other")
	r.write(map[string]string{"conflict": "theirs\n"})
	r.commit()
	r.git("checkout", "-q", "-")
	if err := r.command("merge", "-q", "other").Run(); err == nil {
		t.Fatal("merge had no conflict")
	}
	r.write(map[string]string{
		"staged":    "staged",
		"partly":    "staged",
		"added":     "*new and executable",
		"untracked": "not in the index",
		"intent":    "added with -N",
	})
	r.git("add", "staged", "partly", "added")
	r.git("add", "-N", "intent")
	r.git("rm", "-q", "--cached", "removed")
	r.write(map[string]string{"partly": "staged, then changed on disk"})

	repo := openTest(t, r, "")
	x, err := repo.OpenIndex()
	if err != nil {
		t.Fatal(err)
	}
	got := indexFiles(t, x)
	var want []string
	for line := range strings.Lines(r.git("ls-files", "-s")) {
		fields := strings.Fields(line)
		name := fields[3]
		if name == "intent" || slices.Contains(want, name) {
			continue
		}
		want = append(want, name)
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("index paths = %q, git shows %q", got, want)
	}
	for _, name := range []string{"staged", "partly", "added", "same"} {
		if got, want := readWorkFile(t, x, name), r.git("show", ":"+name); got != want {
			t.Errorf("%s = %q, git shows %q", name, got, want)
		}
	}
	if e, err := x.Entry("added", false); err != nil || e.Mode != 0o100755 {
		t.Errorf("added = %+v, %v", e, err)
	}
	if e, err := x.Entry("link", false); err != nil || e.Mode != 0o120777 || e.Target != "same" {
		t.Errorf("link = %+v, %v", e, err)
	}
	if _, err := x.Entry("conflict", false); !errors.Is(err, errUnmerged) {
		t.Errorf("conflict = %v", err)
	}
	if id, ok := x.ContentID("staged"); !ok || id.Sum != string(r.id(":staged").Bytes()) {
		t.Errorf("staged content ID = %v, %v", id, ok)
	}
	if _, err := x.Stat("untracked"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("untracked stat = %v", err)
	}
	if names, err := x.ReadDir("dir"); err != nil || !slices.Equal(names, []string{"sub"}) {
		t.Errorf("dir = %q, %v", names, err)
	}
	if x.Path("dir/sub/deep") != ":dir/sub/deep" {
		t.Errorf("path = %q", x.Path("dir/sub/deep"))
	}

	// A rescan reads what was staged since.
	r.git("add", "untracked")
	reloaded, err := x.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(indexFiles(t, reloaded), "untracked") {
		t.Error("reloaded index misses a newly staged file")
	}
}

// indexFiles returns the paths of the files in the index, an unmerged
// one among them.
func indexFiles(t *testing.T, root source.Root) []string {
	t.Helper()
	var files []string
	var walk func(string)
	walk = func(dir string) {
		names, err := root.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			rel := path.Join(dir, name)
			st, err := root.Stat(rel)
			switch {
			case errors.Is(err, errUnmerged):
				files = append(files, rel)
			case err != nil:
				t.Fatal(err)
			case st.Mode&0o170000 == 0o040000:
				walk(rel)
			default:
				files = append(files, rel)
			}
		}
	}
	walk("")
	slices.Sort(files)
	return files
}

func TestIndexRootEmpty(t *testing.T) {
	r := newTestRepo(t, SHA1)
	x, err := openTest(t, r, "").OpenIndex()
	if err != nil {
		t.Fatal(err)
	}
	if names, err := x.ReadDir(""); err != nil || len(names) != 0 {
		t.Errorf("empty index = %q, %v", names, err)
	}
}

// TestPathTree checks that each path and directory is listed once, in its
// parent and in the order first added, even a path that is both a file
// and a directory, as in a conflict between the two.
func TestPathTree(t *testing.T) {
	var tree pathTree
	if tree.isDir("") || tree.names[""] != nil {
		t.Error("an empty tree has entries")
	}
	for _, rel := range []string{"b", "a/x", "a/y/z", "a", "a/x", "c/d"} {
		tree.add(rel)
	}
	want := map[string][]string{"": {"b", "a", "c"}, "a": {"x", "y"}, "a/y": {"z"}, "c": {"d"}}
	for dir, names := range want {
		if !slices.Equal(tree.names[dir], names) {
			t.Errorf("names of %q = %q, want %q", dir, tree.names[dir], names)
		}
	}
	for rel, isDir := range map[string]bool{"": false, "a": true, "a/y": true, "c": true, "b": false, "a/x": false, "e": false} {
		if tree.isDir(rel) != isDir {
			t.Errorf("isDir(%q) = %v", rel, !isDir)
		}
	}
}
