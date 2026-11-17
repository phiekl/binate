// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CommitInfo is what a commit records of itself.
type CommitInfo struct {
	Tree    ID
	Parents []ID
	// AuthorName and AuthorEmail are who made the change.
	AuthorName, AuthorEmail string
	// Author is when the change was made, Committer when it was committed,
	// each in the zone recorded.
	Author, Committer time.Time
	// Message is the message, after the header.
	Message string
}

// Subject returns the first paragraph of the message, joined into a line,
// as git's %s.
func (c *CommitInfo) Subject() string {
	var lines []string
	for _, line := range strings.Split(strings.TrimLeft(c.Message, "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, " ")
}

// ReadCommit reads the commit id.
func (r *Repo) ReadCommit(id ID) (*CommitInfo, error) {
	typ, data, err := r.Read(id, maxTree)
	if err != nil {
		return nil, err
	}
	if typ != Commit {
		return nil, fmt.Errorf("%s is a %s, not a commit", id, typ)
	}
	c, err := parseCommit(r.format, data)
	if err != nil {
		return nil, corrupt(id, "%v", err)
	}
	if r.shallow[id] {
		c.Parents = nil
	}
	return c, nil
}

// parseCommit parses a commit: its tree, parents, author, the times of its
// author and committer lines, and its message.
func parseCommit(f *Format, data []byte) (*CommitInfo, error) {
	var c CommitInfo
	var hasTree bool
	for len(data) > 0 {
		line, rest, _ := bytes.Cut(data, []byte{'\n'})
		data = rest
		if len(line) == 0 {
			c.Message = string(data)
			break
		}
		k, v, _ := strings.Cut(string(line), " ")
		var err error
		switch k {
		case "tree":
			if hasTree {
				return nil, errors.New("commit with two trees")
			}
			c.Tree, err = ParseID(f, v)
			hasTree = true
		case "parent":
			var p ID
			if p, err = ParseID(f, v); err == nil {
				c.Parents = append(c.Parents, p)
			}
		case "author":
			c.AuthorName, c.AuthorEmail, c.Author, err = parseSignature(v)
		case "committer":
			_, _, c.Committer, err = parseSignature(v)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
	}
	if !hasTree {
		return nil, errors.New("commit without a tree")
	}
	return &c, nil
}

// parseSignature parses an author or committer line, "Name <email>
// 1700000000 +0100", as git's split_ident_line does: the name before the
// first "<", without spaces around it, the email up to the next ">", and
// the time after the last ">", in its zone. A time git couldn't parse
// either is zero, as git shows a broken one as the epoch.
func parseSignature(sig string) (name, email string, t time.Time, err error) {
	lt := strings.IndexByte(sig, '<')
	gt := -1
	if lt >= 0 {
		gt = strings.IndexByte(sig[lt:], '>')
	}
	if gt < 0 {
		return "", "", time.Time{}, errors.New("without an email")
	}
	name = strings.TrimSpace(sig[:lt])
	email = sig[lt+1 : lt+gt]
	t, err = parseSignatureTime(sig[strings.LastIndexByte(sig, '>')+1:])
	return name, email, t, err
}

// parseSignatureTime parses the time of a signature, " 1700000000 +0100".
func parseSignatureTime(date string) (time.Time, error) {
	fields := strings.Fields(date)
	if len(fields) == 0 {
		return time.Time{}, nil
	}
	secs, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || secs < 0 {
		return time.Time{}, nil
	}
	t := time.Unix(secs, 0)
	if len(fields) < 2 {
		return t.UTC(), nil
	}
	zone := fields[1]
	if len(zone) != 5 || zone[0] != '+' && zone[0] != '-' {
		return t.UTC(), nil
	}
	hhmm, err := strconv.Atoi(zone[1:])
	if err != nil {
		return t.UTC(), nil
	}
	offset := (hhmm/100*60 + hhmm%100) * 60
	if zone[0] == '-' {
		offset = -offset
	}
	return t.In(time.FixedZone(zone, offset)), nil
}

// maxHistory is how many commits LastChange walks before giving up, which
// takes a second or so in a large repository.
const maxHistory = 100_000

// A short walk costs less than loading and checking the commit graph.
const historyGraphAfter = 8

// ErrHistoryTooLong is the error of a path not changed within the last
// maxHistory commits walked.
var ErrHistoryTooLong = fmt.Errorf("not changed in the last %d commits", maxHistory)

// LastChange returns the commit that last changed the entry at path, ""
// for the root, in the tree of the commit start, as "git log -1 -- path"
// finds it: from start, while a parent has the entry the same, in mode and
// object, that parent is followed, the first such of a merge; the commit
// with no such parent is the one. It is canceled with ctx.
func (r *Repo) LastChange(ctx context.Context, start ID, path string) (ID, *CommitInfo, error) {
	var names []string
	if path != "" {
		names = strings.Split(path, "/")
	}
	id := start
	c, err := r.ReadCommit(id)
	if err != nil {
		return ID{}, nil, err
	}
	// The entries on the way to path in c's tree, from the root.
	way, err := r.walkPath(c.Tree, names, nil)
	if err != nil {
		return ID{}, nil, err
	}
	if len(way) <= len(names) {
		return ID{}, nil, fmt.Errorf("%s does not exist in %s", path, id)
	}
	var graph commitGraph
	for n := 0; ; n++ {
		if n%256 == 0 {
			if err := ctx.Err(); err != nil {
				return ID{}, nil, err
			}
		}
		if n == maxHistory {
			return ID{}, nil, ErrHistoryTooLong
		}
		if n == historyGraphAfter {
			graph, err = r.historyGraph(ctx)
			if err != nil {
				return ID{}, nil, err
			}
		}
		if len(graph) != 0 {
			var layer *graphLayer
			var row uint32
			fault := safely(func() {
				layer, row = graph.find(id)
				if layer != nil && layer.tree(row) != c.Tree {
					layer = nil
				}
			})
			var last ID
			var walkErr error
			if fault == nil && layer != nil {
				last, walkErr = r.lastChangeInGraph(ctx, graph, layer, row, n, path, names, way)
			}
			if fault != nil || errors.Is(walkErr, errGraphRead) {
				graph = nil // Ignore a broken optional index for this walk.
			} else if walkErr != nil {
				return ID{}, nil, walkErr
			} else if !last.IsZero() {
				if last == id {
					return id, c, nil
				}
				info, err := r.ReadCommit(last)
				if err != nil {
					return ID{}, nil, err
				}
				return last, info, nil
			}
		}
		found := false
		for _, p := range c.Parents {
			pc, err := r.ReadCommit(p)
			if err != nil {
				return ID{}, nil, err
			}
			pway, err := r.walkPath(pc.Tree, names, way)
			if err != nil {
				return ID{}, nil, err
			}
			if len(pway) == len(way) && pway[len(pway)-1] == way[len(way)-1] {
				id, c, way, found = p, pc, pway, true
				break
			}
		}
		if !found {
			return id, c, nil
		}
	}
}

var errGraphRead = errors.New("cannot read the commit graph")

func (r *Repo) lastChangeInGraph(ctx context.Context, graph commitGraph, layer *graphLayer, row uint32, n int, path string, names []string, way []TreeEntry) (ID, error) {
	entry := way[len(way)-1]
	var keys bloomKeys
	// Git's filters can omit empty subtree changes and ignored submodule
	// changes. Compare the entries directly for both modes.
	if entry.Mode != ModeTree && entry.Mode != ModeSubmodule {
		keys = newBloomKeys(path)
	}
	for ; n < maxHistory; n++ {
		if n%256 == 0 {
			if err := ctx.Err(); err != nil {
				return ID{}, err
			}
		}
		found := false
		first := true
		for pos := range layer.parents(row) {
			if pos >= layer.end || pos == layer.start+row {
				return ID{}, errGraphRead
			}
			parent := layer
			if pos < layer.start {
				for i := len(graph) - 1; i >= 0; i-- {
					if pos >= graph[i].start {
						parent = graph[i]
						break
					}
				}
			}
			p := pos - parent.start
			var unchanged bool
			var tree ID
			if err := safely(func() {
				unchanged = first && layer.bloom.excludes(row, keys)
				if !unchanged {
					tree = parent.tree(p)
				}
			}); err != nil {
				return ID{}, errGraphRead
			}
			if unchanged {
				// Keep the last inspected tree trail. Its final entry is
				// still equal to the one in the parent.
				layer, row, found = parent, p, true
				break
			}
			first = false
			pway, err := r.walkPath(tree, names, way)
			if err != nil {
				return ID{}, err
			}
			if len(pway) == len(way) && pway[len(pway)-1] == entry {
				layer, row, way, found = parent, p, pway, true
				break
			}
		}
		if !found {
			var id ID
			if err := safely(func() { id = layer.id(row) }); err != nil {
				return ID{}, errGraphRead
			}
			return id, nil
		}
	}
	return ID{}, ErrHistoryTooLong
}

// walkPath returns the entries on the way to the path of names from tree:
// the tree itself, then one for each name, ending early where one is
// missing. Where the way so far is that of like, the tree there is the
// same, and the rest is like's.
func (r *Repo) walkPath(tree ID, names []string, like []TreeEntry) ([]TreeEntry, error) {
	way := make([]TreeEntry, 0, len(names)+1)
	e := TreeEntry{Mode: ModeTree, ID: tree}
	for i := 0; ; i++ {
		if i < len(like) && like[i] == e {
			return append(way, like[i:]...), nil
		}
		way = append(way, e)
		if i == len(names) {
			return way, nil
		}
		if e.Mode != ModeTree {
			return way, nil
		}
		next, ok, err := r.treeEntry(e.ID, names[i])
		if err != nil || !ok {
			return way, err
		}
		e = next
	}
}

// treeEntry returns the entry name of the tree id, read without parsing
// the rest.
func (r *Repo) treeEntry(id ID, name string) (TreeEntry, bool, error) {
	typ, data, err := r.Read(id, maxTree)
	if err != nil {
		return TreeEntry{}, false, err
	}
	if typ != Tree {
		return TreeEntry{}, false, fmt.Errorf("%s is a %s, not a tree", id, typ)
	}
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		nul := bytes.IndexByte(data, 0)
		if sp < 0 || nul < sp || len(data) < nul+1+r.format.size {
			return TreeEntry{}, false, corrupt(id, "tree entry cut short")
		}
		if string(data[sp+1:nul]) == name {
			mode, err := parseMode(data[:sp])
			if err != nil {
				return TreeEntry{}, false, corrupt(id, "%v", err)
			}
			e := TreeEntry{Mode: mode, Name: name}
			e.ID, _ = IDFromBytes(r.format, data[nul+1:nul+1+r.format.size])
			return e, true, nil
		}
		data = data[nul+1+r.format.size:]
	}
	return TreeEntry{}, false, nil
}
