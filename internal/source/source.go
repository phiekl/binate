// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package source reads a side of the comparison: what its entries are,
// what a directory holds and what a file contains. Entries are named by
// their path relative to the side's root, slash-separated, "" for the root
// itself.
package source

import (
	"context"
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
	// ReadDir returns the names in the directory at rel, in no order. The
	// slice may be shared, and must not be changed.
	ReadDir(rel string) ([]string, error)
	// Open opens the entry at rel for reading if it is a regular file,
	// following a final symlink if follow. It never waits on a FIFO or
	// opens a terminal put in a file's place.
	Open(rel string, follow bool) (File, error)
}

// ModeGitlink is the type, in Mode, of a git submodule: a commit of another
// repository, named by Target, as git's S_IFGITLINK. No file has it.
const ModeGitlink = 0o160000

// Entry is what the scan compares of an entry: less than stat.Stat, and
// cheaper to read.
type Entry struct {
	// Mode is the type and permission bits, as st_mode; 0 if unknown.
	Mode uint32
	// Size is -1 for a regular file whose size costs to read, which a root
	// of ContentIDs may leave unknown: it is compared by its ID instead.
	// Stat reads it.
	Size     int64
	Uid, Gid uint32
	// Rdev is a device's number.
	Rdev  uint64
	Mtime time.Time
	// Target is a symlink's target, or a submodule's commit.
	Target string
}

// File is an open regular file.
type File interface {
	io.ReadCloser
	Stat() (fs.FileInfo, error)
}

// ContentIDs is a Root that names its files by a hash of their contents,
// as a git tree does, so that files can be compared without reading them.
type ContentIDs interface {
	Root
	// ContentID returns the ID of the regular file at rel, or false if it
	// has none, as for an entry that is not a regular file. Two files with
	// IDs of the same Scheme have the same contents exactly when their
	// IDs are equal.
	ContentID(rel string) (ContentID, bool)
}

// ContentID is the ID of a file's contents.
type ContentID struct {
	Scheme string
	Sum    string
}

// History is a Root with a history of changes, as a git commit's tree.
type History interface {
	Root
	// HasHistory reports whether the root has a history, as a git
	// commit's tree has but one named otherwise not.
	HasHistory() bool
	// LastChange returns the change that last changed the entry at rel,
	// of a root with a history. It may take long, and is canceled with
	// ctx.
	LastChange(ctx context.Context, rel string) (Change, error)
}

// RepoPaths is a Root in a repository, as a git commit's tree.
type RepoPaths interface {
	Root
	// RepoPath returns the path of the entry at rel in the repository,
	// from its top, as git commands take it, or false if that isn't
	// known.
	RepoPath(rel string) (string, bool)
}

// Change is a change in a History: a commit.
type Change struct {
	// ID names the change, as a commit's hash.
	ID string
	// AuthorName and AuthorEmail are who made the change.
	AuthorName, AuthorEmail string
	// Authored is when the change was made, and Committed when it was
	// recorded, each in the zone recorded.
	Authored, Committed time.Time
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
