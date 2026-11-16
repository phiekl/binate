// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/tree"
	"pxy.se/go/binate/internal/view"
)

func TestPreview(t *testing.T) {
	left := writeTestFiles(t, map[string]string{
		"changed": "a\nb\n", "same": "s\n", "left": "l\n", "bin": "\xff", "dir/x": "x",
	})
	right := writeTestFiles(t, map[string]string{
		"changed": "a\nB\n", "same": "s\n", "bin": "\xfe", "dir/y": "y",
	})
	if err := os.Symlink("t", filepath.Join(right, "left2")); err != nil {
		t.Fatal(err)
	}
	s := scan.Start(source.Disk(left), source.Disk(right), scan.Options{Compare: scan.DefaultAttrs}, nil)
	s.Wait()
	a := testApp(80, 40)
	ts := &TreeScreen{
		app: a, scan: s, model: tree.New(s.Root), height: 40, styles: colors.Default(),
		view:  &view.TreeView{Styles: colors.Default(), Left: left, Right: right},
		roots: [2]source.Root{source.Disk(left), source.Disk(right)},
	}
	// wait returns the preview of the entry at path once read.
	wait := func(path string) (*view.DiffView, bool) {
		t.Helper()
		n := s.Root.Find(path)
		for range 1000 {
			s.Lock()
			pv, ok := ts.preview(n)
			s.Unlock()
			if pv != nil || !ok {
				return pv, ok
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("%s never read", path)
		return nil, false
	}
	for _, path := range []string{"same", "dir", "left2"} {
		if _, ok := wait(path); ok {
			t.Errorf("%s has a preview", path)
		}
	}
	if pv, ok := wait("changed"); !ok || !pv.HasChanges() || pv.Message != "" {
		t.Errorf("changed: %v %+v", ok, pv)
	}
	if pv, ok := wait("left"); !ok || len(pv.Rows) != 1 || len(pv.R.Lines) != 0 {
		t.Errorf("left only: %v %+v", ok, pv)
	}
	binary := "Unable to show data that is not UTF-8 encoded."
	if pv, _ := wait("bin"); pv == nil || pv.SideErrors != [2]string{binary, binary} {
		t.Errorf("binary: %+v", pv)
	}

	// Read again in a new generation, as after an edit.
	if err := os.WriteFile(filepath.Join(left, "changed"), []byte("a\nB\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts.statGen++
	if pv, _ := wait("changed"); len(pv.L.Lines) != 3 {
		t.Errorf("not read again: %+v", pv.L)
	}
}
