// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"container/list"
	"sync"
)

// baseCache keeps objects read from packs, the bases of deltas, so that
// the objects of a chain of deltas don't each make the chain again. It
// keeps at most limit bytes, dropping the least recently used beyond.
type baseCache struct {
	limit int64

	mu    sync.Mutex
	size  int64
	order *list.List
	byKey map[baseKey]*list.Element
}

type baseKey struct {
	p   *pack
	off int64
}

type baseEntry struct {
	key  baseKey
	typ  Type
	data []byte
}

// defaultCacheLimit is how much the cache keeps by default, as git's
// core.deltaBaseCacheLimit.
const defaultCacheLimit = 96 << 20

func (c *baseCache) get(p *pack, off int64) (Type, []byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[baseKey{p, off}]
	if !ok {
		return 0, nil, false
	}
	c.order.MoveToFront(el)
	e := el.Value.(*baseEntry)
	return e.typ, e.data, true
}

// put keeps data, which must not be changed after, unless it alone would
// take a quarter of the cache.
func (c *baseCache) put(p *pack, off int64, typ Type, data []byte) {
	n := int64(len(data))
	if n > c.limit/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byKey == nil {
		c.order, c.byKey = list.New(), map[baseKey]*list.Element{}
	}
	key := baseKey{p, off}
	if _, ok := c.byKey[key]; ok {
		return
	}
	c.byKey[key] = c.order.PushFront(&baseEntry{key, typ, data})
	c.size += n
	for c.size > c.limit {
		old := c.order.Back()
		e := old.Value.(*baseEntry)
		c.order.Remove(old)
		delete(c.byKey, e.key)
		c.size -= int64(len(e.data))
	}
}

// treeCache shares parsed directories between roots without retaining every
// tree visited in a session. A root itself retains the directories it uses.
// Costs include an estimate of map buckets, entries, and names.
type treeCache struct {
	limit int64
	mu    sync.Mutex
	size  int64
	order *list.List
	byID  map[ID]*list.Element
}

type treeCacheEntry struct {
	id   ID
	dir  *dir
	cost int64
}

const defaultTreeCacheLimit = 16 << 20

func treeCost(d *dir) int64 {
	cost := int64(128 + len(d.names)*128)
	for _, name := range d.names {
		cost += int64(len(name))
	}
	return cost
}

func (c *treeCache) get(id ID) (*dir, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byID[id]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*treeCacheEntry).dir, true
}

func (c *treeCache) put(id ID, d *dir) {
	cost := treeCost(d)
	if cost > c.limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID == nil {
		c.order, c.byID = list.New(), map[ID]*list.Element{}
	}
	if _, ok := c.byID[id]; ok {
		return
	}
	c.byID[id] = c.order.PushFront(&treeCacheEntry{id, d, cost})
	c.size += cost
	for c.size > c.limit {
		el := c.order.Back()
		e := el.Value.(*treeCacheEntry)
		c.order.Remove(el)
		delete(c.byID, e.id)
		c.size -= e.cost
	}
}
