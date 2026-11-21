// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package stat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRead(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("four"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("missing-target", link); err != nil {
		t.Fatal(err)
	}

	st, err := Read(file)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != unix.S_IFREG|0o640 || st.Size != 4 || st.Nlink != 1 || st.Ino == 0 {
		t.Errorf("file %+v", st)
	}
	if st.Uid != uint32(os.Getuid()) || st.Mtime.IsZero() || st.Ctime.IsZero() || st.Atime.IsZero() {
		t.Errorf("file %+v", st)
	}

	// A symlink is described, not followed, even to nowhere.
	st, err = Read(link)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFLNK || st.Target != "missing-target" {
		t.Errorf("link %+v", st)
	}

	if st, err := Read(dir); err != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		t.Errorf("directory %+v, %v", st, err)
	}
	if _, err := Read(filepath.Join(dir, "nope")); !errors.Is(err, unix.ENOENT) {
		t.Errorf("missing: %v", err)
	}
}

func TestNames(t *testing.T) {
	ids := parseIDs(strings.NewReader("# comment\nroot:x:0:0:root:/root:/bin/sh\n\nbroken\nnoid:x:abc:\nalias:x:0:\nal:x:1000:1000::/home/al:/bin/sh\n"))
	n := &Names{users: ids}
	if n.User(0) != "root" || n.User(1000) != "al" || n.User(5) != "" {
		t.Errorf("users %v", ids)
	}
	if len(ids) != 2 {
		t.Errorf("ids %v", ids)
	}
	var none *Names
	if none.User(0) != "" || none.Group(0) != "" {
		t.Error("nil names")
	}
}
