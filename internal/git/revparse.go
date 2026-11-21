// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Target is what a revision names to compare: a tree, or a file's blob.
type Target struct {
	ID ID
	// Mode is ModeTree, ModeFile or ModeExecutable.
	Mode Mode
	// Commit, unless zero, is the commit the revision names, of whose tree
	// the target is the entry at Path, "" for the tree itself: where the
	// target's history starts. A tree or blob named otherwise has none.
	Commit ID
	Path   string
}

// IsTree reports whether t is a tree.
func (t Target) IsTree() bool { return t.Mode == ModeTree }

// errUnsupported is the error of revision syntax git has and binate not.
var errUnsupported = errors.New("not supported")

// dwimRules are where a ref name given short is looked for, in order, as
// git's ref_rev_parse_rules.
var dwimRules = []string{"%s", "refs/%s", "refs/tags/%s", "refs/heads/%s", "refs/remotes/%s", "refs/remotes/%s/HEAD"}

// Resolve returns what the revision rev names, of git's syntax: a ref
// name, short or full, or an object name, maybe abbreviated, or "@" for
// HEAD; then any of "^", "^N", "~", "~N", "^{}" and "^{type}"; then maybe
// ":path", relative to the directory searched from if starting with "./"
// or "../". A commit or tag names its tree.
func (r *Repository) Resolve(rev string) (Target, error) {
	t, err := r.resolve(rev)
	if err != nil {
		return Target{}, fmt.Errorf("revision %s: %w", rev, err)
	}
	return t, nil
}

func (r *Repository) resolve(rev string) (Target, error) {
	spec, file, hasPath := strings.Cut(rev, ":")
	switch {
	case hasPath && spec == "" && strings.HasPrefix(file, "/"):
		return Target{}, fmt.Errorf("searching commit messages with :/ is %w", errUnsupported)
	case hasPath && spec == "":
		return Target{}, fmt.Errorf("paths in the index, as :path, are %w", errUnsupported)
	case strings.Contains(spec, "@{"):
		return Target{}, fmt.Errorf("reflogs and upstreams, as @{...}, are %w", errUnsupported)
	}
	end := strings.IndexAny(spec, "^~")
	if end < 0 {
		end = len(spec)
	}
	base, suffixes := spec[:end], spec[end:]
	if base == "" {
		return Target{}, errors.New("no revision before ^ or ~")
	}
	if base == "@" {
		base = "HEAD"
	}
	id, err := r.object(base)
	if err != nil {
		return Target{}, err
	}
	if id, err = r.applySuffixes(id, suffixes); err != nil {
		return Target{}, err
	}
	if id, err = r.peelTags(id); err != nil {
		return Target{}, err
	}
	typ, _, err := r.Info(id)
	if err != nil {
		return Target{}, err
	}
	var commit ID
	if typ == Commit {
		commit = id
	}
	if hasPath {
		t, err := r.lookupPath(id, file)
		if err != nil {
			return Target{}, err
		}
		t.Commit = commit
		return t, nil
	}
	if typ == Blob {
		return Target{ID: id, Mode: ModeFile}, nil
	}
	tree, err := r.peelTo(id, Tree)
	if err != nil {
		return Target{}, err
	}
	return Target{ID: tree, Mode: ModeTree, Commit: commit}, nil
}

// object returns the object name names: a full object name, a ref, or an
// abbreviated object name, tried in that order, as git does.
func (r *Repository) object(name string) (ID, error) {
	if len(name) == 2*r.format.size && isHex(name) {
		return ParseID(r.format, name)
	}
	for _, rule := range dwimRules {
		ref := fmt.Sprintf(rule, name)
		if checkRefName(ref) != nil {
			continue
		}
		id, err := r.resolveRef(ref)
		if errors.Is(err, errNoRef) {
			continue
		}
		return id, err
	}
	id, err := r.expand(name)
	if errors.Is(err, errNoRef) {
		return ID{}, errors.New("unknown revision")
	}
	return id, err
}

// applySuffixes follows the suffixes of a revision from id.
func (r *Repository) applySuffixes(id ID, s string) (ID, error) {
	for s != "" {
		op := s[0]
		s = s[1:]
		if op == '^' && strings.HasPrefix(s, "{") {
			end := strings.IndexByte(s, '}')
			if end < 0 {
				return ID{}, errors.New("^{ not ended")
			}
			what := s[1:end]
			s = s[end+1:]
			var err error
			if id, err = r.peelBraces(id, what); err != nil {
				return ID{}, err
			}
			continue
		}
		digits := len(s) - len(strings.TrimLeft(s, "0123456789"))
		n := 1
		if digits > 0 {
			var err error
			if n, err = strconv.Atoi(s[:digits]); err != nil || n > 1<<20 {
				return ID{}, fmt.Errorf("invalid number %s", s[:digits])
			}
			s = s[digits:]
		}
		if op != '^' && op != '~' {
			return ID{}, fmt.Errorf("unexpected %q; ranges and other suffixes are %w", op, errUnsupported)
		}
		commit, err := r.peelTo(id, Commit)
		if err != nil {
			return ID{}, err
		}
		if op == '^' {
			if id, err = r.parent(commit, n); err != nil {
				return ID{}, err
			}
			continue
		}
		id = commit
		for range n {
			if id, err = r.parent(id, 1); err != nil {
				return ID{}, err
			}
		}
	}
	return id, nil
}

// peelBraces follows "^{what}" from id.
func (r *Repository) peelBraces(id ID, what string) (ID, error) {
	switch what {
	case "":
		return r.peelTags(id)
	case "commit":
		return r.peelTo(id, Commit)
	case "tree":
		return r.peelTo(id, Tree)
	case "blob":
		return r.peelTo(id, Blob)
	case "tag":
		return r.peelTo(id, Tag)
	case "object":
		_, _, err := r.Info(id)
		return id, err
	}
	if strings.HasPrefix(what, "/") {
		return ID{}, fmt.Errorf("searching commit messages with ^{/...} is %w", errUnsupported)
	}
	return ID{}, fmt.Errorf("unknown ^{%s}", what)
}

// parent returns the n-th parent of the commit id, or id itself for 0.
func (r *Repository) parent(id ID, n int) (ID, error) {
	if n == 0 {
		return id, nil
	}
	c, err := r.ReadCommit(id)
	if err != nil {
		return ID{}, err
	}
	if n <= len(c.Parents) {
		return c.Parents[n-1], nil
	}
	return ID{}, fmt.Errorf("commit %s has no parent %d", id, n)
}

// headerLines yields a commit's or tag's header lines.
func headerLines(data []byte) func(func(string) bool) {
	return func(yield func(string) bool) {
		for line := range strings.Lines(string(data)) {
			line = strings.TrimSuffix(line, "\n")
			if line == "" || !yield(line) {
				return
			}
		}
	}
}

// peelTags follows tags from id to what is not a tag.
func (r *Repo) peelTags(id ID) (ID, error) {
	for range maxPeel {
		typ, data, err := r.readSmallObject(id)
		if err != nil {
			return ID{}, err
		}
		if typ != Tag {
			return id, nil
		}
		if id, err = r.tagTarget(id, data); err != nil {
			return ID{}, err
		}
	}
	return ID{}, fmt.Errorf("%s: more than %d tags in a row", id, maxPeel)
}

// peelTo follows tags, and a commit to its tree, from id to an object of
// type want.
func (r *Repo) peelTo(id ID, want Type) (ID, error) {
	start := id
	for range maxPeel {
		typ, data, err := r.readSmallObject(id)
		if err != nil {
			return ID{}, err
		}
		switch {
		case typ == want:
			return id, nil
		case typ == Tag:
			if id, err = r.tagTarget(id, data); err != nil {
				return ID{}, err
			}
		case typ == Commit && want == Tree:
			tree, ok := header(data, "tree")
			if !ok {
				return ID{}, corrupt(id, "commit without a tree")
			}
			t, err := ParseID(r.format, tree)
			if err != nil {
				return ID{}, corrupt(id, "%v", err)
			}
			return t, nil
		default:
			return ID{}, fmt.Errorf("%s is a %s, not a %s", start, typ, want)
		}
	}
	return ID{}, fmt.Errorf("%s: more than %d tags in a row", start, maxPeel)
}

// readSmallObject returns an object's type, and its content unless a blob,
// which may be large and isn't needed.
func (r *Repo) readSmallObject(id ID) (Type, []byte, error) {
	p, off, err := r.find(id)
	if err != nil {
		return 0, nil, err
	}
	var obj Object
	if p != nil {
		e, entryErr := p.entry(off)
		if entryErr != nil {
			return 0, nil, corrupt(id, "%v", entryErr)
		}
		if e.typ == ofsDelta || e.typ == refDelta {
			// Opening a delta reconstructs its entire content. Find its
			// type through the base chain first, so blobs stay unread.
			typ, _, err := p.info(off, id, 0)
			if err != nil || typ == Blob {
				return typ, nil, err
			}
		}
		obj, err = p.open(off, id)
	} else {
		obj, err = r.openLoose(id)
	}
	if err != nil {
		return 0, nil, err
	}
	if obj.Type == Blob {
		_ = obj.Close()
		return Blob, nil, nil
	}
	return readObject(id, obj, maxTree)
}

// tagTarget returns the object the tag id, of content data, points to.
func (r *Repo) tagTarget(id ID, data []byte) (ID, error) {
	obj, ok := header(data, "object")
	if !ok {
		return ID{}, corrupt(id, "tag without an object")
	}
	next, err := ParseID(r.format, obj)
	if err != nil {
		return ID{}, corrupt(id, "%v", err)
	}
	return next, nil
}

// lookupPath returns the entry at file in the tree of id, with its path in
// the tree.
func (r *Repository) lookupPath(id ID, file string) (Target, error) {
	tree, err := r.peelTo(id, Tree)
	if err != nil {
		return Target{}, err
	}
	// As in git, a path ending in a slash names a tree, as does a relative
	// one ending in . or .., which git leaves a slash of when it cleans it.
	given := file
	wantTree := strings.HasSuffix(file, "/")
	if strings.HasPrefix(file, "./") || strings.HasPrefix(file, "../") {
		if last := path.Base(file); last == "." || last == ".." {
			wantTree = true
		}
		if r.loc.WorkTree == "" {
			return Target{}, fmt.Errorf("path %s relative to a work tree, in a bare repository", file)
		}
		file = path.Join(r.loc.Prefix, file)
		if file == ".." || strings.HasPrefix(file, "../") {
			return Target{}, fmt.Errorf("path %s outside the repository", file)
		}
		if file == "." {
			file = ""
		}
	}
	// Otherwise, as git, the path is taken as it is, but for a slash at
	// its end.
	file = strings.TrimSuffix(file, "/")
	t := Target{ID: tree, Mode: ModeTree, Path: file}
	if file == "" {
		return t, nil
	}
	for name := range strings.SplitSeq(file, "/") {
		if !t.IsTree() {
			return Target{}, fmt.Errorf("path %s: not a directory on the way", file)
		}
		entries, err := r.ReadTree(t.ID)
		if err != nil {
			return Target{}, err
		}
		found := false
		for _, e := range entries {
			if e.Name == name {
				t, found = Target{ID: e.ID, Mode: e.Mode, Path: file}, true
				break
			}
		}
		if !found {
			return Target{}, fmt.Errorf("path %s does not exist", file)
		}
	}
	if wantTree && !t.IsTree() {
		return Target{}, fmt.Errorf("path %s names a directory, but %s is not one", given, file)
	}
	switch t.Mode {
	case ModeSymlink:
		return Target{}, fmt.Errorf("path %s is a symlink, which is not followed", file)
	case ModeSubmodule:
		return Target{}, fmt.Errorf("path %s is a submodule, which is not read", file)
	}
	return t, nil
}
