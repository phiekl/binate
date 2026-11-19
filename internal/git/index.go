// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/stat"
)

// errUnmerged is the error of a path with conflicting entries in the
// index, which has no single version staged.
var errUnmerged = errors.New("unmerged path")

// IndexRoot is the repository's index as a side of the comparison: what
// is staged, as git diff --cached compares it with a commit and git diff
// with the work tree. Its entries have the modes git records, and no
// times or owners.
type IndexRoot struct {
	repo *Repository

	// The index is read on first use, which for a reload is in the scan
	// rather than on the caller's goroutine.
	loadOnce sync.Once
	loadErr  error
	entries  map[string]stagedEntry
	// tree is the directories leading to entries.
	tree pathTree
}

// stagedEntry is a path of the index: its staged version, or none where
// it is unmerged.
type stagedEntry struct {
	mode     Mode
	id       ID
	unmerged bool
}

// OpenIndex returns the index of a repository with a work tree. A missing
// index has nothing staged.
func (r *Repository) OpenIndex() (*IndexRoot, error) {
	if r.loc.WorkTree == "" {
		return nil, errors.New("the repository has no work tree")
	}
	x := &IndexRoot{repo: r}
	if err := x.loaded(); err != nil {
		return nil, err
	}
	return x, nil
}

// Reload returns a snapshot for a new scan, which reads the index again
// when first listing or reading below the top.
func (x *IndexRoot) Reload() (source.Root, error) {
	return &IndexRoot{repo: x.repo}, nil
}

func (x *IndexRoot) loaded() error {
	x.loadOnce.Do(func() { x.loadErr = x.load() })
	return x.loadErr
}

func (x *IndexRoot) load() error {
	name := filepath.Join(x.repo.loc.GitDir, "index")
	x.entries = map[string]stagedEntry{}
	data, err := readLimited(name, maxWorkIndex)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	err = walkIndex(x.repo.format, []byte(data), func(e indexEntry) {
		switch {
		case e.stage == 0 && e.intentToAdd:
			// Added with git add -N: Git compares it as not staged.
			return
		case e.stage == 0:
			x.add(e.name, stagedEntry{mode: e.mode, id: e.id})
		default:
			if _, ok := x.entries[e.name]; !ok {
				x.add(e.name, stagedEntry{mode: e.mode, unmerged: true})
			}
		}
	})
	if err != nil {
		return &fs.PathError{Op: "read", Path: name, Err: err}
	}
	return nil
}

// add adds the entry at name, and the directories leading to it.
func (x *IndexRoot) add(name string, e stagedEntry) {
	x.tree.add(name)
	x.entries[name] = e
}

// IsIndex reports true: the index is no revision.
func (x *IndexRoot) IsIndex() bool { return true }

// Path returns how git names the entry at rel in the index, as :rel.
func (x *IndexRoot) Path(rel string) string {
	if rel == "" {
		return "(index)"
	}
	return ":" + rel
}

func (x *IndexRoot) pathError(op, rel string, err error) error {
	return &fs.PathError{Op: op, Path: x.Path(rel), Err: err}
}

// lookup returns the entry at rel, or whether it is a directory.
func (x *IndexRoot) lookup(rel string) (stagedEntry, bool, error) {
	if rel != "" {
		if err := x.loaded(); err != nil {
			return stagedEntry{}, false, err
		}
	}
	if e, ok := x.entries[rel]; ok {
		if e.unmerged {
			return stagedEntry{}, false, errUnmerged
		}
		return e, false, nil
	}
	if rel == "" || x.tree.isDir(rel) {
		return stagedEntry{}, true, nil
	}
	return stagedEntry{}, false, unix.ENOENT
}

func (x *IndexRoot) Entry(rel string, follow bool) (source.Entry, error) {
	e, dir, err := x.lookup(rel)
	switch {
	case err != nil:
		return source.Entry{}, x.pathError("lstat", rel, err)
	case dir:
		return source.Entry{Mode: unixMode(ModeTree)}, nil
	}
	switch e.mode {
	case ModeSubmodule:
		return source.Entry{Mode: source.ModeGitlink, Target: e.id.String()}, nil
	case ModeSymlink:
		if follow {
			return source.Entry{}, x.pathError("stat", rel, errFollow)
		}
		target, err := x.repo.blob(e.id, maxTarget)
		out := source.Entry{Mode: unixMode(ModeSymlink), Target: string(target), Size: int64(len(target))}
		if err != nil {
			return out, x.pathError("readlink", rel, err)
		}
		return out, nil
	}
	// Compared by its blob's ID, as a revision's file is.
	return source.Entry{Mode: unixMode(e.mode), Size: -1}, nil
}

func (x *IndexRoot) Stat(rel string) (*stat.Stat, error) {
	entry, err := x.Entry(rel, false)
	if err != nil {
		return nil, err
	}
	if entry.Size < 0 {
		e, _, _ := x.lookup(rel)
		if entry.Size, err = x.repo.Size(e.id); err != nil {
			return nil, x.pathError("lstat", rel, err)
		}
	}
	return &stat.Stat{Mode: entry.Mode, Size: entry.Size, Target: entry.Target}, nil
}

func (x *IndexRoot) ReadDir(rel string) ([]string, error) {
	if err := x.loaded(); err != nil {
		return nil, err
	}
	if rel == "" || x.tree.isDir(rel) {
		return x.tree.names[rel], nil
	}
	if _, ok := x.entries[rel]; ok {
		return nil, x.pathError("open", rel, unix.ENOTDIR)
	}
	return nil, x.pathError("open", rel, unix.ENOENT)
}

func (x *IndexRoot) Open(rel string, follow bool) (source.File, error) {
	e, dir, err := x.lookup(rel)
	switch {
	case err != nil:
		return nil, x.pathError("open", rel, err)
	case dir:
		return nil, x.pathError("open", rel, files.ErrNotRegular)
	case e.mode == ModeSymlink && follow:
		return nil, x.pathError("open", rel, errFollow)
	}
	f, err := x.repo.openBlob(e.id, e.mode, path.Base(rel))
	if err != nil {
		return nil, x.pathError("open", rel, err)
	}
	return f, nil
}

// ContentID returns the ID of the blob staged for the regular file at rel.
func (x *IndexRoot) ContentID(rel string) (source.ContentID, bool) {
	e, dir, err := x.lookup(rel)
	if err != nil || dir || (e.mode != ModeFile && e.mode != ModeExecutable) {
		return source.ContentID{}, false
	}
	return blobContentID(x.repo.format, e.id), true
}

// RepoPath returns the path of the entry at rel from the top of the
// repository, which the index's paths are.
func (x *IndexRoot) RepoPath(rel string) (string, bool) {
	if rel == "" {
		return ".", true
	}
	return rel, true
}

// blob reads the blob id, of at most limit bytes.
func (r *Repository) blob(id ID, limit int64) ([]byte, error) {
	typ, data, err := r.Read(id, limit)
	if err == nil && typ != Blob {
		err = corrupt(id, "%s in place of a blob", typ)
	}
	return data, err
}

// openBlob opens the blob id of a regular file of mode, named name.
func (r *Repository) openBlob(id ID, mode Mode, name string) (source.File, error) {
	if mode != ModeFile && mode != ModeExecutable {
		return nil, files.ErrNotRegular
	}
	obj, err := r.Open(id)
	if err != nil {
		return nil, err
	}
	if obj.Type != Blob {
		_ = obj.Close()
		return nil, corrupt(id, "file of a %s", obj.Type)
	}
	return &file{Object: obj, info: fileInfo{name: name, size: obj.Size, mode: fs.FileMode(unixMode(mode) & 0o777)}}, nil
}

var (
	_ source.ContentIDs = (*IndexRoot)(nil)
	_ source.Index      = (*IndexRoot)(nil)
	_ source.RepoPaths  = (*IndexRoot)(nil)
	_ source.Reloadable = (*IndexRoot)(nil)
)
