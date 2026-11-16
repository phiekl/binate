// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testRepo is a repository made by git for a test, which is skipped if git
// is not installed: binate never runs git, only its tests do.
type testRepo struct {
	t      testing.TB
	dir    string
	format *Format
}

// newTestRepo makes an empty repository of format f, its work tree in a
// temporary directory.
func newTestRepo(t testing.TB, f *Format) *testRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	r := &testRepo{t: t, dir: t.TempDir(), format: f}
	r.git("init", "-q", "--object-format="+f.name)
	return r
}

// git runs git in the repository and returns its output.
func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	home := r.t.TempDir()
	cmd.Env = append(os.Environ(),
		"HOME="+home, "XDG_CONFIG_HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.org", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.org", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

// write writes files into the work tree, as tree in scan's tests does: a
// trailing "/" makes a directory, "->target" a symlink and "*" before the
// contents an executable.
func (r *testRepo) write(files map[string]string) {
	r.t.Helper()
	for p, contents := range files {
		full := filepath.Join(r.dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatal(err)
		}
		var err error
		switch {
		case strings.HasSuffix(p, "/"):
			err = os.MkdirAll(full, 0o755)
		case strings.HasPrefix(contents, "->"):
			_ = os.Remove(full)
			err = os.Symlink(contents[2:], full)
		case strings.HasPrefix(contents, "*"):
			err = os.WriteFile(full, []byte(contents[1:]), 0o755)
		default:
			err = os.WriteFile(full, []byte(contents), 0o644)
		}
		if err != nil {
			r.t.Fatal(err)
		}
	}
}

// commit commits everything in the work tree and returns the commit.
func (r *testRepo) commit() ID {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", "c")
	return r.id("HEAD")
}

// id returns the object rev names.
func (r *testRepo) id(rev string) ID {
	r.t.Helper()
	id, err := ParseID(r.format, strings.TrimSpace(r.git("rev-parse", "--verify", rev)))
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

// open opens the repository's objects with the reader.
func (r *testRepo) open() *Repo {
	r.t.Helper()
	repo, err := OpenRepo(filepath.Join(r.dir, ".git", "objects"), r.format)
	if err != nil {
		r.t.Fatal(err)
	}
	return repo
}

// gitObject is an object as git reads it.
type gitObject struct {
	id   ID
	typ  string
	data []byte
}

// all returns every object in the repository, as git reads them.
func (r *testRepo) all() []gitObject {
	r.t.Helper()
	cmd := exec.Command("git", "cat-file", "--batch-all-objects", "--batch", "--unordered")
	cmd.Dir = r.dir
	out, err := cmd.Output()
	if err != nil {
		r.t.Fatal(err)
	}
	var objs []gitObject
	br := bufio.NewReader(bytes.NewReader(out))
	for {
		line, err := br.ReadString('\n')
		if errors.Is(err, io.EOF) {
			return objs
		}
		if err != nil {
			r.t.Fatal(err)
		}
		f := strings.Fields(line)
		id, err := ParseID(r.format, f[0])
		if err != nil {
			r.t.Fatal(err)
		}
		size, _ := strconv.Atoi(f[2])
		data := make([]byte, size+1)
		if _, err := io.ReadFull(br, data); err != nil {
			r.t.Fatal(err)
		}
		objs = append(objs, gitObject{id, f[1], data[:size]})
	}
}

// checkAll checks that repo reads every object as git does.
func checkAll(t *testing.T, r *testRepo, repo *Repo) {
	t.Helper()
	objs := r.all()
	if len(objs) == 0 {
		t.Fatal("no objects")
	}
	for _, o := range objs {
		typ, data, err := repo.Read(o.id, 1<<30)
		if err != nil {
			t.Errorf("%s: %v", o.id, err)
			continue
		}
		if typ.String() != o.typ || !bytes.Equal(data, o.data) {
			t.Errorf("%s: read a %s of %d bytes, git a %s of %d", o.id, typ, len(data), o.typ, len(o.data))
		}
		typ, size, err := repo.Info(o.id)
		if err != nil || typ.String() != o.typ || size != int64(len(o.data)) {
			t.Errorf("%s: info %s, %d, %v", o.id, typ, size, err)
		}
	}
}

var testFiles = map[string]string{
	"a.txt":         "hello\n",
	"empty":         "",
	"run.sh":        "*#!/bin/sh\n",
	"link":          "->a.txt",
	"dir/b.txt":     "b\n",
	"dir/sub/c.bin": "\x00\x01\x02",
	"dir/sub/d":     strings.Repeat("large\n", 100000),
}

func TestLoose(t *testing.T) {
	for _, f := range []*Format{SHA1, SHA256} {
		t.Run(f.name, func(t *testing.T) {
			r := newTestRepo(t, f)
			r.write(testFiles)
			r.commit()
			checkAll(t, r, r.open())
		})
	}
}

func TestReadTree(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(testFiles)
	commit := r.commit()
	repo := r.open()
	tree, err := repo.PeelTree(commit)
	if err != nil {
		t.Fatal(err)
	}
	if want := r.id("HEAD^{tree}"); tree != want {
		t.Fatalf("tree %s, want %s", tree, want)
	}
	entries, err := repo.ReadTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, strconv.FormatUint(uint64(e.Mode), 8)+" "+e.Name+" "+e.ID.String())
	}
	var want []string
	for line := range strings.Lines(r.git("ls-tree", "HEAD")) {
		meta, name, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "\t")
		f := strings.Fields(meta)
		mode, _ := strconv.ParseUint(f[0], 8, 32)
		want = append(want, strconv.FormatUint(mode, 8)+" "+name+" "+f[2])
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("entries\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if _, err := repo.ReadTree(commit); err == nil {
		t.Error("read a commit as a tree")
	}
}

func TestPeelTree(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"f": "x"})
	r.commit()
	r.git("tag", "-a", "-m", "t", "inner")
	r.git("tag", "-a", "-m", "t", "outer", "inner")
	repo := r.open()
	want := r.id("HEAD^{tree}")
	for _, rev := range []string{"HEAD", "HEAD^{tree}", "inner", "outer"} {
		if got, err := repo.PeelTree(r.id(rev)); err != nil || got != want {
			t.Errorf("%s: %s, %v", rev, got, err)
		}
	}
	if _, err := repo.PeelTree(r.id("HEAD:f")); err == nil {
		t.Error("peeled a blob")
	}
	missing, _ := ParseID(SHA1, strings.Repeat("ab", 20))
	if _, err := repo.PeelTree(missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

// writeLoose writes a loose object of raw content, header and all, as id,
// compressed.
func writeLoose(t *testing.T, dir string, id ID, raw []byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	p := loosePath(dir, id)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o444); err != nil {
		t.Fatal(err)
	}
}

func TestLooseCorrupt(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"no header", "blob"},
		{"unknown type", "blub 1\x00x"},
		{"leading zero", "blob 01\x00x"},
		{"sign", "blob +1\x00x"},
		{"short", "blob 2\x00x"},
		{"long", "blob 1\x00xy"},
		{"huge", "blob 99999999999999999999\x00x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			id, _ := ParseID(SHA1, strings.Repeat("12", 20))
			writeLoose(t, dir, id, []byte(tc.raw))
			repo, _ := OpenRepo(dir, SHA1)
			_, _, err := repo.Read(id, 1<<20)
			var ce *CorruptError
			if !errors.As(err, &ce) {
				t.Errorf("read: %v", err)
			}
		})
	}
	// Not zlib at all.
	dir := t.TempDir()
	id, _ := ParseID(SHA1, strings.Repeat("34", 20))
	p := loosePath(dir, id)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte("blob 1\x00x"), 0o444)
	repo, _ := OpenRepo(dir, SHA1)
	if _, _, err := repo.Read(id, 1<<20); err == nil {
		t.Error("read an uncompressed object")
	}
}

func TestParseTree(t *testing.T) {
	id := strings.Repeat("\x11", 20)
	for _, tc := range []struct {
		name, data string
		mode       Mode
		ok         bool
	}{
		{"file", "100644 f\x00" + id, ModeFile, true},
		{"group writable", "100664 f\x00" + id, ModeFile, true},
		{"executable", "100755 f\x00" + id, ModeExecutable, true},
		{"tree", "40000 d\x00" + id, ModeTree, true},
		{"padded tree", "040000 d\x00" + id, ModeTree, true},
		{"symlink", "120000 l\x00" + id, ModeSymlink, true},
		{"submodule", "160000 s\x00" + id, ModeSubmodule, true},
		{"fifo", "10644 f\x00" + id, 0, false},
		{"not octal", "100844 f\x00" + id, 0, false},
		{"no mode", " f\x00" + id, 0, false},
		{"slash", "100644 a/b\x00" + id, 0, false},
		{"dot", "40000 .\x00" + id, 0, false},
		{"dot dot", "40000 ..\x00" + id, 0, false},
		{"empty name", "100644 \x00" + id, 0, false},
		{"short id", "100644 f\x00" + id[1:], 0, false},
		{"twice", "100644 f\x00" + id + "100644 f\x00" + id, 0, false},
	} {
		entries, err := ParseTree(SHA1, []byte(tc.data))
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if tc.ok && (len(entries) != 1 || entries[0].Mode != tc.mode) {
			t.Errorf("%s: %+v", tc.name, entries)
		}
	}
}

func FuzzParseTree(f *testing.F) {
	id := strings.Repeat("\x11", 20)
	f.Add([]byte("100644 f\x00" + id + "40000 d\x00" + id))
	f.Add([]byte("120000 l\x00" + id))
	f.Fuzz(func(t *testing.T, data []byte) {
		entries, err := ParseTree(SHA1, data)
		if err != nil {
			return
		}
		for _, e := range entries {
			if checkName(e.Name) != nil {
				t.Errorf("accepted name %q", e.Name)
			}
			switch e.Mode {
			case ModeFile, ModeExecutable, ModeSymlink, ModeTree, ModeSubmodule:
			default:
				t.Errorf("mode %o", e.Mode)
			}
		}
	})
}

func FuzzLooseHeader(f *testing.F) {
	f.Add([]byte("blob 12\x00"))
	f.Add([]byte("commit 0\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		typ, size, n, err := parseLooseHeader(b)
		if err != nil {
			return
		}
		if n > len(b) || b[n-1] != 0 || size < 0 || typ < Commit || typ > Tag {
			t.Errorf("%q: %v %d %d", b, typ, size, n)
		}
	})
}
