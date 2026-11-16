// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
)

// errNoRef is the error of a ref that doesn't exist.
var errNoRef = errors.New("no such ref")

// checkRefName checks that name is a ref name git would write: HEAD, a
// pseudoref in capitals such as ORIG_HEAD, or a name under refs/ by git's
// check_refname_format. As a ref is read by its name as a path, this
// also keeps it beneath the git directory.
func checkRefName(name string) error {
	if isPseudoref(name) {
		return nil
	}
	if !strings.HasPrefix(name, "refs/") {
		return fmt.Errorf("invalid ref name %q", name)
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") ||
		strings.Contains(name, "..") || strings.Contains(name, "@{") {
		return fmt.Errorf("invalid ref name %q", name)
	}
	for _, c := range []byte(name) {
		if c < 0x20 || c == 0x7f || strings.IndexByte(" ~^:?*[\\", c) >= 0 {
			return fmt.Errorf("invalid ref name %q", name)
		}
	}
	for comp := range strings.SplitSeq(name, "/") {
		if comp == "" || comp[0] == '.' || strings.HasSuffix(comp, ".lock") {
			return fmt.Errorf("invalid ref name %q", name)
		}
	}
	return nil
}

// isPseudoref reports whether name is HEAD or a ref like it at the top,
// in capitals and underscores, as ORIG_HEAD or FETCH_HEAD.
func isPseudoref(name string) bool {
	if name == "" || name[0] == '_' {
		return false
	}
	for _, c := range []byte(name) {
		if !(c >= 'A' && c <= 'Z' || c == '_') {
			return false
		}
	}
	return true
}

// refDir returns the directory holding the ref name, as a file: a work
// tree's own refs are in its git directory, the rest in the common one.
func (r *Repository) refDir(name string) string {
	if isPseudoref(name) || strings.HasPrefix(name, "refs/bisect/") ||
		strings.HasPrefix(name, "refs/worktree/") || strings.HasPrefix(name, "refs/rewritten/") {
		return r.loc.GitDir
	}
	return r.loc.CommonDir
}

// maxSymref is how many symrefs are followed, as git follows them.
const maxSymref = 5

// resolveRef returns the object the ref name points to, following symrefs,
// or errNoRef.
func (r *Repository) resolveRef(name string) (ID, error) {
	for range maxSymref + 1 {
		if err := checkRefName(name); err != nil {
			return ID{}, err
		}
		id, target, err := r.readRef(name)
		if err != nil || target == "" {
			return id, err
		}
		name = target
	}
	return ID{}, fmt.Errorf("symrefs nested more than %d deep", maxSymref)
}

// readRef reads the ref name: its object, or the ref it points to as a
// symref.
func (r *Repository) readRef(name string) (ID, string, error) {
	path := filepath.Join(r.refDir(name), filepath.FromSlash(name))
	data, err := readSmall(path)
	switch {
	case err == nil:
		return r.parseRef(name, data)
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, files.ErrNotRegular):
		// Not loose, as where a directory of refs is: maybe packed.
	default:
		return ID{}, "", err
	}
	if !strings.HasPrefix(name, "refs/") {
		return ID{}, "", errNoRef
	}
	id, ok, err := r.packed.find(name)
	if err != nil {
		return ID{}, "", err
	}
	if !ok {
		return ID{}, "", errNoRef
	}
	return id, "", nil
}

// parseRef parses a loose ref's contents: "ref: target" of a symref, or an
// object name followed by nothing but whitespace, or, as in FETCH_HEAD,
// whatever follows a tab or space.
func (r *Repository) parseRef(name, data string) (ID, string, error) {
	if target, ok := strings.CutPrefix(data, "ref:"); ok {
		target = strings.TrimSpace(target)
		if err := checkRefName(target); err != nil {
			return ID{}, "", fmt.Errorf("ref %s: %w", name, err)
		}
		return ID{}, target, nil
	}
	hs := 2 * r.format.size
	if len(data) < hs || len(data) > hs && !strings.ContainsRune(" \t\r\n", rune(data[hs])) {
		return ID{}, "", fmt.Errorf("ref %s: invalid contents", name)
	}
	id, err := ParseID(r.format, data[:hs])
	if err != nil {
		return ID{}, "", fmt.Errorf("ref %s: %w", name, err)
	}
	return id, "", nil
}

// packedRefs is the packed-refs file, read once when first needed.
type packedRefs struct {
	path   string
	format *Format
	once   sync.Once
	refs   map[string]ID
	err    error
}

// find returns the object of the packed ref name.
func (p *packedRefs) find(name string) (ID, bool, error) {
	p.once.Do(p.load)
	if p.err != nil {
		return ID{}, false, p.err
	}
	id, ok := p.refs[name]
	return id, ok, nil
}

// maxPackedRefs is the largest packed-refs file read.
const maxPackedRefs = 256 << 20

func (p *packedRefs) load() {
	p.refs = map[string]ID{}
	data, err := readLimited(p.path, maxPackedRefs)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		p.err = err
		return
	}
	p.refs, p.err = parsePackedRefs(data, p.format)
	if p.err != nil {
		p.err = fmt.Errorf("%s: %w", p.path, p.err)
	}
}

// parsePackedRefs parses a packed-refs file: a header comment, then
// "<object> <name>" lines, each optionally followed by "^<object>", the
// object a tag peels to.
func parsePackedRefs(data string, f *Format) (map[string]ID, error) {
	refs := map[string]ID{}
	n := 0
	for line := range strings.Lines(data) {
		n++
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			continue
		case line[0] == '#':
			if n != 1 {
				return nil, fmt.Errorf("line %d: unexpected comment", n)
			}
			continue
		case line[0] == '^':
			if _, err := ParseID(f, line[1:]); err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
			continue
		}
		hex, name, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("line %d: invalid", n)
		}
		id, err := ParseID(f, hex)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if err := checkRefName(name); err != nil || !strings.HasPrefix(name, "refs/") {
			return nil, fmt.Errorf("line %d: invalid ref name %q", n, name)
		}
		refs[name] = id
	}
	return refs, nil
}
