// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import "testing"

func TestTreeCacheEviction(t *testing.T) {
	d := &dir{names: []string{"file"}, byName: map[string]TreeEntry{"file": {Name: "file"}}}
	c := treeCache{limit: 2 * treeCost(d)}
	a, b, next := ID{n: 1, raw: [32]byte{1}}, ID{n: 1, raw: [32]byte{2}}, ID{n: 1, raw: [32]byte{3}}
	c.put(a, d)
	c.put(b, d)
	if got, ok := c.get(a); !ok || got != d {
		t.Fatal("missing cached directory")
	}
	c.put(next, d)
	if _, ok := c.get(b); ok {
		t.Fatal("least recently used tree retained")
	}
	if _, ok := c.get(a); !ok {
		t.Fatal("recently used tree evicted")
	}
	large := &dir{names: []string{"a", "b", "c", "d"}}
	c.put(b, large)
	if _, ok := c.get(b); ok {
		t.Fatal("oversized tree cached")
	}
	if c.size > c.limit {
		t.Fatal("cache exceeds its limit")
	}
	// Eviction does not invalidate directories held by existing roots.
	if d.names[0] != "file" || d.byName["file"].Name != "file" {
		t.Fatal("eviction changed directory")
	}
}
