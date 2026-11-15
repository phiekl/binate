// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
	key := statKey{paths: [2]string{f, ""}}
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
	other := statKey{paths: [2]string{"", filepath.Join(dir, "missing")}}
	r.read(other)
	r.fetch(key)
	if got, done := r.read(other); done && got[0].st != nil {
		t.Errorf("an earlier request's result kept: %+v", got)
	}
	if got := waitRead(t, &r, other); got[1].err == nil {
		t.Errorf("missing file read %+v", got)
	}
}
