// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestAlternates(t *testing.T) {
	for _, f := range []*Format{SHA1, SHA256} {
		t.Run(f.name, func(t *testing.T) {
			orig := newTestRepo(t, f)
			history(orig, 5)
			orig.git("repack", "-ad")
			// A clone sharing the original's objects, with objects of its own.
			clone := &testRepo{t: t, dir: filepath.Join(t.TempDir(), "clone"), format: f}
			orig.git("clone", "-q", "--shared", orig.dir, clone.dir)
			clone.write(map[string]string{"new.txt": "only in the clone\n"})
			clone.commit()
			repo := clone.open()
			defer repo.Close()
			want := []string{filepath.Join(clone.dir, ".git", "objects"), filepath.Join(orig.dir, ".git", "objects")}
			if got := repo.Dirs(); !slices.Equal(got, want) {
				t.Errorf("dirs %q, want %q", got, want)
			}
			checkAll(t, clone, repo)
		})
	}
}

// writeAlternates writes the alternates of the objects directory dir.
func writeAlternates(t *testing.T, dir string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "info", "alternates"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestObjectDirs(t *testing.T) {
	root := t.TempDir()
	d := func(name string) string { return filepath.Join(root, name) }
	// a names b relatively and c quoted, with a comment and an empty line;
	// b names a back, and d; c names b again.
	writeAlternates(t, d("a"), "# comment", "", "../b", strconv.Quote(d("c")))
	writeAlternates(t, d("b"), d("a"), d("d"))
	writeAlternates(t, d("c"), d("b"))
	got, err := objectDirs(d("a"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{d("a"), d("b"), d("d"), d("c")}; !slices.Equal(got, want) {
		t.Errorf("dirs %q, want %q", got, want)
	}

	// Too deep: each of a chain names the next.
	for i := range 7 {
		writeAlternates(t, d("chain"+strconv.Itoa(i)), d("chain"+strconv.Itoa(i+1)))
	}
	if _, err := objectDirs(d("chain0")); err == nil {
		t.Error("followed alternates too deep")
	}

	writeAlternates(t, d("bad"), `"unterminated`)
	if _, err := objectDirs(d("bad")); err == nil {
		t.Error("read an invalid quoted path")
	}
}
