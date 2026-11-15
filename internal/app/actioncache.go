// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"container/list"
	"context"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/view"
)

// resultCacheLimit is how many results the cache keeps, dropping the
// least recently used beyond it.
const resultCacheLimit = 10000

// racyWindow is how recently a file may have changed before its result is
// not cached. File systems record times at a clock tick's resolution, so a
// file rewritten within the tick it was last changed in, at the same
// size, keeps its times: as git does, results of files that changed just
// before they were read are not trusted later.
var racyWindow = 2 * time.Second

// resultCache keeps the results of actions with cache set, by action and
// path, while the file is unchanged, so running one again on it shows the
// result at once. It is safe for concurrent use.
type resultCache struct {
	mu sync.Mutex
	// order holds the entries, the most recently used first; byKey finds
	// them.
	order *list.List
	byKey map[cacheKey]*list.Element
}

type cacheKey struct {
	action, path string
}

type cacheEntry struct {
	key  cacheKey
	file fileVersion
	side view.ActionSide
}

// fileVersion tells whether a file changed: its device and inode, which
// change if another file takes its place, and its size, mtime and ctime,
// which a write changes. Unlike the mtime, the ctime can't be set back.
type fileVersion struct {
	dev, ino     uint64
	size         int64
	mtime, ctime unix.Timespec
}

// versionOf returns the version of the file at path, without following a
// symlink.
func versionOf(path string) (fileVersion, bool) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return fileVersion{}, false
	}
	return versionOfStat(&st), true
}

// versionOfFile returns the version of the open file f, or the zero
// version, which matches no file, if it can't be read.
func versionOfFile(f *os.File) fileVersion {
	conn, err := f.SyscallConn()
	if err != nil {
		return fileVersion{}
	}
	var st unix.Stat_t
	if conn.Control(func(fd uintptr) { err = unix.Fstat(int(fd), &st) }) != nil || err != nil {
		return fileVersion{}
	}
	return versionOfStat(&st)
}

func versionOfStat(st *unix.Stat_t) fileVersion {
	return fileVersion{
		dev: uint64(st.Dev), ino: st.Ino, size: st.Size,
		mtime: st.Mtim, ctime: st.Ctim,
	}
}

// run returns a's result on the side at path, rel from its root: the one
// kept from before if the file is unchanged since, or else a new one, kept
// if a caches its results. Only a finished result is kept, of a file that
// didn't change shortly before it was read, and was read unchanged: the
// file a reports reading must be the one found at path before, at the same
// version both once opened and once read. While it is open its inode can't
// be reused, so neither another file put in its place, as by swapping a
// directory above it, nor a change while it is read can be mistaken for
// it, even if the path is as it was once the read ends.
func (c *resultCache) run(ctx context.Context, a *action, path, rel string, progress func(float64)) view.ActionSide {
	side := &sideRun{progress: progress}
	if !a.cache {
		return a.run(ctx, path, rel, side)
	}
	key := cacheKey{a.name, path}
	start := time.Now()
	before, ok := versionOf(path)
	if ok {
		if res, hit := c.get(key, before); hit {
			return res
		}
	}
	var read []fileVersion
	side.read = func(f *os.File) { read = append(read, versionOfFile(f)) }
	res := a.run(ctx, path, rel, side)
	if !ok || res.Err != "" || ctx.Err() != nil || racy(before, start) {
		return res
	}
	if len(read) == 2 && read[0] == before && read[1] == before {
		c.put(cacheEntry{key, before, res})
	}
	return res
}

// racy reports whether v changed too shortly before start, or after it,
// for its times to tell a later change apart.
func racy(v fileVersion, start time.Time) bool {
	changed := time.Unix(v.ctime.Unix())
	if m := time.Unix(v.mtime.Unix()); m.After(changed) {
		changed = m
	}
	return !changed.Before(start.Add(-racyWindow))
}

// get returns the result kept for key, if the file is still at version v.
func (c *resultCache) get(key cacheKey, v fileVersion) (view.ActionSide, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[key]
	if !ok {
		return view.ActionSide{}, false
	}
	e := el.Value.(*cacheEntry)
	if e.file != v {
		c.order.Remove(el)
		delete(c.byKey, key)
		return view.ActionSide{}, false
	}
	c.order.MoveToFront(el)
	return e.side, true
}

// put keeps e, replacing what was kept for its key, and drops the least
// recently used results beyond the limit.
func (c *resultCache) put(e cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byKey == nil {
		c.order, c.byKey = list.New(), map[cacheKey]*list.Element{}
	}
	if el, ok := c.byKey[e.key]; ok {
		c.order.Remove(el)
	}
	c.byKey[e.key] = c.order.PushFront(&e)
	for c.order.Len() > resultCacheLimit {
		old := c.order.Back()
		c.order.Remove(old)
		delete(c.byKey, old.Value.(*cacheEntry).key)
	}
}
