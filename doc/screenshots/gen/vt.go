// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// term emulates the part of a terminal that binate uses: cursor
// positioning, erasing, SGR attributes and colours, and OSC 8 hyperlinks.
// Everything else is ignored.
type term struct {
	w, h   int
	cells  []cell
	x, y   int
	cur    attr
	link   bool
	unread []byte // an incomplete sequence or character
}

type cell struct {
	r rune
	a attr
}

type attr struct {
	fg, bg                      tcolor
	bold, dim, under, rev, link bool
}

type colorKind uint8

const (
	colorDefault colorKind = iota
	colorIndex
	colorRGB
)

type tcolor struct {
	kind    colorKind
	idx     uint8
	r, g, b uint8
}

func newTerm(w, h int) *term {
	t := &term{w: w, h: h, cells: make([]cell, w*h)}
	t.erase(0, len(t.cells))
	return t
}

func (t *term) erase(from, to int) {
	for i := from; i < to; i++ {
		t.cells[i] = cell{r: ' ', a: attr{bg: t.cur.bg}}
	}
}

// text returns the screen as lines without trailing spaces.
func (t *term) text() string {
	var b strings.Builder
	for y := 0; y < t.h; y++ {
		var line strings.Builder
		for _, c := range t.cells[y*t.w : (y+1)*t.w] {
			line.WriteRune(c.r)
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func (t *term) write(p []byte) {
	data := append(t.unread, p...)
	t.unread = nil
	for i := 0; i < len(data); {
		c := data[i]
		n := 1
		switch {
		case c == 0x1b:
			n = t.escape(data[i:])
		case c == '\r':
			t.x = 0
		case c == '\n':
			t.y = min(t.y+1, t.h-1)
		case c == '\b':
			t.x = max(t.x-1, 0)
		case c < 0x20 || c == 0x7f:
		default:
			if !utf8.FullRune(data[i:]) {
				n = 0
				break
			}
			var r rune
			r, n = utf8.DecodeRune(data[i:])
			t.put(r)
		}
		if n == 0 {
			t.unread = append([]byte(nil), data[i:]...)
			return
		}
		i += n
	}
}

func (t *term) put(r rune) {
	if t.x < t.w {
		a := t.cur
		a.link = t.link
		t.cells[t.y*t.w+t.x] = cell{r: r, a: a}
		t.x++
	}
}

// escape handles the sequence at the start of b and returns its length,
// or 0 if it is incomplete.
func (t *term) escape(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	switch b[1] {
	case '[':
		for j := 2; j < len(b); j++ {
			if b[j] >= 0x40 && b[j] <= 0x7e {
				t.csi(string(b[2:j]), b[j])
				return j + 1
			}
		}
		return 0
	case ']':
		for j := 2; j < len(b); j++ {
			switch {
			case b[j] == 0x07:
				t.osc(string(b[2:j]))
				return j + 1
			case b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\':
				t.osc(string(b[2:j]))
				return j + 2
			}
		}
		return 0
	}
	return 2
}

func (t *term) osc(s string) {
	if rest, ok := strings.CutPrefix(s, "8;"); ok {
		_, target, _ := strings.Cut(rest, ";")
		t.link = target != ""
	}
}

func (t *term) csi(params string, final byte) {
	if strings.HasPrefix(params, "?") {
		return // private modes, such as the alternate screen
	}
	ps := strings.Split(params, ";")
	p := func(i, def int) int {
		if i < len(ps) {
			if n, err := strconv.Atoi(ps[i]); err == nil {
				return n
			}
		}
		return def
	}
	switch final {
	case 'H', 'f':
		t.y = min(max(p(0, 1)-1, 0), t.h-1)
		t.x = min(max(p(1, 1)-1, 0), t.w-1)
	case 'A':
		t.y = max(t.y-p(0, 1), 0)
	case 'B':
		t.y = min(t.y+p(0, 1), t.h-1)
	case 'C':
		t.x = min(t.x+p(0, 1), t.w-1)
	case 'D':
		t.x = max(t.x-p(0, 1), 0)
	case 'J':
		switch p(0, 0) {
		case 0:
			t.erase(t.y*t.w+t.x, len(t.cells))
		case 1:
			t.erase(0, t.y*t.w+t.x+1)
		default:
			t.erase(0, len(t.cells))
		}
	case 'K':
		start, end := t.y*t.w, (t.y+1)*t.w
		switch p(0, 0) {
		case 0:
			t.erase(start+t.x, end)
		case 1:
			t.erase(start, start+t.x+1)
		default:
			t.erase(start, end)
		}
	case 'm':
		t.sgr(ps)
	}
}

func (t *term) sgr(ps []string) {
	a := &t.cur
	num := func(i int) int {
		n, _ := strconv.Atoi(ps[i])
		return n
	}
	for i := 0; i < len(ps); i++ {
		switch n := num(i); {
		case n == 0:
			*a = attr{}
		case n == 1:
			a.bold = true
		case n == 2:
			a.dim = true
		case n == 4:
			a.under = true
		case n == 7:
			a.rev = true
		case n == 22:
			a.bold, a.dim = false, false
		case n == 24:
			a.under = false
		case n == 27:
			a.rev = false
		case n >= 30 && n <= 37:
			a.fg = tcolor{kind: colorIndex, idx: uint8(n - 30)}
		case n == 39:
			a.fg = tcolor{}
		case n >= 40 && n <= 47:
			a.bg = tcolor{kind: colorIndex, idx: uint8(n - 40)}
		case n == 49:
			a.bg = tcolor{}
		case n >= 90 && n <= 97:
			a.fg = tcolor{kind: colorIndex, idx: uint8(n - 90 + 8)}
		case n >= 100 && n <= 107:
			a.bg = tcolor{kind: colorIndex, idx: uint8(n - 100 + 8)}
		case n == 38 || n == 48:
			var c tcolor
			switch {
			case i+2 < len(ps) && ps[i+1] == "5":
				c = tcolor{kind: colorIndex, idx: uint8(num(i + 2))}
				i += 2
			case i+4 < len(ps) && ps[i+1] == "2":
				c = tcolor{kind: colorRGB, r: uint8(num(i + 2)), g: uint8(num(i + 3)), b: uint8(num(i + 4))}
				i += 4
			}
			if n == 38 {
				a.fg = c
			} else {
				a.bg = c
			}
		}
	}
}
