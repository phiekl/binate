// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package git reads a git repository's objects, without running git: loose
// objects, packs and alternates, in either object format. It reads only
// what it is given: finding the repository and resolving refs is left to
// the caller. Everything read is checked for being well formed, as it
// may come from anyone.
package git

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
)

// Format is an object format: the hash that names objects.
type Format struct {
	name string
	size int
	new  func() hash.Hash
}

// The object formats.
var (
	SHA1   = &Format{"sha1", sha1.Size, sha1.New}
	SHA256 = &Format{"sha256", sha256.Size, sha256.New}
)

// ParseFormat returns the format named name, as extensions.objectFormat
// names it.
func ParseFormat(name string) (*Format, error) {
	switch name {
	case "sha1":
		return SHA1, nil
	case "sha256":
		return SHA256, nil
	}
	return nil, fmt.Errorf("unknown object format %q", name)
}

func (f *Format) String() string { return f.name }

// Size returns the size of an object name in bytes.
func (f *Format) Size() int { return f.size }

// ID is an object name.
type ID struct {
	raw [sha256.Size]byte
	n   uint8
}

// IDFromBytes returns the ID whose raw bytes are b, of format f.
func IDFromBytes(f *Format, b []byte) (ID, error) {
	if len(b) != f.size {
		return ID{}, fmt.Errorf("object name of %d bytes, not %d", len(b), f.size)
	}
	var id ID
	id.n = uint8(copy(id.raw[:], b))
	return id, nil
}

// ParseID returns the ID written as hex in s, of format f.
func ParseID(f *Format, s string) (ID, error) {
	if len(s) != 2*f.size {
		return ID{}, fmt.Errorf("invalid %s object name %q", f.name, s)
	}
	var id ID
	if _, err := hex.Decode(id.raw[:], []byte(s)); err != nil {
		return ID{}, fmt.Errorf("invalid %s object name %q", f.name, s)
	}
	id.n = uint8(f.size)
	return id, nil
}

// Bytes returns the raw bytes of id.
func (id ID) Bytes() []byte { return id.raw[:id.n] }

// String returns id as hex.
func (id ID) String() string { return hex.EncodeToString(id.Bytes()) }

// IsZero reports whether id is the zero ID, which names nothing.
func (id ID) IsZero() bool { return id == ID{} }

// Type is an object's type.
type Type uint8

// The object types, numbered as in packs.
const (
	Commit Type = 1
	Tree   Type = 2
	Blob   Type = 3
	Tag    Type = 4
)

func (t Type) String() string {
	switch t {
	case Commit:
		return "commit"
	case Tree:
		return "tree"
	case Blob:
		return "blob"
	case Tag:
		return "tag"
	}
	return fmt.Sprintf("type %d", uint8(t))
}

// parseType returns the type named name, as in a loose object's header.
func parseType(name []byte) (Type, bool) {
	switch string(name) {
	case "commit":
		return Commit, true
	case "tree":
		return Tree, true
	case "blob":
		return Blob, true
	case "tag":
		return Tag, true
	}
	return 0, false
}

// ErrNotFound is the error of an object the repository doesn't have, as
// one a partial clone left out.
var ErrNotFound = errors.New("object not found")

// CorruptError is the error of an object that is not well formed.
type CorruptError struct {
	ID     ID
	Reason string
}

func (e *CorruptError) Error() string {
	if e.ID.IsZero() {
		return "corrupt object: " + e.Reason
	}
	return "corrupt object " + e.ID.String() + ": " + e.Reason
}

func corrupt(id ID, format string, args ...any) error {
	return &CorruptError{ID: id, Reason: fmt.Sprintf(format, args...)}
}
