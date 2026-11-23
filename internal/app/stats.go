// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
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
// moving quickly over a slow file system doesn't pile them up; a read
// that is no longer wanted is canceled, for a loader that heeds that.
type reader[K loader[V], V any] struct {
	app  *App
	mu   sync.Mutex
	want K
	done bool
	got  V
	// busy is set while a read runs; it reads want next if that changed.
	busy bool
	// stopped is set when the owning screen is discarded.
	stopped bool
	// cancel cancels the read running.
	cancel context.CancelFunc
}

// loader is a request of a reader, which load reads, until ctx is
// canceled.
type loader[V any] interface {
	comparable
	load(ctx context.Context) V
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

func (key statKey) load(context.Context) [2]statResult {
	var got [2]statResult
	for i, sk := range key.sides {
		if sk.root == nil {
			continue
		}
		got[i].st, got[i].err = sk.root.Stat(sk.rel)
	}
	return got
}

// changeReader reads the changes that last changed what the panels show,
// apart from statReader, as finding one may take long.
type changeReader = reader[changeKey, [2]changeResult]

// changeKey is a request: the two sides, the zero sideKey for one with
// no history.
type changeKey struct {
	sides [2]sideKey
}

type changeResult struct {
	ch  source.Change
	err error
}

func (key changeKey) load(ctx context.Context) [2]changeResult {
	var got [2]changeResult
	for i, sk := range key.sides {
		if h, ok := sk.root.(source.History); ok {
			got[i].ch, got[i].err = h.LastChange(ctx, sk.rel)
		}
	}
	return got
}

// historyKey returns the sideKey of the entry at rel in root to read the
// last change of, the zero sideKey if root has no history.
func historyKey(root source.Root, rel string) sideKey {
	if h, ok := root.(source.History); ok && h.HasHistory() {
		return sideKey{root, rel}
	}
	return sideKey{}
}

// changeSide returns what a panel shows of the last change read as res
// for the side sk: nothing without a history, and no commit while
// reading.
func changeSide(sk sideKey, res changeResult, done bool) *view.Change {
	switch {
	case sk.root == nil:
		return nil
	case !done:
		return &view.Change{}
	case res.err != nil:
		return &view.Change{Err: files.UnwrapPath(res.err)}
	}
	return &view.Change{
		ID: res.ch.ID, AuthorName: res.ch.AuthorName, AuthorEmail: res.ch.AuthorEmail,
		Authored: res.ch.Authored, Committed: res.ch.Committed,
	}
}

// read returns the result for key, and whether it is read yet. A key not
// asked for last is read in the background, and the app woken once done.
func (r *reader[K, V]) read(key K) (V, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		var zero V
		return zero, false
	}
	if key != r.want {
		var zero V
		r.want, r.done, r.got = key, false, zero
		if r.busy {
			r.cancel()
		} else {
			r.busy = true
			r.fetch(key)
		}
	}
	return r.got, r.done
}

// fetch starts reading key in the background, and then what was asked
// for meanwhile, until it has read the key asked for last. r.mu must be
// held.
func (r *reader[K, V]) fetch(key K) {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func() {
		got := key.load(ctx)
		r.mu.Lock()
		canceled := ctx.Err() != nil
		cancel()
		if r.stopped {
			r.busy = false
			r.mu.Unlock()
			return
		}
		if key != r.want || canceled {
			r.fetch(r.want)
			r.mu.Unlock()
			return
		}
		r.got, r.done, r.busy = got, true, false
		r.mu.Unlock()
		if r.app != nil {
			r.app.Wake()
		}
	}()
}

// panelPath returns the path a panel shows of the entry at rel in root:
// made absolute if in the file system, else as the root names it.
func panelPath(root source.Root, rel string) string {
	if d, ok := source.AsDisk(root); ok {
		return absPath(d.Path(rel))
	}
	return root.Path(rel)
}

// repoPath returns the path of the entry at rel in root, a revision, from
// the top of its repository, "" if that isn't known.
func repoPath(root source.Root, rel string) string {
	if w, ok := root.(source.RepoPaths); ok {
		if p, ok := w.RepoPath(rel); ok {
			return p
		}
	}
	return ""
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
// reading, and the owner's names as recorded with the entry, or else
// where names has them.
func statSide(res statResult, done bool, names *stat.Names) view.StatSide {
	sd := view.StatSide{Entries: -1}
	if !done {
		return sd
	}
	sd.Stat, sd.Err = res.st, files.UnwrapPath(res.err)
	if sd.Stat != nil {
		sd.User, sd.Group = sd.Stat.User, sd.Stat.Group
		if sd.User == "" {
			sd.User = names.User(sd.Stat.Uid)
		}
		if sd.Group == "" {
			sd.Group = names.Group(sd.Stat.Gid)
		}
	}
	return sd
}

// stop cancels the current read and prevents any further reads or results.
// Loaders that cannot be interrupted finish without waking the app.
func (r *reader[K, V]) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	if r.cancel != nil {
		r.cancel()
	}
	var zero V
	r.got, r.done = zero, false
}
