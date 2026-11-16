// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package view draws the screens into a cell buffer: the tree view,
// the diff view, the Help overlay, the Edit popup and the status bar. All text
// from the file system goes through internal/display.
package view

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/term"
)

// Panes is the layout of the two sides: each in a box, with the mark
// column between the boxes, a space either side of it.
type Panes struct {
	// LeftBox, LeftBoxW, RightBox and RightBoxW are each box's first
	// column and width, borders included.
	LeftBox, LeftBoxW, RightBox, RightBoxW int
	// LeftX, LeftW, RightX and RightW are the columns inside each box.
	LeftX, LeftW, RightX, RightW int
	Mark                         int
}

// Layout splits a width into panes.
func Layout(w int) Panes {
	lb := (w - 3) / 2
	rb := w - 3 - lb
	return Panes{
		LeftBox: 0, LeftBoxW: lb, RightBox: lb + 3, RightBoxW: rb,
		LeftX: 1, LeftW: max(lb-2, 0), RightX: lb + 4, RightW: max(rb-2, 0),
		Mark: lb + 1,
	}
}

// drawBoxes draws both panes' boxes from row 0 to row h-1, blanking their
// insides. The titles, shortened in the middle, go in the top borders,
// after a lead, dimmed, if leads has any, and the labels at the right of
// the bottom borders. The lead is the first of leads, longest first, that
// leaves room for the whole title, or else the last: it is shortened
// before the title, and kept over the title where there is little room.
func drawBoxes(s *term.Screen, p Panes, h int, leads []string, titles [2][]display.Glyph, labels [2]string, st colors.Styles) {
	for i, x := range []int{p.LeftBox, p.RightBox} {
		w := []int{p.LeftBoxW, p.RightBoxW}[i]
		drawBox(s, x, w, h, leads, titles[i], labels[i], st)
	}
}

func drawBox(s *term.Screen, x, w, h int, leads []string, title []display.Glyph, label string, st colors.Styles) {
	if w < 2 || h < 2 {
		return
	}
	border := st.Border
	horizontal := strings.Repeat("─", w-2)
	s.Label(x, 0, w, "┌"+horizontal+"┐", border)
	s.Label(x, h-1, w, "└"+horizontal+"┘", border)
	for y := 1; y < h-1; y++ {
		s.Label(x, y, 1, "│", border)
		s.Fill(x+1, y, w-2, st.Plain)
		s.Label(x+w-1, y, 1, "│", border)
	}
	// "┌ title ─┐": a space either side of the title, and a corner and at
	// least one line after it.
	if room := w - 5; room > 0 && len(title) > 0 {
		var lead string
		for _, lead = range leads {
			if len(lead)+display.Width(title) <= room {
				break
			}
		}
		s.Label(x+1, 0, 1, " ", border)
		used := s.Label(x+2, 0, room, lead, st.Plain.With(term.Dim))
		if used < room {
			title = ShortenMiddle(title, room-used)
			used += s.Text(x+2+used, 0, room-used, title, st.Plain.With(term.Bold), st.Escape)
		} else {
			// No room for the title: the space after it ends the lead.
			used = len(strings.TrimRight(lead[:used], " "))
		}
		s.Label(x+2+used, 0, 1, " ", border)
	}
	if n := len([]rune(label)); label != "" && n+4 <= w {
		s.Label(x+w-n-3, h-1, n+2, " "+label+" ", border)
	}
}

// drawNote draws msg inside a pane, from its top, broken into lines of
// the pane's width.
func drawNote(s *term.Screen, x, y, w, h int, msg string, st colors.Styles) {
	drawNoteIn(s, x, y, w, h, msg, st.Plain, st.Escape)
}

// drawNoteIn is drawNote in style, with escapes in esc.
func drawNoteIn(s *term.Screen, x, y, w, h int, msg string, style, esc term.Style) {
	for i, line := range breakLines(display.Name(msg), w) {
		if i >= h {
			break
		}
		s.Text(x, y+i, w, line, style, esc)
	}
}

// drawSideErrors draws, from row y in h rows, why each side that can't
// be shown can't, as errors set say, in red, and in the box of a side
// that could be, which side couldn't.
func drawSideErrors(s *term.Screen, p Panes, y, h int, errors [2]string, st colors.Styles) {
	for i, x := range []int{p.LeftX, p.RightX} {
		w := []int{p.LeftW, p.RightW}[i]
		switch {
		case errors[i] != "":
			drawNoteIn(s, x, y, w, h, errors[i], st.Type, st.Escape)
		case errors[1-i] != "":
			side := [2]string{"left", "right"}[1-i]
			drawNote(s, x, y, w, h, "Unable to compare files due to an error on the "+side+" side.", st)
		}
	}
}

// breakLines breaks glyphs into lines of at most width columns, after a
// space where there is one, and leaves out the space.
func breakLines(glyphs []display.Glyph, width int) [][]display.Glyph {
	var lines [][]display.Glyph
	for len(glyphs) > 0 && width > 0 {
		if display.Width(glyphs) <= width {
			return append(lines, glyphs)
		}
		end, w, space := 0, 0, -1
		for end < len(glyphs) && w+int(glyphs[end].W) <= width {
			if glyphs[end].R == ' ' && !glyphs[end].Esc {
				space = end
			}
			w += int(glyphs[end].W)
			end++
		}
		switch {
		case end < len(glyphs) && glyphs[end].R == ' ' && !glyphs[end].Esc:
			lines = append(lines, glyphs[:end])
			glyphs = glyphs[end+1:]
		case space > 0:
			lines = append(lines, glyphs[:space])
			glyphs = glyphs[space+1:]
		default:
			end = max(end, 1)
			lines = append(lines, glyphs[:end])
			glyphs = glyphs[end:]
		}
	}
	return lines
}

// HumanSize formats a size in bytes with binary units: 512, 4.2K, 13M.
func HumanSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10)
	}
	f := float64(n)
	i := -1
	for f >= 1024 && i < 5 {
		f /= 1024
		i++
	}
	unit := "KMGTPE"[i]
	if f < 9.95 {
		return fmt.Sprintf("%.1f%c", f, unit)
	}
	return fmt.Sprintf("%.0f%c", f, unit)
}

// HomePath shows a path under home with "~" in its place.
func HomePath(path, home string) string {
	if home == "" || home == "/" || !filepath.IsAbs(path) {
		return path
	}
	home = strings.TrimSuffix(home, "/")
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+"/"); ok {
		return "~/" + rest
	}
	return path
}

// ellipsis marks text left out.
var ellipsis = display.Plain("…")

// ShortenMiddle fits glyphs into width columns by leaving out the middle,
// marked with "…".
func ShortenMiddle(glyphs []display.Glyph, width int) []display.Glyph {
	if display.Width(glyphs) <= width {
		return glyphs
	}
	if width <= 1 {
		return ellipsis[:min(width, 1)]
	}
	keep := width - 1
	headW := keep / 2
	tailW := keep - headW
	var head, tail []display.Glyph
	w := 0
	for _, g := range glyphs {
		if w+int(g.W) > headW {
			break
		}
		head = append(head, g)
		w += int(g.W)
	}
	w = 0
	start := len(glyphs)
	for i := len(glyphs) - 1; i >= 0; i-- {
		if w+int(glyphs[i].W) > tailW {
			break
		}
		w += int(glyphs[i].W)
		start = i
	}
	tail = glyphs[start:]
	out := append(append(append([]display.Glyph{}, head...), ellipsis...), tail...)
	return out
}

// ShortenEnd fits glyphs into width columns by cutting the end, marked
// with "…".
func ShortenEnd(glyphs []display.Glyph, width int) []display.Glyph {
	if display.Width(glyphs) <= width {
		return glyphs
	}
	if width <= 0 {
		return nil
	}
	var out []display.Glyph
	w := 0
	for _, g := range glyphs {
		if w+int(g.W) > width-1 {
			break
		}
		out = append(out, g)
		w += int(g.W)
	}
	return append(out, ellipsis...)
}

// reverseEscape returns the style of an escape on text in reverse video,
// as the status bar. The escape style is usually reverse video too, so
// there the escape turns reverse off and is underlined instead, to stay
// apart.
func reverseEscape(esc term.Style) term.Style {
	sgr := "27"
	if esc.SGR != "" {
		sgr = esc.SGR + ";27"
	}
	return term.Style{SGR: sgr, Attr: (esc.Attr &^ term.Reverse) | term.Underline}
}

// rowEscape returns the style of an escape in a row with the background
// bg: the selection's or a state's tint, or none.
func rowEscape(esc, bg term.Style) term.Style {
	if bg.Attr&term.Reverse != 0 || bg.SGR == "7" {
		esc = reverseEscape(esc)
	}
	return esc.On(bg)
}

// barItem is one item of a status bar: text shown in brackets, or a note
// without them, in its own style.
type barItem struct {
	text  string
	style term.Style
	note  bool
}

// drawBar draws a status bar on line y: its items, a column in from the
// edges, each in brackets in the border's style, cut at the right edge.
func drawBar(s *term.Screen, y int, items []barItem, st colors.Styles) {
	s.Fill(0, y, s.W, st.Status)
	esc := rowEscape(st.Escape, st.Status)
	bracket := st.Border.On(st.Status)
	x, end := 1, max(s.W-1, 1)
	put := func(text string, style term.Style) {
		x += s.Text(x, y, end-x, display.Name(text), style.On(st.Status), esc)
	}
	for i, it := range items {
		if i > 0 {
			put(" ", st.Status)
		}
		if it.note {
			put(it.text, it.style)
			continue
		}
		put("[", bracket)
		put(it.text, it.style)
		put("]", bracket)
	}
}

// Header is a row above the boxes naming what both sides are of: a
// repository's directory.
type Header struct {
	// Label says what Path is.
	Label string
	Path  string
	// Home is shown as "~" in Path.
	Home string
}

// DrawHeader draws h in the top row, its path shortened in the middle if
// it doesn't fit, and linked to in full, quoted for the shell as the
// panels' paths are.
func DrawHeader(s *term.Screen, h Header, st colors.Styles) {
	s.Fill(0, 0, s.W, st.Plain)
	x := 1
	x += s.Label(x, 0, s.W-x, h.Label+" ", st.Plain.With(term.Dim))
	room := max(s.W-x-1, 0)
	style := st.Plain.With(term.Bold)
	esc := st.Escape
	style.Link = display.ShellQuote(h.Path)
	esc.Link = style.Link
	s.Text(x, 0, room, ShortenMiddle(display.Name(HomePath(h.Path, h.Home)), room), style, esc)
}
