// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"path/filepath"
	"sync"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/stat"
	"pxy.se/go/binate/internal/view"
)

// reader reads in the background what K's load returns, as a file system
// may take any time to answer. Only the last request's result is kept: one
// for an entry no longer selected is dropped. One read runs at a time, so
// moving quickly over a slow file system doesn't pile them up.
type reader[K loader[V], V any] struct {
	app  *App
	mu   sync.Mutex
	want K
	done bool
	got  V
	// busy is set while a read runs; it reads want next if that changed.
	busy bool
}

// loader is a request of a reader, which load reads.
type loader[V any] interface {
	comparable
	load() V
}

// statReader reads what the panels show.
type statReader = reader[statKey, [2]statResult]

// statKey is a request: the two sides, the zero sideKey for one not read,
// and a generation, raised when the entries may have changed, as after an
// edit, to read the same sides again.
type statKey struct {
	sides [2]sideKey
	gen   int
}

// sideKey is the entry at rel in root, or none if root is nil.
type sideKey struct {
	root source.Root
	rel  string
}

type statResult struct {
	st  *stat.Stat
	err error
}

func (key statKey) load() [2]statResult {
	var got [2]statResult
	for i, sk := range key.sides {
		if sk.root == nil {
			continue
		}
		got[i].st, got[i].err = sk.root.Stat(sk.rel)
	}
	return got
}

// read returns the result for key, and whether it is read yet. A key not
// asked for last is read in the background, and the app woken once done.
func (r *reader[K, V]) read(key K) (V, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if key != r.want {
		var zero V
		r.want, r.done, r.got = key, false, zero
		if !r.busy {
			r.busy = true
			go r.fetch(key)
		}
	}
	return r.got, r.done
}

// fetch reads key, and then what was asked for meanwhile, until it has
// read the key asked for last.
func (r *reader[K, V]) fetch(key K) {
	for {
		got := key.load()
		r.mu.Lock()
		if key != r.want {
			key = r.want
			r.mu.Unlock()
			continue
		}
		r.got, r.done, r.busy = got, true, false
		r.mu.Unlock()
		if r.app != nil {
			r.app.Wake()
		}
		return
	}
}

// absPath returns path made absolute, for a panel, or as given if it can't
// be.
func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// statSide returns what a panel shows of a side read as res: nothing while
// reading, and owner names where names has them.
func statSide(res statResult, done bool, names *stat.Names) view.StatSide {
	sd := view.StatSide{Entries: -1}
	if !done {
		return sd
	}
	sd.Stat, sd.Err = res.st, files.UnwrapPath(res.err)
	if sd.Stat != nil {
		sd.User, sd.Group = names.User(sd.Stat.Uid), names.Group(sd.Stat.Gid)
	}
	return sd
}
