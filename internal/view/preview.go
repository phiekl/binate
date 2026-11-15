// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"pxy.se/go/binate/internal/diff"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/term"
)

// previewContext is the number of unchanged lines a preview shows around
// changes: fewer than the diff view, as it has few rows.
const previewContext = 1

// NewPreview returns a diff view of two texts for the tree view's preview
// of the selected pair, comparing them: done where it may take a while.
func NewPreview(l, r diff.Text) *DiffView {
	v := &DiffView{Context: previewContext}
	v.SetTexts(l, r)
	return v
}

// drawPreview draws the preview pv in h rows from row y: nil is one still
// read, and SideErrors why a side can't be shown.
func (v *TreeView) drawPreview(s *term.Screen, p Panes, y, h int, pv *DiffView) {
	switch {
	case pv == nil:
		s.Label(p.LeftX, y, p.LeftW, "…", v.Styles.Plain.With(term.Dim))
	case pv.failed():
		drawSideErrors(s, p, y, h, pv.SideErrors, v.Styles)
	default:
		pv.drawPreview(s, p, y, h)
	}
}

// drawPreview draws the first changes in h rows from row y, without
// scrolling, the last saying so if more are left out.
func (v *DiffView) drawPreview(s *term.Screen, p Panes, y, h int) {
	lines := v.layout(s.W)
	if !v.HasChanges() {
		// The scan found a difference the lines don't show, or the files
		// changed since.
		msg := "no differences"
		switch {
		case len(v.Rows) == 0:
			msg = "empty"
		case v.L.CRLF != v.R.CRLF:
			msg = "only line ends differ"
		}
		drawCentered(s, p, y, dotted(msg, min(p.LeftW, p.RightW)), v.Styles)
		return
	}
	// From the first change's context, without the separator for the
	// unchanged lines before it: the line numbers tell where it is.
	for len(lines) > 0 && lines[0].row < 0 {
		lines = lines[1:]
	}
	if len(lines) > h && changesIn(v.Rows, lines[h-1:]) {
		v.drawLines(s, p, y, h-1, lines)
		text := moreChanges(min(p.LeftW, p.RightW))
		drawCentered(s, p, y+h-1, text, v.Styles)
		return
	}
	v.drawLines(s, p, y, h, lines)
}

// moreChanges returns the note for changes left out of a preview, in a
// pane w columns wide: shorter if the full text doesn't fit.
func moreChanges(w int) string {
	for _, text := range []string{"··· more changes, Enter shows all ···", "··· Enter shows all ···"} {
		if len([]rune(text)) <= w {
			return text
		}
	}
	return "···"
}

// noPreview says why n, compared for compare, has no preview.
func noPreview(n *scan.Node, compare scan.Attrs) string {
	switch {
	case n.IsDir():
		return "directories have no preview"
	case n.L.Kind != scan.File || n.R.Kind != scan.File:
		return "only files have a preview"
	case compare&scan.Content == 0:
		return "contents not compared"
	case n.Data == scan.Pending:
		return "contents not compared yet"
	case n.Data == scan.Same:
		return "contents identical"
	}
	// Unverified, though the pair may differ in its attributes.
	return "contents not verified"
}

// changesIn reports whether any of lines shows a changed row of rows.
func changesIn(rows []diff.Row, lines []vline) bool {
	for _, l := range lines {
		if l.row >= 0 && rows[l.row].Kind != diff.Same {
			return true
		}
	}
	return false
}
