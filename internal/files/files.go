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
	"strings"

	"golang.org/x/sys/unix"
)

// ErrNotRegular is the error opening what is no longer a regular file, as
// when something else replaced a file since it was looked at.
var ErrNotRegular = errors.New("no longer a regular file")

// OpenDirAt opens a directory below root without following symlinks.
// The returned descriptor can be used with the *at calls.
func OpenDirAt(root int, rel string) (*os.File, error) {
	const flags = unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if rel == "" {
		rel = "."
	}
	fd, err := unix.Openat2(root, rel, &unix.OpenHow{
		Flags:   flags,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
		fd, err = unix.Openat(root, ".", flags, 0)
		if err == nil && rel != "." {
			for part := range strings.SplitSeq(rel, "/") {
				if part == "" || part == "." || part == ".." {
					_ = unix.Close(fd)
					return nil, unix.EXDEV
				}
				next, err := unix.Openat(fd, part, flags, 0)
				_ = unix.Close(fd)
				if err != nil {
					return nil, err
				}
				fd = next
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), rel), nil
}

// maxLinks is how many symlinks are followed beneath a root, as Linux
// follows.
const maxLinks = 40

// ResolveDirAt opens a directory below root, the directory at rootPath,
// following symlinks that stay below it. An absolute target is followed
// where it names a path below rootPath. A symlink leading out is refused
// with EXDEV.
func ResolveDirAt(root int, rootPath, rel string) (*os.File, error) {
	if rel == "" {
		rel = "."
	}
	fd, err := unix.Openat2(root, rel, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err == nil {
		return os.NewFile(uintptr(fd), rel), nil
	}
	if !resolveAgain(err) {
		return nil, err
	}
	resolved, err := resolveBeneath(root, rootPath, rel)
	if err != nil {
		return nil, err
	}
	return OpenDirAt(root, resolved)
}

// OpenRegularBeneath opens the regular file at rel below root as
// OpenRegular does, following symlinks as ResolveDirAt does, a final one
// included. rel may also be absolute, naming a path below rootPath.
func OpenRegularBeneath(root int, rootPath, rel string) (*os.File, error) {
	fd, err := unix.Openat2(root, rel, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOCTTY,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err == nil {
		return regularFile(fd, rel)
	}
	if !resolveAgain(err) {
		return nil, err
	}
	resolved, err := resolveBeneath(root, rootPath, rel)
	if err != nil {
		return nil, err
	}
	dirPath, name := "", resolved
	if i := strings.LastIndexByte(resolved, '/'); i >= 0 {
		dirPath, name = resolved[:i], resolved[i+1:]
	}
	if name == "" {
		name = "."
	}
	dir, err := OpenDirAt(root, dirPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	return OpenRegularAt(int(dir.Fd()), name, false)
}

// resolveAgain reports whether openat2 failed in a way resolveBeneath may
// not: where it is missing or filtered, as before Linux 5.6 or in some
// containers, or at an absolute symlink, which it always refuses.
func resolveAgain(err error) bool {
	return errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EXDEV)
}

// resolveBeneath resolves rel below root a name at a time, an absolute rel
// as a symlink to it would be, replacing each symlink by its target, and
// returns the path with none left. Opening it without following symlinks
// refuses a name replaced by one since.
func resolveBeneath(root int, rootPath, rel string) (string, error) {
	if strings.HasPrefix(rel, "/") {
		below, ok := cutRoot(rel, rootPath)
		if !ok {
			return "", unix.EXDEV
		}
		rel = below
	}
	var resolved []string
	pending := strings.Split(rel, "/")
	links := 0
	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]
		switch name {
		case "":
			continue
		case ".", "..":
			if name == ".." && len(resolved) == 0 {
				return "", unix.EXDEV
			}
			// The kernel looks these up in the directory, which needs
			// search permission. Readlinkat checks it for other names.
			if err := searchable(root, resolved); err != nil {
				return "", err
			}
			if name == ".." {
				resolved = resolved[:len(resolved)-1]
			}
			continue
		}
		dir, err := OpenDirAt(root, strings.Join(resolved, "/"))
		if err != nil {
			return "", err
		}
		target, err := ReadlinkAt(int(dir.Fd()), name)
		if errors.Is(err, unix.EINVAL) {
			// Not a symlink. One with more to look up in it must be a
			// directory, even if a later ".." leaves it, as the kernel
			// requires.
			var st unix.Stat_t
			err = unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
			if err == nil && len(pending) > 0 && st.Mode&unix.S_IFMT != unix.S_IFDIR {
				err = unix.ENOTDIR
			}
			_ = dir.Close()
			if err != nil {
				return "", err
			}
			resolved = append(resolved, name)
			continue
		}
		_ = dir.Close()
		if err != nil {
			return "", err
		}
		if links++; links > maxLinks {
			return "", unix.ELOOP
		}
		if strings.HasPrefix(target, "/") {
			below, ok := cutRoot(target, rootPath)
			if !ok {
				return "", unix.EXDEV
			}
			resolved, target = nil, below
		}
		pending = append(strings.Split(target, "/"), pending...)
	}
	return strings.Join(resolved, "/"), nil
}

// cutRoot returns the absolute path p from rootPath, if it starts with
// rootPath's names. Any "." or ".." among those leaves it unmatched.
func cutRoot(p, rootPath string) (string, bool) {
	if !strings.HasPrefix(rootPath, "/") {
		return "", false
	}
	names := strings.Split(p, "/")
	for want := range strings.SplitSeq(rootPath, "/") {
		if want == "" {
			continue
		}
		for len(names) > 0 && names[0] == "" {
			names = names[1:]
		}
		if len(names) == 0 || names[0] != want {
			return "", false
		}
		names = names[1:]
	}
	return strings.Join(names, "/"), true
}

// searchable checks that the directory of the names resolved below root
// may be searched, as the kernel requires to look up a name in it.
func searchable(root int, resolved []string) error {
	if len(resolved) == 0 {
		return unix.Faccessat(root, ".", unix.X_OK, unix.AT_EACCESS)
	}
	last := len(resolved) - 1
	dir, err := OpenDirAt(root, strings.Join(resolved[:last], "/"))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return unix.Faccessat(int(dir.Fd()), resolved[last], unix.X_OK, unix.AT_EACCESS|unix.AT_SYMLINK_NOFOLLOW)
}

// ReadlinkAt returns the target of the symlink name in dir.
func ReadlinkAt(dir int, name string) (string, error) {
	for size := 128; size <= 1<<20; size *= 2 {
		buf := make([]byte, size)
		n, err := unix.Readlinkat(dir, name, buf)
		if err != nil {
			return "", &os.PathError{Op: "readlink", Path: name, Err: err}
		}
		if n < len(buf) {
			return string(buf[:n]), nil
		}
	}
	return "", &os.PathError{Op: "readlink", Path: name, Err: unix.ENAMETOOLONG}
}

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
	return regularFile(fd, path)
}

// regularFile returns fd, opened for path, if it is a regular file, or
// closes it.
func regularFile(fd int, path string) (*os.File, error) {
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
