// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/stat"
)

// Root is a tree of a repository as a side of the comparison, or a file,
// a blob, compared on its own. Its entries have the modes git records, and
// no times or owners.
type Root struct {
	repo *Repo
	tree ID
	// file is set for a file compared on its own, at "".
	file *TreeEntry
	name string
	// commit, unless zero, is the commit whose tree has the root at
	// prefix, "" for the tree itself: where the history of its entries
	// starts.
	commit ID
	prefix string

	mu sync.Mutex
	// dirs are the directories read, by path.
	dirs map[string]*dir
	// changes are the last changes found, by path.
	changes map[string]source.Change
}

// dir is a tree's entries.
type dir struct {
	names  []string
	byName map[string]TreeEntry
}

// NewRoot returns the tree id names in repo, as peeled by PeelTree, shown
// as name.
func NewRoot(repo *Repo, id ID, name string) (*Root, error) {
	tree, err := repo.PeelTree(id)
	if err != nil {
		return nil, err
	}
	return &Root{repo: repo, tree: tree, name: name, dirs: map[string]*dir{}}, nil
}

// NewTargetRoot returns the tree or file t in repo, shown as name, with
// the history of t's commit, if any.
func NewTargetRoot(repo *Repo, t Target, name string) *Root {
	r := &Root{repo: repo, name: name, dirs: map[string]*dir{}, commit: t.Commit, prefix: t.Path}
	if t.IsTree() {
		r.tree = t.ID
	} else {
		r.file = &TreeEntry{Mode: t.Mode, ID: t.ID}
	}
	return r
}

// IsFile reports whether the root is a file, not a tree.
func (r *Root) IsFile() bool { return r.file != nil }

// errSubmodule is the error of a submodule, whose commit is in another
// repository.
var errSubmodule = errors.New("a submodule, which is not read")

// errFollow is the error of following a symlink, which in a tree names
// nothing the tree can resolve for sure.
var errFollow = errors.New("symlinks are not followed in a git tree")

// maxTarget is the longest symlink target read.
const maxTarget = 64 << 10

// Path returns the path of the entry at rel as git names it: the root's
// name, and a colon and rel, or a slash and rel if the name has a path
// already.
func (r *Root) Path(rel string) string {
	switch {
	case rel == "":
		return r.name
	case strings.HasSuffix(r.name, ":") || strings.HasSuffix(r.name, "/"):
		return r.name + rel
	case strings.Contains(r.name, ":"):
		return r.name + "/" + rel
	}
	return r.name + ":" + rel
}

func (r *Root) pathError(op, rel string, err error) error {
	return &fs.PathError{Op: op, Path: r.Path(rel), Err: err}
}

// dir returns the entries of the directory at rel.
func (r *Root) dir(rel string) (*dir, error) {
	r.mu.Lock()
	d, ok := r.dirs[rel]
	r.mu.Unlock()
	if ok {
		return d, nil
	}
	if r.file != nil {
		return nil, unix.ENOTDIR
	}
	id := r.tree
	if rel != "" {
		e, err := r.lookup(rel)
		if err != nil {
			return nil, err
		}
		if e.Mode != ModeTree {
			return nil, unix.ENOTDIR
		}
		id = e.ID
	}
	entries, err := r.repo.ReadTree(id)
	if err != nil {
		return nil, err
	}
	d = &dir{names: make([]string, len(entries)), byName: make(map[string]TreeEntry, len(entries))}
	for i, e := range entries {
		d.names[i] = e.Name
		d.byName[e.Name] = e
	}
	r.mu.Lock()
	r.dirs[rel] = d
	r.mu.Unlock()
	return d, nil
}

// lookup returns the entry at rel: not the root, unless a file.
func (r *Root) lookup(rel string) (TreeEntry, error) {
	if r.file != nil {
		if rel != "" {
			return TreeEntry{}, unix.ENOTDIR
		}
		return *r.file, nil
	}
	parent, name := path.Split(rel)
	d, err := r.dir(strings.TrimSuffix(parent, "/"))
	if err != nil {
		return TreeEntry{}, err
	}
	e, ok := d.byName[name]
	if !ok {
		return TreeEntry{}, unix.ENOENT
	}
	return e, nil
}

// unixMode returns the mode of a file of mode m, as checked out with no
// umask but git's.
func unixMode(m Mode) uint32 {
	switch m {
	case ModeFile:
		return unix.S_IFREG | 0o644
	case ModeExecutable:
		return unix.S_IFREG | 0o755
	case ModeSymlink:
		return unix.S_IFLNK | 0o777
	case ModeTree:
		return unix.S_IFDIR | 0o755
	}
	return 0
}

func (r *Root) Entry(rel string, follow bool) (source.Entry, error) {
	if rel == "" && r.file == nil {
		return source.Entry{Mode: unix.S_IFDIR | 0o755}, nil
	}
	e, err := r.lookup(rel)
	if err != nil {
		return source.Entry{}, r.pathError("lstat", rel, err)
	}
	switch e.Mode {
	case ModeSubmodule:
		return source.Entry{}, r.pathError("lstat", rel, errSubmodule)
	case ModeSymlink:
		if follow {
			return source.Entry{}, r.pathError("stat", rel, errFollow)
		}
	}
	out := source.Entry{Mode: unixMode(e.Mode)}
	switch e.Mode {
	case ModeFile, ModeExecutable:
		if out.Size, err = r.repo.Size(e.ID); err != nil {
			return source.Entry{}, r.pathError("lstat", rel, err)
		}
	case ModeSymlink:
		target, err := r.target(e)
		out.Target, out.Size = target, int64(len(target))
		if err != nil {
			return out, r.pathError("readlink", rel, err)
		}
	}
	return out, nil
}

// target returns the target of the symlink e: its blob's contents.
func (r *Root) target(e TreeEntry) (string, error) {
	typ, data, err := r.repo.Read(e.ID, maxTarget)
	if err != nil {
		return "", err
	}
	if typ != Blob {
		return "", corrupt(e.ID, "symlink of a %s", typ)
	}
	return string(data), nil
}

func (r *Root) Stat(rel string) (*stat.Stat, error) {
	e, err := r.Entry(rel, false)
	if err != nil {
		return nil, err
	}
	return &stat.Stat{Mode: e.Mode, Size: e.Size, Target: e.Target}, nil
}

func (r *Root) ReadDir(rel string) ([]string, error) {
	d, err := r.dir(rel)
	if err != nil {
		return nil, r.pathError("open", rel, err)
	}
	return d.names, nil
}

func (r *Root) Open(rel string, follow bool) (source.File, error) {
	if rel == "" && r.file == nil {
		return nil, r.pathError("open", rel, files.ErrNotRegular)
	}
	e, err := r.lookup(rel)
	if err != nil {
		return nil, r.pathError("open", rel, err)
	}
	if e.Mode != ModeFile && e.Mode != ModeExecutable {
		if e.Mode == ModeSymlink && follow {
			return nil, r.pathError("open", rel, errFollow)
		}
		return nil, r.pathError("open", rel, files.ErrNotRegular)
	}
	obj, err := r.repo.Open(e.ID)
	if err != nil {
		return nil, r.pathError("open", rel, err)
	}
	if obj.Type != Blob {
		_ = obj.Close()
		return nil, r.pathError("open", rel, corrupt(e.ID, "file of a %s", obj.Type))
	}
	return &file{Object: obj, info: fileInfo{name: path.Base(r.Path(rel)), size: obj.Size, mode: fs.FileMode(unixMode(e.Mode) & 0o777)}}, nil
}

// ContentID returns the ID of the blob of the regular file at rel.
func (r *Root) ContentID(rel string) (source.ContentID, bool) {
	if rel == "" && r.file == nil {
		return source.ContentID{}, false
	}
	e, err := r.lookup(rel)
	if err != nil || (e.Mode != ModeFile && e.Mode != ModeExecutable) {
		return source.ContentID{}, false
	}
	return source.ContentID{Scheme: "git-" + r.repo.format.name, Sum: string(e.ID.Bytes())}, true
}

// HasHistory reports whether the root is of a commit.
func (r *Root) HasHistory() bool { return !r.commit.IsZero() }

// LastChange returns the commit that last changed the entry at rel, as
// "git log -1" finds it.
func (r *Root) LastChange(ctx context.Context, rel string) (source.Change, error) {
	if r.commit.IsZero() {
		return source.Change{}, errors.New("no history")
	}
	full := r.prefix
	if rel != "" {
		full = path.Join(r.prefix, rel)
	}
	r.mu.Lock()
	ch, ok := r.changes[full]
	r.mu.Unlock()
	if ok {
		return ch, nil
	}
	id, c, err := r.repo.LastChange(ctx, r.commit, full)
	if err != nil {
		return source.Change{}, r.pathError("log", rel, err)
	}
	ch = source.Change{
		ID: id.String(), AuthorName: c.AuthorName, AuthorEmail: c.AuthorEmail,
		Authored: c.Author, Committed: c.Committer,
	}
	r.mu.Lock()
	if r.changes == nil {
		r.changes = map[string]source.Change{}
	}
	r.changes[full] = ch
	r.mu.Unlock()
	return ch, nil
}

// file is a blob open for reading.
type file struct {
	Object
	info fileInfo
}

func (f *file) Stat() (fs.FileInfo, error) { return f.info, nil }

// fileInfo describes a blob as a file.
type fileInfo struct {
	name string
	size int64
	mode fs.FileMode
}

func (fi fileInfo) Name() string       { return fi.name }
func (fi fileInfo) Size() int64        { return fi.size }
func (fi fileInfo) Mode() fs.FileMode  { return fi.mode }
func (fi fileInfo) ModTime() time.Time { return time.Time{} }
func (fi fileInfo) IsDir() bool        { return false }
func (fi fileInfo) Sys() any           { return nil }

var (
	_ source.ContentIDs = (*Root)(nil)
	_ source.History    = (*Root)(nil)
	_ io.ReadCloser     = (*file)(nil)
)
