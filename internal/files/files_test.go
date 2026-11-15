// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package files

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
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
