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

// DiskRoot is a root in the file system. Editable reports whether the
// supervising process has a target for its regular files.
type DiskRoot interface {
	Root
	Editable() bool
}

// Reloadable is a root whose listing uses metadata read for each scan.
type Reloadable interface {
	Root
	Reload() (Root, error)
}

// Index is a root of what a version control system's index has staged, as
// git's, which is no revision. IsIndex reports true.
type Index interface {
	Root
	IsIndex() bool
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
	// Ino and Ctime are the inode and status change time of an entry on
	// disk, which a work tree's status compares, or zero.
	Ino   uint64
	Ctime time.Time
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
	// Subject is the first paragraph of its message, in a line.
	Subject string
}

// Changes is a History whose changes can be shown, as a git
// repository's commits.
type Changes interface {
	History
	RepoPaths
	// ShowChange returns the change id described as "git show --no-patch"
	// does.
	ShowChange(id string) (string, error)
	// ChangeTrees returns the trees before and after the change id, the
	// one before as of its first parent, or empty if it has none, and its
	// number of parents.
	ChangeTrees(id string) (before, after Root, parents int, err error)
	// ChangeStat counts the lines the change id changed, against its
	// first parent, and returns the lines describing them, as "git show
	// --stat --summary" does, for a width. It may take long, and is
	// canceled with ctx.
	ChangeStat(ctx context.Context, id string) (func(width int) []string, error)
}

// Disk is a side in the file system, at the path given. Its errors carry
// the path, as those of package os do.
type Disk string

func (d Disk) Editable() bool { return true }

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
		Mtime: time.Unix(st.Mtim.Unix()), Ino: st.Ino, Ctime: time.Unix(st.Ctim.Unix()),
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		e.Target, err = os.Readlink(p)
	}
	return e, err
}

func (d Disk) Stat(rel string) (*stat.Stat, error) {
	return stat.Read(d.Path(rel))
}

// ReadDir opens only a directory, which the entry at rel may no longer be
// since it was found one: a FIFO put in its place, which would wait for a
// writer, or a device, which opening may change, is refused unopened, and
// a symlink isn't followed, but for the root, as given.
func (d Disk) ReadDir(rel string) ([]string, error) {
	p := d.Path(rel)
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC
	if rel != "" {
		flags |= unix.O_NOFOLLOW
	}
	fd, err := unix.Open(p, flags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: p, Err: err}
	}
	f := os.NewFile(uintptr(fd), p)
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

// Nothing is a side with no entries, given as /dev/null, at the path
// given, which every entry shows. Opposite a tree it is an empty tree, and
// opposite a file a missing one, so that everything on the other side is
// added or removed. It is never read: every entry is missing, the root
// too.
type Nothing string

// Editable is false: there is nothing to edit.
func (n Nothing) Editable() bool { return false }

// Path returns the path given, for every entry.
func (n Nothing) Path(string) string { return string(n) }

func (n Nothing) Entry(rel string, _ bool) (Entry, error) {
	return Entry{}, n.missing("stat", rel)
}

func (n Nothing) Stat(rel string) (*stat.Stat, error) {
	return nil, n.missing("stat", rel)
}

func (n Nothing) ReadDir(rel string) ([]string, error) {
	return nil, n.missing("open", rel)
}

func (n Nothing) Open(rel string, _ bool) (File, error) {
	return nil, n.missing("open", rel)
}

// missing is the error of the entry at rel, which is not there.
func (n Nothing) missing(op, rel string) error {
	return &os.PathError{Op: op, Path: n.Path(rel), Err: unix.ENOENT}
}

// GitModes is a Disk side compared with a side of git, with the modes git
// records: a regular file 0644, or 0755 if its owner may run it, a
// directory 0755 and a symlink 0777. Other types keep theirs, which git
// can't record.
type GitModes struct{ Disk }

func (g GitModes) Entry(rel string, follow bool) (Entry, error) {
	e, err := g.Disk.Entry(rel, follow)
	e.Mode = gitMode(e.Mode)
	return e, err
}

func (g GitModes) Stat(rel string) (*stat.Stat, error) {
	st, err := g.Disk.Stat(rel)
	if st != nil {
		st.Mode = gitMode(st.Mode)
	}
	return st, err
}

// gitMode returns the mode git records of an entry of mode m.
func gitMode(m uint32) uint32 {
	switch m & unix.S_IFMT {
	case unix.S_IFREG:
		if m&unix.S_IXUSR != 0 {
			return unix.S_IFREG | 0o755
		}
		return unix.S_IFREG | 0o644
	case unix.S_IFDIR:
		return unix.S_IFDIR | 0o755
	case unix.S_IFLNK:
		return unix.S_IFLNK | 0o777
	}
	return m
}

// AsDisk returns the Disk that root is, or shows with other modes.
func AsDisk(root Root) (Disk, bool) {
	switch r := root.(type) {
	case Disk:
		return r, true
	case GitModes:
		return r.Disk, true
	}
	return "", false
}

// Status is an entry's state in a version control system's work tree, as
// git status --short gives it: Staged is the letter of what is staged
// against the last commit, and Unstaged of what is not, each ' ' for
// nothing. An untracked entry is "??", and an unmerged one has the letters
// of its conflict, as "UU". The zero Status is none: an entry that is
// neither, or one not compared.
type Status struct {
	Staged, Unstaged byte
}

// IsZero reports whether s is no status: none to show.
func (s Status) IsZero() bool {
	return s.Staged == 0 && s.Unstaged == 0 || s.Staged == ' ' && s.Unstaged == ' '
}

// String returns the two letters, as git status --short shows them, or ""
// for none.
func (s Status) String() string {
	if s.IsZero() {
		return ""
	}
	return string([]byte{s.Staged, s.Unstaged})
}

// Untracked reports whether s is of an untracked entry.
func (s Status) Untracked() bool { return s.Staged == '?' && s.Unstaged == '?' }

// Unmerged reports whether s is of an entry with a merge conflict.
func (s Status) Unmerged() bool {
	switch s.String() {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	}
	return false
}

// Statuses is a Root whose entries have a Status, as a git work tree's
// files are staged or not.
type Statuses interface {
	Root
	// Status returns the status of the entry at rel, given as Entry read
	// it, or nil if the root has none: one it has not may have a status
	// still, as a file deleted. It may read a file's contents, until ctx
	// is canceled.
	Status(ctx context.Context, rel string, e *Entry) (Status, error)
	// Recorded returns the entries the directory at rel holds as recorded
	// apart from the root, as in a commit or index, which the root may
	// not list but may have a status, as a file deleted.
	Recorded(rel string) []Recorded
}

// Recorded is an entry as Statuses records it: its name and the type
// bits of its mode, as st_mode's, and whether entries are recorded below
// it, as for a directory, whatever its mode, as for a file staged in place
// of one.
type Recorded struct {
	Name  string
	Mode  uint32
	Below bool
}
