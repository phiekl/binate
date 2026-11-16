// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pxy.se/go/binate/internal/source"
)

// waitRead asks r for key until it is read.
func waitRead(t *testing.T, r *statReader, key statKey) [2]statResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, done := r.read(key); done {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%v not read", key)
	return [2]statResult{}
}

func TestStatReader(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("four"), 0o644); err != nil {
		t.Fatal(err)
	}
	var r statReader
	key := statKey{sides: [2]sideKey{{source.Disk(f), ""}, {}}}
	got := waitRead(t, &r, key)
	if got[0].st == nil || got[0].st.Size != 4 || got[1].st != nil || got[1].err != nil {
		t.Errorf("read %+v", got)
	}

	// The same paths are read again in a new generation.
	if err := os.WriteFile(f, []byte("longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.read(key); got[0].st.Size != 4 {
		t.Errorf("read again without a new generation: %+v", got[0].st)
	}
	key.gen++
	if got := waitRead(t, &r, key); got[0].st.Size != 6 {
		t.Errorf("read in a new generation %+v", got[0].st)
	}

	// A result for an earlier request is dropped.
	other := statKey{sides: [2]sideKey{{}, {source.Disk(filepath.Join(dir, "missing")), ""}}}
	r.read(other)
	r.mu.Lock()
	r.fetch(key)
	r.mu.Unlock()
	if got, done := r.read(other); done && got[0].st != nil {
		t.Errorf("an earlier request's result kept: %+v", got)
	}
	if got := waitRead(t, &r, other); got[1].err == nil {
		t.Errorf("missing file read %+v", got)
	}
}

// blockingKey is a request that blocks until canceled, unless quick.
type blockingKey struct {
	quick    bool
	canceled chan bool
}

func (k blockingKey) load(ctx context.Context) bool {
	if k.quick {
		return true
	}
	<-ctx.Done()
	k.canceled <- true
	return false
}

func TestReaderCancels(t *testing.T) {
	var r reader[blockingKey, bool]
	slow := blockingKey{canceled: make(chan bool, 1)}
	if _, done := r.read(slow); done {
		t.Fatal("slow request done")
	}
	quick := blockingKey{quick: true}
	r.read(quick)
	select {
	case <-slow.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the request no longer wanted not canceled")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got, done := r.read(quick); done {
			if !got {
				t.Error("the canceled result kept")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the request wanted not read")
		}
		time.Sleep(time.Millisecond)
	}
}

// returningKey holds a canceled lookup until the same key is wanted again.
type returningKey struct {
	state *returningLoad
	other bool
}
type returningLoad struct {
	started chan struct{}
	release chan struct{}
	calls   int
}

func (k returningKey) load(ctx context.Context) bool {
	if k.other {
		return true
	}
	k.state.calls++
	if k.state.calls == 1 {
		close(k.state.started)
		<-k.state.release
		return ctx.Err() == nil
	}
	return true
}
func TestReaderReturnsToCanceledKey(t *testing.T) {
	state := &returningLoad{started: make(chan struct{}), release: make(chan struct{})}
	var r reader[returningKey, bool]
	a := returningKey{state: state}
	r.read(a)
	<-state.started
	r.read(returningKey{state: state, other: true})
	r.read(a)
	close(state.release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got, done := r.read(a); done {
			if !got {
				t.Fatal("kept result from canceled lookup")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lookup did not restart")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestReaderStop(t *testing.T) {
	var r reader[blockingKey, bool]
	key := blockingKey{canceled: make(chan bool, 1)}
	r.read(key)
	r.stop()
	select {
	case <-key.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("stopped reader did not cancel its load")
	}
	r.read(blockingKey{quick: true})
	deadline := time.Now().Add(5 * time.Second)
	for {
		r.mu.Lock()
		busy, done := r.busy, r.done
		r.mu.Unlock()
		if !busy {
			if done {
				t.Fatal("stopped reader saved a result")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stopped reader restarted its load")
		}
		time.Sleep(time.Millisecond)
	}
}
