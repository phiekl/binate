// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package source

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
)

func TestDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "d", "f"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("d/f", filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "p"), 0o644); err != nil {
		t.Fatal(err)
	}
	var root Root = Disk(dir + "/")

	// The root is as given; entries are joined to it.
	if got := root.Path(""); got != dir+"/" {
		t.Errorf("root path %q", got)
	}
	if got := root.Path("d/f"); got != filepath.Join(dir, "d", "f") {
		t.Errorf("entry path %q", got)
	}

	names, err := root.ReadDir("")
	slices.Sort(names)
	if err != nil || !slices.Equal(names, []string{"d", "l", "p"}) {
		t.Errorf("ReadDir: %q, %v", names, err)
	}
	if _, err := root.ReadDir("nope"); !errors.Is(err, unix.ENOENT) {
		t.Errorf("ReadDir of a missing directory: %v", err)
	}

	// A symlink is followed only if asked.
	e, err := root.Entry("l", false)
	if err != nil || e.Mode&unix.S_IFMT != unix.S_IFLNK || e.Target != "d/f" {
		t.Errorf("Entry of the symlink: %+v, %v", e, err)
	}
	e, err = root.Entry("l", true)
	if err != nil || e.Mode&unix.S_IFMT != unix.S_IFREG || e.Size != 4 || e.Target != "" || e.Mtime.IsZero() {
		t.Errorf("Entry through the symlink: %+v, %v", e, err)
	}
	if e, err := root.Entry("nope", false); e.Mode != 0 || !errors.Is(err, unix.ENOENT) {
		t.Errorf("Entry of a missing entry: %+v, %v", e, err)
	}
	st, err := root.Stat("l")
	if err != nil || st.Mode&unix.S_IFMT != unix.S_IFLNK || st.Target != "d/f" || st.Ino == 0 {
		t.Errorf("Stat of the symlink: %+v, %v", st, err)
	}
	if st, err := root.Stat("nope"); st != nil || !errors.Is(err, unix.ENOENT) {
		t.Errorf("Stat of a missing entry: %+v, %v", st, err)
	}

	f, err := root.Open("d/f", false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || string(data) != "data" {
		t.Errorf("Open: %q, %v", data, err)
	}
	if f, err := root.Open("l", true); err != nil {
		t.Errorf("Open through the symlink: %v", err)
	} else {
		_ = f.Close()
	}
	// Failing, Open returns a nil File, not a nil *os.File in one.
	if f, err := root.Open("l", false); f != nil || err == nil {
		t.Errorf("Open of the symlink: %v, %v", f, err)
	}
	if f, err := root.Open("p", false); f != nil || !errors.Is(err, files.ErrNotRegular) {
		t.Errorf("Open of a FIFO: %v, %v", f, err)
	}
}

// TestReadDirOpensOnlyDirectories checks that what took a directory's
// place since it was found one is not opened: a FIFO without a writer
// fails at once, and /dev/null and a symlink to a directory are refused,
// but for the root, given through a symlink.
func TestReadDirOpensOnlyDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"null": "/dev/null", "link": "real"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	d := Disk(dir)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, rel := range []string{"fifo", "null", "link"} {
			if names, err := d.ReadDir(rel); err == nil {
				t.Errorf("%s listed: %q", rel, names)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waiting on the FIFO")
	}
	if names, err := Disk(filepath.Join(dir, "link")).ReadDir(""); err != nil || len(names) != 0 {
		t.Errorf("root through a symlink: %q, %v", names, err)
	}
}
