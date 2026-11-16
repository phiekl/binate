// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// Repo reads the objects of a repository. It is safe for concurrent use.
type Repo struct {
	format *Format
	// dirs are the objects directories searched, the repository's first.
	dirs []string
}

// OpenRepo returns the repository whose objects directory is dir, of
// format f.
func OpenRepo(dir string, f *Format) (*Repo, error) {
	return &Repo{format: f, dirs: []string{dir}}, nil
}

// Format returns the repository's object format.
func (r *Repo) Format() *Format { return r.format }

// Object is an object being read: its type, its size, and a reader of its
// content, which fails at its end if the object turns out corrupt.
type Object struct {
	Type Type
	Size int64
	io.ReadCloser
}

// Open opens the object id.
func (r *Repo) Open(id ID) (Object, error) {
	for _, dir := range r.dirs {
		obj, err := openLoose(dir, id)
		if !errors.Is(err, ErrNotFound) {
			return obj, err
		}
	}
	return Object{}, fmt.Errorf("%s: %w", id, ErrNotFound)
}

// Info returns the type and size of the object id.
func (r *Repo) Info(id ID) (Type, int64, error) {
	obj, err := r.Open(id)
	if err != nil {
		return 0, 0, err
	}
	_ = obj.Close()
	return obj.Type, obj.Size, nil
}

// Read returns the type and content of the object id, which must not be
// larger than max bytes.
func (r *Repo) Read(id ID, max int64) (Type, []byte, error) {
	obj, err := r.Open(id)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = obj.Close() }()
	if obj.Size > max {
		return 0, nil, fmt.Errorf("%s: %s of %d bytes, larger than %d", id, obj.Type, obj.Size, max)
	}
	data := make([]byte, obj.Size)
	if _, err := io.ReadFull(obj, data); err != nil {
		return 0, nil, err
	}
	// Read to the end, which checks the object.
	if _, err := obj.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		if err == nil {
			err = corrupt(id, "longer than its size")
		}
		return 0, nil, err
	}
	return obj.Type, data, nil
}

// maxTree is the largest tree, commit or tag read, as a corrupt or hostile
// repository could claim any size. Git's own largest trees are a few
// megabytes.
const maxTree = 256 << 20

// ReadTree returns the entries of the tree id.
func (r *Repo) ReadTree(id ID) ([]TreeEntry, error) {
	typ, data, err := r.Read(id, maxTree)
	if err != nil {
		return nil, err
	}
	if typ != Tree {
		return nil, fmt.Errorf("%s is a %s, not a tree", id, typ)
	}
	entries, err := ParseTree(r.format, data)
	if err != nil {
		return nil, corrupt(id, "%v", err)
	}
	return entries, nil
}

// maxPeel is how many tags are followed to a commit or tree, past which a
// chain is taken to be a loop.
const maxPeel = 32

// PeelTree returns the tree id names: id itself if a tree, a commit's
// tree, or that of what a tag points to.
func (r *Repo) PeelTree(id ID) (ID, error) {
	for range maxPeel {
		typ, data, err := r.Read(id, maxTree)
		if err != nil {
			return ID{}, err
		}
		switch typ {
		case Tree:
			return id, nil
		case Commit:
			tree, ok := header(data, "tree")
			if !ok {
				return ID{}, corrupt(id, "commit without a tree")
			}
			t, err := ParseID(r.format, tree)
			if err != nil {
				return ID{}, corrupt(id, "%v", err)
			}
			return t, nil
		case Tag:
			obj, ok := header(data, "object")
			if !ok {
				return ID{}, corrupt(id, "tag without an object")
			}
			next, err := ParseID(r.format, obj)
			if err != nil {
				return ID{}, corrupt(id, "%v", err)
			}
			id = next
		default:
			return ID{}, fmt.Errorf("%s is a %s, not a tree, commit or tag", id, typ)
		}
	}
	return ID{}, fmt.Errorf("%s: more than %d tags in a row", id, maxPeel)
}

// header returns the value of the header field name of a commit or tag:
// the lines before the first empty one.
func header(data []byte, name string) (string, bool) {
	for len(data) > 0 {
		line, rest, _ := bytes.Cut(data, []byte{'\n'})
		if len(line) == 0 {
			break
		}
		if k, v, ok := bytes.Cut(line, []byte{' '}); ok && string(k) == name {
			return string(v), true
		}
		data = rest
	}
	return "", false
}
