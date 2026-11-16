// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// minAbbrev is the shortest abbreviated object name taken, as git takes.
const minAbbrev = 4

// prefix is an abbreviated object name: whole bytes, and maybe the high
// half of one more.
type prefix struct {
	hex  string
	full []byte
	half int // the last half byte, shifted high, or -1
}

func parsePrefix(s string) (prefix, bool) {
	if len(s) < minAbbrev || !isHex(s) {
		return prefix{}, false
	}
	s = strings.ToLower(s)
	p := prefix{hex: s, half: -1}
	for i := 0; i+1 < len(s); i += 2 {
		p.full = append(p.full, unhex(s[i])<<4|unhex(s[i+1]))
	}
	if len(s)%2 == 1 {
		p.half = int(unhex(s[len(s)-1])) << 4
	}
	return p, true
}

func unhex(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return c - 'a' + 10
}

// matches reports whether the raw name b starts with p.
func (p prefix) matches(b []byte) bool {
	if !bytes.HasPrefix(b, p.full) {
		return false
	}
	return p.half < 0 || len(b) > len(p.full) && int(b[len(p.full)]&0xf0) == p.half
}

// findPrefix returns the objects whose names start with p, up to two: enough
// to tell whether one alone does.
func (r *Repo) findPrefix(p prefix) ([]ID, error) {
	var found []ID
	add := func(id ID) {
		for _, f := range found {
			if f == id {
				return
			}
		}
		found = append(found, id)
	}
	for _, pk := range r.packs {
		ids, err := pk.idx.withPrefix(p, r.format, 2)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			add(id)
		}
		if len(found) > 1 {
			return found, nil
		}
	}
	for _, dir := range r.dirs {
		names, err := readDirNames(filepath.Join(dir, p.hex[:2]))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sort.Strings(names)
		for _, name := range names {
			if !strings.HasPrefix(name, p.hex[2:]) {
				continue
			}
			id, err := ParseID(r.format, p.hex[:2]+name)
			if err != nil {
				// Not an object, as a temporary file.
				continue
			}
			add(id)
			if len(found) > 1 {
				return found, nil
			}
		}
	}
	return found, nil
}

// withPrefix returns up to max objects in the index whose names start with
// p.
func (x *packIndex) withPrefix(p prefix, f *Format, max int) ([]ID, error) {
	var ids []ID
	err := safely(func() {
		lo := sort.Search(x.n, func(i int) bool {
			return bytes.Compare(x.names[i*x.hs:(i+1)*x.hs], p.full) >= 0
		})
		for i := lo; i < x.n && len(ids) < max; i++ {
			name := x.names[i*x.hs : (i+1)*x.hs]
			if !p.matches(name) {
				if bytes.HasPrefix(name, p.full) {
					continue
				}
				break
			}
			id, _ := IDFromBytes(f, name)
			ids = append(ids, id)
		}
	})
	return ids, err
}

// errAmbiguous is the error of an abbreviated name of more than one object.
var errAmbiguous = errors.New("ambiguous")

// expand returns the one object whose name starts with s.
func (r *Repo) expand(s string) (ID, error) {
	p, ok := parsePrefix(s)
	if !ok {
		return ID{}, errNoRef
	}
	found, err := r.findPrefix(p)
	switch {
	case err != nil:
		return ID{}, err
	case len(found) == 0:
		return ID{}, errNoRef
	case len(found) > 1:
		return ID{}, fmt.Errorf("short object name %s is %w", s, errAmbiguous)
	}
	return found[0], nil
}
