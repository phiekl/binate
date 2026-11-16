// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"fmt"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/term"
)

// TextView shows a text in one box across the screen, its lines broken to
// fit, as a commit's message, with the status bar below.
type TextView struct {
	Styles colors.Styles
	// Lead, dimmed, and Title are in the box's top border.
	Lead  string
	Title []display.Glyph
	Lines []string
	// tail, if set, returns more lines after Lines for the width of the
	// text, as a commit's stat, scaled to it (see SetTail).
	tail func(width int) []string
	// Top is the first visual line shown.
	Top int
	// Notice, if set, is shown in the status bar.
	Notice string

	rows   [][]display.Glyph
	width  int
	height int
}

// textRows returns the rows inside the box on a screen of height h: all
// but its borders and the status bar.
func textRows(h int) int { return max(h-3, 0) }

// SetTail sets the lines shown after Lines, for the width of the text, or
// none if tail is nil.
func (v *TextView) SetTail(tail func(width int) []string) {
	v.tail = tail
	v.rows = nil
}

// layout breaks the lines into rows of width columns.
func (v *TextView) layout(width int) {
	if v.rows != nil && v.width == width {
		return
	}
	v.width = width
	v.rows = [][]display.Glyph{}
	lines := v.Lines
	if v.tail != nil {
		lines = append(lines[:len(lines):len(lines)], v.tail(width)...)
	}
	for _, line := range lines {
		parts := breakLines(display.Content([]byte(line)), width)
		if len(parts) == 0 {
			parts = [][]display.Glyph{nil}
		}
		v.rows = append(v.rows, parts...)
	}
}

// Scroll scrolls by delta visual lines.
func (v *TextView) Scroll(delta int) {
	v.Top += delta
	v.clamp()
}

// ScrollTo scrolls to the start (true) or end.
func (v *TextView) ScrollTo(start bool) {
	if start {
		v.Top = 0
	} else {
		v.Top = len(v.rows)
	}
	v.clamp()
}

// Page returns the distance of a page.
func (v *TextView) Page() int {
	return max(textRows(v.height)-1, 1)
}

func (v *TextView) clamp() {
	v.Top = max(0, min(v.Top, len(v.rows)-textRows(v.height)))
}

// Draw draws the box, the lines in it from Top, and the status bar.
func (v *TextView) Draw(s *term.Screen) {
	v.height = s.H
	v.layout(max(s.W-4, 1))
	v.clamp()
	st := v.Styles
	var leads []string
	if v.Lead != "" {
		leads = []string{v.Lead}
	}
	drawBox(s, 0, s.W, s.H-1, leads, v.Title, "", st)
	h := textRows(s.H)
	for i := 0; i < h && v.Top+i < len(v.rows); i++ {
		s.Text(2, 1+i, s.W-4, v.rows[v.Top+i], st.Plain, st.Escape)
	}
	var items []barItem
	switch {
	case v.Notice != "":
		items = append(items, barItem{text: v.Notice, style: st.Status, note: true})
	case len(v.rows) > 0:
		bottom := min(v.Top+h, len(v.rows))
		items = append(items, barItem{text: fmt.Sprintf("%d%%", bottom*100/len(v.rows)), style: st.Status})
	}
	drawBar(s, s.H-1, items, st)
}
