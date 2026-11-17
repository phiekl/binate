// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package files opens the files binate reads, safely, and words their
// errors without the paths the caller already knows.
package files

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

// ErrNotRegular is the error opening what is no longer a regular file, as
// when something else replaced a file since it was looked at.
var ErrNotRegular = errors.New("no longer a regular file")

// OpenRegular opens path for reading only if it is a regular file. It never
// waits on a FIFO or opens a terminal put in its place; with follow false,
// it doesn't follow a symlink either.
func OpenRegular(path string, follow bool) (*os.File, error) {
	return OpenRegularAt(unix.AT_FDCWD, path, follow)
}

// OpenRegularAt is OpenRegular relative to an open directory.
func OpenRegularAt(dir int, path string, follow bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOCTTY
	if !follow {
		flags |= unix.O_NOFOLLOW
	}
	fd, err := unix.Openat(dir, path, flags, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return nil, ErrNotRegular
	}
	return os.NewFile(uintptr(fd), path), nil
}

// UnwrapPath returns the error of a *fs.PathError or *os.LinkError without
// its path, which the caller knows, or err as it is.
func UnwrapPath(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return linkErr.Err
	}
	return err
}
