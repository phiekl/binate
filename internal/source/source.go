// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package source reads a side of the comparison: what its entries are,
// what a directory holds and what a file contains. Entries are named by
// their path relative to the side's root, slash-separated, "" for the root
// itself.
package source

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/stat"
)

// Root is a side: a directory compared as a tree, or a file compared on
// its own. Its methods are safe for concurrent use.
type Root interface {
	// Path returns the path of the entry at rel, as shown and edited.
	Path(rel string) string
	// Entry reads what the scan compares of the entry at rel, following
	// a final symlink if follow. A symlink whose target can't be read is
	// returned with that error.
	Entry(rel string, follow bool) (Entry, error)
	// Stat reads all the panels show of the entry at rel, without
	// following a symlink.
	Stat(rel string) (*stat.Stat, error)
	// ReadDir returns the names in the directory at rel, in no order.
	ReadDir(rel string) ([]string, error)
	// Open opens the entry at rel for reading if it is a regular file,
	// following a final symlink if follow. It never waits on a FIFO or
	// opens a terminal put in a file's place.
	Open(rel string, follow bool) (File, error)
}

// Entry is what the scan compares of an entry: less than stat.Stat, and
// cheaper to read.
type Entry struct {
	// Mode is the type and permission bits, as st_mode; 0 if unknown.
	Mode     uint32
	Size     int64
	Uid, Gid uint32
	// Rdev is a device's number.
	Rdev  uint64
	Mtime time.Time
	// Target is a symlink's target.
	Target string
}

// File is an open regular file.
type File interface {
	io.ReadCloser
	Stat() (fs.FileInfo, error)
}

// Disk is a side in the file system, at the path given. Its errors carry
// the path, as those of package os do.
type Disk string

// Path returns the root joined with rel, or the root as given for "".
func (d Disk) Path(rel string) string {
	if rel == "" {
		return string(d)
	}
	return filepath.Join(string(d), filepath.FromSlash(rel))
}

func (d Disk) Entry(rel string, follow bool) (Entry, error) {
	p := d.Path(rel)
	var st unix.Stat_t
	var err error
	if follow {
		err = unix.Stat(p, &st)
	} else {
		err = unix.Lstat(p, &st)
	}
	if err != nil {
		return Entry{}, &os.PathError{Op: "stat", Path: p, Err: err}
	}
	e := Entry{
		Mode: st.Mode, Size: st.Size, Uid: st.Uid, Gid: st.Gid, Rdev: st.Rdev,
		Mtime: time.Unix(st.Mtim.Unix()),
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		e.Target, err = os.Readlink(p)
	}
	return e, err
}

func (d Disk) Stat(rel string) (*stat.Stat, error) {
	return stat.Read(d.Path(rel))
}

func (d Disk) ReadDir(rel string) ([]string, error) {
	f, err := os.Open(d.Path(rel))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.Readdirnames(-1)
}

func (d Disk) Open(rel string, follow bool) (File, error) {
	f, err := files.OpenRegular(d.Path(rel), follow)
	if err != nil {
		// A nil *os.File must not become a non-nil File.
		return nil, err
	}
	return f, nil
}
