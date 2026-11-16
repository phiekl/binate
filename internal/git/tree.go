// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// Mode is a tree entry's mode, as git normalizes it.
type Mode uint32

// The modes git writes.
const (
	ModeFile       Mode = 0o100644
	ModeExecutable Mode = 0o100755
	ModeSymlink    Mode = 0o120000
	ModeTree       Mode = 0o040000
	// ModeSubmodule is a submodule's commit, which this repository
	// doesn't have.
	ModeSubmodule Mode = 0o160000
)

// TreeEntry is an entry of a tree.
type TreeEntry struct {
	Mode Mode
	Name string
	ID   ID
}

// ParseTree parses the tree data, of format f. A mode is normalized as git
// does, so that one written by an old or other tool reads as git would
// read it. A name that git would refuse to check out, as one containing
// "/" or "..", is an error: such a tree is not well formed.
func ParseTree(f *Format, data []byte) ([]TreeEntry, error) {
	var entries []TreeEntry
	seen := map[string]bool{}
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		if sp < 0 {
			return nil, errors.New("tree entry without a mode")
		}
		mode, err := parseMode(data[:sp])
		if err != nil {
			return nil, err
		}
		data = data[sp+1:]
		nul := bytes.IndexByte(data, 0)
		if nul < 0 {
			return nil, errors.New("tree entry without a name's end")
		}
		name := string(data[:nul])
		if err := checkName(name); err != nil {
			return nil, err
		}
		data = data[nul+1:]
		if len(data) < f.size {
			return nil, errors.New("tree entry without an object name")
		}
		id, _ := IDFromBytes(f, data[:f.size])
		data = data[f.size:]
		if seen[name] {
			return nil, fmt.Errorf("tree entry %q twice", name)
		}
		seen[name] = true
		entries = append(entries, TreeEntry{Mode: mode, Name: name, ID: id})
	}
	return entries, nil
}

// parseMode parses a mode in octal, normalized as git's canon_mode does: a
// file is executable or not by its owner's execute bit, and only the type
// of anything else counts.
func parseMode(b []byte) (Mode, error) {
	if len(b) == 0 || len(b) > 7 {
		return 0, fmt.Errorf("invalid tree entry mode %q", b)
	}
	var m uint32
	for _, c := range b {
		if c < '0' || c > '7' {
			return 0, fmt.Errorf("invalid tree entry mode %q", b)
		}
		m = m<<3 | uint32(c-'0')
	}
	switch m & 0o170000 {
	case 0o100000:
		if m&0o100 != 0 {
			return ModeExecutable, nil
		}
		return ModeFile, nil
	case 0o120000:
		return ModeSymlink, nil
	case 0o040000:
		return ModeTree, nil
	case 0o160000:
		return ModeSubmodule, nil
	}
	return 0, fmt.Errorf("invalid tree entry mode %q", b)
}

// checkName checks that a tree entry's name names one entry: it is not
// empty, ".", "..", or holds a "/".
func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return fmt.Errorf("invalid tree entry name %q", name)
	}
	return nil
}
