// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/view"
)

// countingAction returns an action that reads the file at its path,
// reporting it to sideRun.Read, and counts its runs. also, if set, is done
// before the file is opened, and its result returned if not empty; open,
// if set, is done once it is opened.
func countingAction(cache bool, runs *int, also func(path string) view.ActionSide, open func(path string)) *action {
	return &action{
		name: "count", cache: cache,
		run: func(ctx context.Context, root source.Root, rel string, side *sideRun) view.ActionSide {
			path := root.Path(rel)
			*runs++
			if also != nil {
				if res := also(path); res != (view.ActionSide{}) {
					return res
				}
			}
			f, err := files.OpenRegular(path, false)
			if err != nil {
				return view.ActionSide{Err: err.Error()}
			}
			defer f.Close()
			side.Read(f)
			if open != nil {
				open(path)
			}
			data, err := io.ReadAll(f)
			if err != nil {
				return view.ActionSide{Err: err.Error()}
			}
			side.Read(f)
			return view.ActionSide{Value: string(data), Link: rel}
		},
	}
}

func TestResultCache(t *testing.T) {
	// Files written by the test changed just now: trust their times.
	defer func(w time.Duration) { racyWindow = w }(racyWindow)
	racyWindow = 0
	dir := writeTestFiles(t, map[string]string{"f": "one"})
	path := filepath.Join(dir, "f")
	var c resultCache
	runs := 0
	a := countingAction(true, &runs, nil, nil)
	ctx := context.Background()
	get := func() string {
		t.Helper()
		return c.run(ctx, a, source.Disk(dir), "f", func(float64) {}).Value
	}
	// The second run is kept from the first.
	first, second := get(), get()
	if first != "one" || second != "one" || runs != 1 {
		t.Errorf("unchanged file: %q then %q, run %d times", first, second, runs)
	}
	// A change, of the size here, runs it again.
	if err := os.WriteFile(path, []byte("three"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != "three" || runs != 2 {
		t.Errorf("changed file: %q, run %d times", got, runs)
	}
	// As does another file in its place, at the same size and times.
	other := filepath.Join(dir, "g")
	if err := os.WriteFile(other, []byte("THREE"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != "THREE" || runs != 3 {
		t.Errorf("replaced file: %q, run %d times", got, runs)
	}
	// Each action has its own results.
	runs2 := 0
	b := countingAction(true, &runs2, nil, nil)
	b.name = "other"
	c.run(ctx, b, source.Disk(dir), "f", func(float64) {})
	if runs2 != 1 {
		t.Errorf("another action's result was used")
	}
}

func TestResultCacheKeepsOnlyTrustedResults(t *testing.T) {
	defer func(w time.Duration) { racyWindow = w }(racyWindow)
	racyWindow = 0
	dir := writeTestFiles(t, map[string]string{"f": "one"})
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		cache bool
		also  func(path string) view.ActionSide
		open  func(path string)
		ctx   func() context.Context
		racy  time.Duration
	}{
		{name: "not cached by the action"},
		{name: "failed", cache: true, also: func(string) view.ActionSide { return view.ActionSide{Err: "no"} }},
		{name: "changed while read", cache: true, open: func(p string) {
			_ = os.WriteFile(p, []byte(fmt.Sprint(time.Now().UnixNano())), 0o644)
		}},
		{name: "cancelled", cache: true, ctx: func() context.Context {
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			return ctx
		}},
		{name: "changed just before", cache: true, racy: time.Hour},
	} {
		racyWindow = c.racy
		var rc resultCache
		runs := 0
		a := countingAction(c.cache, &runs, c.also, c.open)
		runCtx := ctx
		if c.ctx != nil {
			runCtx = c.ctx()
		}
		rc.run(runCtx, a, source.Disk(dir), "f", func(float64) {})
		rc.run(runCtx, a, source.Disk(dir), "f", func(float64) {})
		if runs != 2 {
			t.Errorf("%s: run %d times, want 2", c.name, runs)
		}
	}
}

// Another file put in the path's place while read, and moved away again
// before the read ends, leaves the path as it was, but its result is not
// the path's to keep. Swapping the file itself changes its ctime, so the
// directory holding it is swapped, which leaves the file untouched.
func TestResultCacheSwappedWhileRead(t *testing.T) {
	defer func(w time.Duration) { racyWindow = w }(racyWindow)
	racyWindow = 0
	dir := writeTestFiles(t, map[string]string{"d/f": "one", "e/f": "two"})
	d, e, aside := filepath.Join(dir, "d"), filepath.Join(dir, "e"), filepath.Join(dir, "x")
	rename := func(from, to string) {
		if err := os.Rename(from, to); err != nil {
			t.Fatal(err)
		}
	}
	var c resultCache
	runs := 0
	swap := true
	a := countingAction(true, &runs, func(string) view.ActionSide {
		// e takes d's place, so e/f is opened instead of d/f.
		if swap {
			rename(d, aside)
			rename(e, d)
		}
		return view.ActionSide{}
	}, func(string) {
		// Once it is open, d is put back.
		if swap {
			rename(d, e)
			rename(aside, d)
			swap = false
		}
	})
	get := func() string {
		t.Helper()
		return c.run(context.Background(), a, source.Disk(dir), "d/f", func(float64) {}).Value
	}
	if got := get(); got != "two" {
		t.Fatalf("swapped read %q, want two", got)
	}
	if got := get(); got != "one" || runs != 2 {
		t.Errorf("after the swap: %q, run %d times", got, runs)
	}
	// Read unchanged, it is kept.
	if got := get(); got != "one" || runs != 2 {
		t.Errorf("unchanged: %q, run %d times", got, runs)
	}
}

// otherRoot is a side not in the file system, as far as the cache can
// tell: its files have no version to check.
type otherRoot struct{ source.Disk }

// A result on a side not in the file system is not cached.
func TestResultCacheOnlyOnDisk(t *testing.T) {
	defer func(w time.Duration) { racyWindow = w }(racyWindow)
	racyWindow = 0
	dir := writeTestFiles(t, map[string]string{"f": "one"})
	runs := 0
	a := countingAction(true, &runs, nil, nil)
	var c resultCache
	for range 2 {
		if got := c.run(context.Background(), a, otherRoot{source.Disk(dir)}, "f", func(float64) {}).Value; got != "one" {
			t.Errorf("read %q", got)
		}
	}
	if runs != 2 {
		t.Errorf("run %d times, want 2", runs)
	}
}

// An action that doesn't report the file it read is not cached, as what
// it read can't be checked.
func TestResultCacheNeedsReadReported(t *testing.T) {
	defer func(w time.Duration) { racyWindow = w }(racyWindow)
	racyWindow = 0
	dir := writeTestFiles(t, map[string]string{"f": "one"})
	runs := 0
	a := &action{name: "silent", cache: true, run: func(context.Context, source.Root, string, *sideRun) view.ActionSide {
		runs++
		return view.ActionSide{Value: "x"}
	}}
	var c resultCache
	c.run(context.Background(), a, source.Disk(dir), "f", func(float64) {})
	c.run(context.Background(), a, source.Disk(dir), "f", func(float64) {})
	if runs != 2 {
		t.Errorf("run %d times, want 2", runs)
	}
}

func TestResultCacheLimit(t *testing.T) {
	var c resultCache
	key := func(i int) cacheKey { return cacheKey{"a", fmt.Sprint(i)} }
	for i := range resultCacheLimit {
		c.put(cacheEntry{key: key(i)})
	}
	// Using the oldest keeps it; the next oldest goes instead.
	if _, ok := c.get(key(0), fileVersion{}); !ok {
		t.Fatal("result 0 not kept")
	}
	c.put(cacheEntry{key: key(resultCacheLimit)})
	if _, ok := c.get(key(1), fileVersion{}); ok {
		t.Error("the least recently used result was kept")
	}
	if _, ok := c.get(key(0), fileVersion{}); !ok {
		t.Error("a recently used result was dropped")
	}
	if c.order.Len() != resultCacheLimit || len(c.byKey) != resultCacheLimit {
		t.Errorf("%d results kept, %d found", c.order.Len(), len(c.byKey))
	}
}
