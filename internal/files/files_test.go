// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package files

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenRegular(t *testing.T) {
	dir := t.TempDir()
	file, link, fifo := filepath.Join(dir, "file"), filepath.Join(dir, "link"), filepath.Join(dir, "fifo")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", link); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		path   string
		follow bool
		want   error
	}{
		{file, false, nil},
		{link, true, nil},
		{link, false, syscall.ELOOP},
		// Opened without waiting for a writer, then refused.
		{fifo, true, ErrNotRegular},
		{dir, true, ErrNotRegular},
	} {
		f, err := OpenRegular(tt.path, tt.follow)
		if f != nil {
			_ = f.Close()
		}
		if !errors.Is(err, tt.want) {
			t.Errorf("OpenRegular(%s, %v) = %v, want %v", filepath.Base(tt.path), tt.follow, err, tt.want)
		}
	}
}

func TestUnwrapPath(t *testing.T) {
	_, err := os.Open("/nonexistent/x")
	if got := UnwrapPath(err); got != syscall.ENOENT {
		t.Errorf("UnwrapPath(%v) = %v", err, got)
	}
	err = os.Rename("/nonexistent/a", "/nonexistent/b")
	if got := UnwrapPath(err); got != syscall.ENOENT {
		t.Errorf("UnwrapPath(%v) = %v", err, got)
	}
}

func TestResolveDirAt(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"a", "b/sub"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{
		"a/to-b":     "../b",
		"a/chain":    "to-b/sub",
		"a/abs":      filepath.Dir(root),
		"a/abs-in":   root + "/b//sub",
		"a/abs-dots": root + "/a/to-b/../b/sub",
		"a/up":       "../..",
		"a/loop":     "loop",
		"a/file":     "",
		"b/to-sub":   "./sub/.",
		"a/via-file": "file/../to-b/sub",
	} {
		var err error
		if target == "" {
			err = os.WriteFile(filepath.Join(root, name), nil, 0o644)
		} else {
			err = os.Symlink(target, filepath.Join(root, name))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	// A directory that can't be searched, as the tests run unprivileged.
	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
	searchBlocked := os.Geteuid() != 0
	rootDir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rootDir.Close() }()
	var sub syscall.Stat_t
	if err := syscall.Stat(filepath.Join(root, "b", "sub"), &sub); err != nil {
		t.Fatal(err)
	}
	for _, open := range []struct {
		name string
		fn   func(int, string) (*os.File, error)
	}{
		{"ResolveDirAt", func(fd int, rel string) (*os.File, error) { return ResolveDirAt(fd, root, rel) }},
		{"walk", func(fd int, rel string) (*os.File, error) {
			resolved, err := resolveBeneath(fd, root, rel)
			if err != nil {
				return nil, err
			}
			return OpenDirAt(fd, resolved)
		}},
	} {
		for _, tt := range []struct {
			rel  string
			want error
		}{
			{"a/to-b/sub", nil},
			{"a/chain", nil},
			{"a/chain/../sub", nil},
			{"b/to-sub", nil},
			{"a/abs", syscall.EXDEV},
			{"a/abs-in", nil},
			{"a/abs-dots", nil},
			{"a/up/x", syscall.EXDEV},
			{"..", syscall.EXDEV},
			{"a/loop", syscall.ELOOP},
			{"a/file", syscall.ENOTDIR},
			{"a/file/../to-b/sub", syscall.ENOTDIR},
			{"a/missing/../to-b/sub", syscall.ENOENT},
			{"a/via-file", syscall.ENOTDIR},
			{"blocked/../b/sub", syscall.EACCES},
			{"blocked/./../b/sub", syscall.EACCES},
			{"a/to-b/../blocked/../b/sub", syscall.EACCES},
			{"a/missing", syscall.ENOENT},
		} {
			if errors.Is(tt.want, syscall.EACCES) && !searchBlocked {
				continue
			}
			// The kernel's own lookup, which may leave the root, agrees
			// on the paths that stay beneath it.
			if !errors.Is(tt.want, syscall.EXDEV) {
				fd, err := unix.Openat(int(rootDir.Fd()), tt.rel, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
				if err == nil {
					_ = unix.Close(fd)
				}
				if !errors.Is(err, tt.want) {
					t.Errorf("openat %s: %v, want %v", tt.rel, err, tt.want)
				}
			}
			f, err := open.fn(int(rootDir.Fd()), tt.rel)
			if !errors.Is(err, tt.want) {
				t.Errorf("%s %s: %v, want %v", open.name, tt.rel, err, tt.want)
			}
			if f == nil {
				continue
			}
			var st syscall.Stat_t
			if err := syscall.Fstat(int(f.Fd()), &st); err != nil || st.Ino != sub.Ino {
				t.Errorf("%s %s: opened inode %d, %v, want b/sub", open.name, tt.rel, st.Ino, err)
			}
			_ = f.Close()
		}
	}
}

func TestOpenRegularBeneath(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	if err := os.MkdirAll(filepath.Join(root, "links"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"root/f": "inside", "outside": "outside"} {
		if err := os.WriteFile(filepath.Join(parent, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{
		"rel":    "../f",
		"abs":    root + "/f",
		"chain":  "abs",
		"out":    "../../outside",
		"absout": filepath.Join(parent, "outside"),
		"dir":    "..",
		"loop":   "loop",
	} {
		if err := os.Symlink(target, filepath.Join(root, "links", name)); err != nil {
			t.Fatal(err)
		}
	}
	rootDir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rootDir.Close() }()
	for _, tt := range []struct {
		rel  string
		want error
	}{
		{"f", nil},
		{"links/rel", nil},
		{"links/abs", nil},
		{"links/chain", nil},
		{"links/out", syscall.EXDEV},
		{"links/absout", syscall.EXDEV},
		{"links/dir", ErrNotRegular},
		{"links/loop", syscall.ELOOP},
		{"links/missing", syscall.ENOENT},
		{root + "/links/abs", nil},
		{root + "/links/../f", nil},
		{parent + "/outside", syscall.EXDEV},
		{root + "/../root/f", syscall.EXDEV},
	} {
		f, err := OpenRegularBeneath(int(rootDir.Fd()), root, tt.rel)
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.rel, err, tt.want)
		}
		if f == nil {
			continue
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil || string(data) != "inside" {
			t.Errorf("%s: read %q, %v", tt.rel, data, err)
		}
	}
}
