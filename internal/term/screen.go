// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package term

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"pxy.se/go/binate/internal/display"
)

// Attr is a set of text attributes.
type Attr uint8

const (
	Bold Attr = 1 << iota
	Underline
	Reverse
	Dim
)

// Style is how a cell is drawn: attributes, and SGR parameters such as
// "1;34" or "38;5;208" from BINATE_COLORS, which contain only digits and
// semicolons. Link, if set, makes the cell part of a hyperlink to it
// (OSC 8), written as it is; one that could end the sequence or hold a
// control is not written (see hyperlink), so a caller linking arbitrary
// text quotes it first.
type Style struct {
	SGR  string
	Attr Attr
	Link string
}

// With returns s with attributes added.
func (s Style) With(a Attr) Style {
	s.Attr |= a
	return s
}

// On returns s drawn over bg, as a row's background: bg's parameters come
// after s's, so where both set a colour, bg's applies, and the attributes
// of both apply.
func (s Style) On(bg Style) Style {
	switch {
	case bg.SGR == "":
	case s.SGR == "":
		s.SGR = bg.SGR
	default:
		s.SGR += ";" + bg.SGR
	}
	s.Attr |= bg.Attr
	return s
}

// sgr returns the sequence setting s from a reset state.
func (s Style) sgr() string {
	var b strings.Builder
	b.WriteString("\x1b[0")
	for _, a := range []struct {
		attr Attr
		code string
	}{{Bold, "1"}, {Dim, "2"}, {Underline, "4"}, {Reverse, "7"}} {
		if s.Attr&a.attr != 0 {
			b.WriteString(";" + a.code)
		}
	}
	if s.SGR != "" {
		b.WriteString(";" + s.SGR)
	}
	b.WriteString("m")
	return b.String()
}

// Cell is one column of the screen. A wide character takes its cell and
// the next, which is a continuation cell of width 0.
type Cell struct {
	R     rune
	W     int8
	Style Style
}

var blank = Cell{R: ' ', W: 1}

// Screen is a buffer of cells. Flush writes only the cells that changed
// since the last flush.
type Screen struct {
	W, H  int
	cells []Cell
	prev  []Cell
	full  bool

	// Cursor, if CursorOn, is where the terminal cursor is shown.
	CursorX, CursorY int
	CursorOn         bool
}

// NewScreen returns a blank screen of the given size.
func NewScreen(w, h int) *Screen {
	s := &Screen{}
	s.Resize(w, h)
	return s
}

// Resize changes the size, clearing the screen and redrawing it fully on
// the next flush.
func (s *Screen) Resize(w, h int) {
	s.W, s.H = max(w, 0), max(h, 0)
	s.cells = make([]Cell, s.W*s.H)
	s.prev = nil
	s.Clear()
	s.Invalidate()
}

// Invalidate makes the next flush redraw every cell, as after the terminal
// was used by another program.
func (s *Screen) Invalidate() {
	s.full = true
}

// Clear blanks every cell.
func (s *Screen) Clear() {
	for i := range s.cells {
		s.cells[i] = blank
	}
	s.CursorOn = false
}

// Fill sets the cells of a row from x to x+w to spaces of style st.
func (s *Screen) Fill(x, y, w int, st Style) {
	for i := x; i < x+w; i++ {
		s.Set(i, y, ' ', 1, st)
	}
}

// Copy copies src, as wide as s, into s with its top row at row y, as
// far as s reaches, and its cursor.
func (s *Screen) Copy(src *Screen, y int) {
	if src.W != s.W {
		panic("term: copying a screen of another width")
	}
	for row := range src.H {
		if y+row < 0 || y+row >= s.H {
			continue
		}
		copy(s.cells[(y+row)*s.W:(y+row+1)*s.W], src.cells[row*src.W:(row+1)*src.W])
	}
	s.CursorX, s.CursorY, s.CursorOn = src.CursorX, src.CursorY+y, src.CursorOn
}

// Cell returns the cell at x, y.
func (s *Screen) Cell(x, y int) Cell {
	return s.cells[y*s.W+x]
}

// Set puts r of width w (1 or 2) at x, y. Cells off the screen are
// ignored, and a wide character that doesn't fit is replaced by a space.
func (s *Screen) Set(x, y int, r rune, w int, st Style) {
	if y < 0 || y >= s.H || x < 0 || x >= s.W {
		return
	}
	if w == 2 && x+1 >= s.W {
		r, w = ' ', 1
	}
	s.clearWide(x, y)
	s.cells[y*s.W+x] = Cell{R: r, W: int8(w), Style: st}
	if w == 2 {
		s.clearWide(x+1, y)
		s.cells[y*s.W+x+1] = Cell{W: 0, Style: st}
	}
}

// clearWide blanks the other half of a wide character that x, y is part of.
func (s *Screen) clearWide(x, y int) {
	i := y*s.W + x
	switch c := s.cells[i]; {
	case c.W == 0 && x > 0:
		s.cells[i-1] = Cell{R: ' ', W: 1, Style: s.cells[i-1].Style}
	case c.W == 2 && x+1 < s.W:
		s.cells[i+1] = Cell{R: ' ', W: 1, Style: c.Style}
	}
}

// Text draws glyphs at x, y in at most width columns, escapes in the style
// esc and everything else in st, and returns the columns used. A wide
// glyph that would cross the limit is left out.
func (s *Screen) Text(x, y, width int, glyphs []display.Glyph, st, esc Style) int {
	used := 0
	for _, g := range glyphs {
		if used+int(g.W) > width {
			break
		}
		style := st
		if g.Esc {
			style = esc
		}
		s.Set(x+used, y, g.R, int(g.W), style)
		used += int(g.W)
	}
	return used
}

// Label draws the program's own text, which needs no escaping.
func (s *Screen) Label(x, y, width int, text string, st Style) int {
	return s.Text(x, y, width, display.Plain(text), st, st)
}

// Flush writes the changes since the last flush to w.
func (s *Screen) Flush(w io.Writer) error {
	var b bytes.Buffer
	full := s.full || len(s.prev) != len(s.cells)
	if full {
		b.WriteString("\x1b[0m\x1b[H\x1b[2J")
	}
	b.WriteString("\x1b[?25l")
	x0, y0 := -1, -1
	var cur *Style
	link := ""
	for y := 0; y < s.H; y++ {
		for x := 0; x < s.W; x++ {
			i := y*s.W + x
			c := s.cells[i]
			if c.W == 0 {
				continue
			}
			changed := full || c != s.prev[i]
			// A wide character's continuation cell changes with it.
			if !changed && c.W == 2 && x+1 < s.W && s.cells[i+1] != s.prev[i+1] {
				changed = true
			}
			if !changed {
				continue
			}
			if x != x0 || y != y0 {
				fmt.Fprintf(&b, "\x1b[%d;%dH", y+1, x+1)
			}
			// The link is written apart, and doesn't change the SGR.
			if st := c.Style; cur == nil || cur.SGR != st.SGR || cur.Attr != st.Attr {
				b.WriteString(st.sgr())
				cur = &st
			}
			if c.Style.Link != link {
				link = c.Style.Link
				b.WriteString(hyperlink(link))
			}
			b.WriteRune(c.R)
			x0, y0 = x+int(c.W), y
		}
	}
	if link != "" {
		b.WriteString(hyperlink(""))
	}
	b.WriteString("\x1b[0m")
	if s.CursorOn {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", s.CursorY+1, s.CursorX+1)
	}
	s.prev = append(s.prev[:0], s.cells...)
	s.full = false
	_, err := w.Write(b.Bytes())
	return err
}

// hyperlink returns the sequence starting a hyperlink to target, or
// ending one if target is empty or unsafe: a target that is not valid
// UTF-8 or holds a control, C0, DEL or C1, which could end the sequence,
// is left unlinked rather than changed.
func hyperlink(target string) string {
	if !SafeLink(target) {
		target = ""
	}
	return "\x1b]8;;" + target + "\x1b\\"
}

// SafeLink reports whether target can be written in a hyperlink as it is:
// valid UTF-8 without controls.
func SafeLink(target string) bool {
	if !utf8.ValidString(target) {
		return false
	}
	for _, r := range target {
		if r < 0x20 || r >= 0x7f && r < 0xa0 {
			return false
		}
	}
	return true
}

// String returns the screen's text, one line per row with trailing spaces
// removed, for tests.
func (s *Screen) String() string {
	var b strings.Builder
	for y := 0; y < s.H; y++ {
		var line strings.Builder
		for x := 0; x < s.W; x++ {
			if c := s.cells[y*s.W+x]; c.W > 0 {
				line.WriteRune(c.R)
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}
