// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"pxy.se/go/binate/internal/diff"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/view"
)

// previewReader reads and compares the selected pair of files for the
// tree view's preview.
type previewReader = reader[previewKey, *view.DiffView]

// previewKey is a request: the two sides, the zero sideKey for one
// missing, and a generation, as statKey's.
type previewKey struct {
	sides [2]sideKey
	gen   int
}

// load reads and compares both sides, or says why each that can't be
// shown can't.
func (key previewKey) load() *view.DiffView {
	var texts [2]diff.Text
	var errs [2]error
	for i, sk := range key.sides {
		texts[i], errs[i] = Source{Root: sk.root, Rel: sk.rel, Missing: sk.root == nil}.Load()
	}
	if why, ok := sideTexts(loadError(errs[0], errs[1])); ok {
		return &view.DiffView{SideErrors: why}
	}
	return view.NewPreview(texts[0], texts[1])
}

// hasPreview reports whether n's differences are previewed: a pair of
// files whose contents differ, or a file on one side only.
func hasPreview(n *scan.Node) bool {
	switch n.State {
	case scan.Differ:
		return n.L.Kind == scan.File && n.R.Kind == scan.File && n.Own&scan.Content != 0
	case scan.LeftOnly:
		return n.L.Kind == scan.File
	case scan.RightOnly:
		return n.R.Kind == scan.File
	}
	return false
}

// preview returns the preview of the selected entry n, nil while it is
// read, and whether n has one. The scan's lock must be held.
func (t *TreeScreen) preview(n *scan.Node) (*view.DiffView, bool) {
	if !hasPreview(n) {
		return nil, false
	}
	key := previewKey{gen: t.statGen}
	for i := range 2 {
		if n.Side(i == 1).Kind != scan.Missing {
			key.sides[i] = t.sideKey(n, i == 1)
		}
	}
	pv, done := t.previews.read(key)
	if !done {
		return nil, true
	}
	pv.Styles = t.styles
	return pv, true
}
