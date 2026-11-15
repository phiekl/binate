// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package display turns text from the file system into text that is safe to
// show: characters that would control the terminal, and characters with no
// width, are replaced by escapes such as \x1b and \u200b. On the screen,
// escapes are marked so they can be highlighted; on stderr, where they
// can't be, a literal backslash is doubled instead.
//
// Drawing code must show file-system text only through this package.
package display

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/width"
)

// TabWidth is the distance between tab stops in file contents.
const TabWidth = 4

// Glyph is one character as shown. A wide character is one glyph of width
// 2; an escape is one glyph per character of the escape, all marked Esc.
type Glyph struct {
	R   rune
	W   int8
	Esc bool
	// Src is the byte offset, in the text given, of the character the
	// glyph shows (part of).
	Src int32
}

// mode selects how tabs, line feeds and backslashes are shown.
type mode int

const (
	modeContent mode = iota // tabs expanded, backslashes plain
	modeName                // tabs escaped, backslashes plain
	modeStderr              // tabs escaped, backslashes doubled
)

// Content returns one line of file contents, without its line end, as
// shown: tabs expanded to the next multiple of TabWidth columns, and
// anything that is not printable escaped. A CR left in the line is shown
// as \r.
func Content(line []byte) []Glyph {
	return appendText(nil, line, modeContent)
}

// Name returns a file name or path as shown. Tabs, line feeds and CRs are
// escaped as \t, \n and \r, as everything else not printable.
func Name(s string) []Glyph {
	return appendText(nil, []byte(s), modeName)
}

// Stderr returns s for a message on stderr, escaped as Name does, with
// each literal backslash doubled so that escapes stay unambiguous without
// highlighting.
func Stderr(s string) string {
	if !needsStderrEscape(s) {
		return s
	}
	return String(appendText(nil, []byte(s), modeStderr))
}

func needsStderrEscape(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r == '\\' || r < 0x20 || hidden(r) {
			return true
		}
	}
	return false
}

// Unsafe reports whether r is escaped when shown: a control character, or
// a character that is not printable or has no width.
func Unsafe(r rune) bool {
	return r < 0x20 || hidden(r)
}

// shellEscapes are the bytes $'...' writes by name, as bash does.
var shellEscapes = map[byte]string{
	0x07: `\a`, 0x08: `\b`, '\t': `\t`, '\n': `\n`, 0x0b: `\v`, 0x0c: `\f`, '\r': `\r`, 0x1b: `\E`,
}

// ShellQuote returns s quoted for the shell as bash's ${s@Q} does, but in
// printable ASCII alone: in single quotes, or, if s holds any other byte,
// in $'...' with each such byte as an escape. Bash would keep printable
// characters beyond ASCII, such as å, as they are; here they are escaped
// byte by byte, as invalid UTF-8 is.
func ShellQuote(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r >= 0x7f }) {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	var b strings.Builder
	b.WriteString("$'")
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c < 0x20 || c >= 0x7f:
			if e, ok := shellEscapes[c]; ok {
				b.WriteString(e)
			} else {
				fmt.Fprintf(&b, `\%03o`, c)
			}
		case c == '\'' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// hidden reports whether r must be escaped although it is valid UTF-8 and
// not a C0 control: DEL, C1 controls, other characters that are not
// printable (format characters, spaces other than U+0020, unassigned and
// private-use characters), and combining marks, which have no width.
func hidden(r rune) bool {
	return r == 0x7f || !unicode.IsPrint(r) || unicode.In(r, unicode.Mn, unicode.Me)
}

func appendText(out []Glyph, text []byte, m mode) []Glyph {
	column := 0
	for i := 0; i < len(text); {
		start := len(out)
		var size int
		out, size = appendChar(out, text, i, column, m)
		column += Width(out[start:])
		i += size
	}
	return out
}

// appendChar appends the glyphs of the character at text[i], shown at
// column, and returns them with the character's size in bytes.
func appendChar(out []Glyph, text []byte, i, column int, m mode) ([]Glyph, int) {
	r, size := utf8.DecodeRune(text[i:])
	start := len(out)
	switch {
	case r == utf8.RuneError && size <= 1:
		out = appendEscape(out, fmt.Sprintf(`\x%02x`, text[i]))
	case r == '\t' && m == modeContent:
		spaces := TabWidth - column%TabWidth
		for range spaces {
			out = append(out, Glyph{R: ' ', W: 1})
		}
	case r == '\t':
		out = appendEscape(out, `\t`)
	case r == '\n':
		out = appendEscape(out, `\n`)
	case r == '\r':
		out = appendEscape(out, `\r`)
	case r < 0x20 || r == 0x7f:
		out = appendEscape(out, fmt.Sprintf(`\x%02x`, r))
	case hidden(r) && r > 0xffff:
		out = appendEscape(out, fmt.Sprintf(`\U%08x`, r))
	case hidden(r):
		out = appendEscape(out, fmt.Sprintf(`\u%04x`, r))
	case r == '\\' && m == modeStderr:
		out = append(out, Glyph{R: '\\', W: 1}, Glyph{R: '\\', W: 1})
	default:
		out = append(out, Glyph{R: r, W: int8(RuneWidth(r))})
	}
	for j := start; j < len(out); j++ {
		out[j].Src = int32(i)
	}
	return out, size
}

// Pos is the place of a glyph in a line of contents: the byte offset of
// the character it shows (part of), the column that character starts at,
// and which of its glyphs it is, as a tab or an escape has several, each
// one column wide. With it, part of a long line can be shown without
// turning all of it into glyphs.
type Pos struct {
	Byte, Col, Glyph int
}

// Column returns the column of the glyph at p.
func (p Pos) Column() int { return p.Col + p.Glyph }

// ContentFrom returns the glyphs of a line of contents, as Content shows
// them, from the glyph at p until they fill width columns or the line
// ends. The last character's glyphs are all returned, so there may be a
// few more columns.
func ContentFrom(line []byte, p Pos, width int) []Glyph {
	var out []Glyph
	column, used, skip := p.Col, 0, p.Glyph
	for i := p.Byte; i < len(line) && used < width; {
		start := len(out)
		var size int
		out, size = appendChar(out, line, i, column, modeContent)
		column += Width(out[start:])
		if skip > 0 {
			out = append(out[:start], out[start+min(skip, len(out)-start):]...)
			skip = 0
		}
		used += Width(out[start:])
		i += size
	}
	return out
}

// ContentSeek returns the place, in a line of contents, of the glyph shown
// at column col, or of a wide glyph that starts before it and covers it, or
// the line's end.
func ContentSeek(line []byte, col int) Pos {
	var buf []Glyph
	column := 0
	for i := 0; i < len(line); {
		var size int
		buf, size = appendChar(buf[:0], line, i, column, modeContent)
		c := column
		for k, g := range buf {
			if c+int(g.W) > col {
				return Pos{Byte: i, Col: column, Glyph: k}
			}
			c += int(g.W)
		}
		column = c
		i += size
	}
	return Pos{Byte: len(line), Col: column}
}

// ContentWraps returns where each part of a line of contents starts when
// it is wrapped into parts of width columns. A wide glyph that would cross
// the end of a part starts the next one instead, so none is cut in two.
func ContentWraps(line []byte, width int) []Pos {
	starts := []Pos{{}}
	var buf []Glyph
	column, from := 0, 0
	for i := 0; i < len(line); {
		var size int
		buf, size = appendChar(buf[:0], line, i, column, modeContent)
		c := column
		for k, g := range buf {
			w := int(g.W)
			if c+w-from > width && c > from {
				starts = append(starts, Pos{Byte: i, Col: column, Glyph: k})
				from = c
			}
			c += w
		}
		column = c
		i += size
	}
	return starts
}

func appendEscape(out []Glyph, esc string) []Glyph {
	for _, r := range esc {
		out = append(out, Glyph{R: r, W: 1, Esc: true})
	}
	return out
}

// RuneWidth returns the columns of a printable character: 2 for East Asian
// wide and fullwidth characters, 1 for everything else. Characters with no
// width are always escaped, so they never reach this function.
func RuneWidth(r rune) int {
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

// Width returns the columns of glyphs.
func Width(glyphs []Glyph) int {
	n := 0
	for _, g := range glyphs {
		n += int(g.W)
	}
	return n
}

// String returns the shown text of glyphs.
func String(glyphs []Glyph) string {
	var b strings.Builder
	for _, g := range glyphs {
		b.WriteRune(g.R)
	}
	return b.String()
}

// Plain returns glyphs for text of the program's own, such as labels,
// which is shown as is. It must not contain characters that Name would
// escape; that is a bug.
func Plain(s string) []Glyph {
	out := make([]Glyph, 0, len(s))
	for _, r := range s {
		if r < 0x20 || hidden(r) {
			panic(fmt.Sprintf("display: unprintable %U in program text %q", r, s))
		}
		out = append(out, Glyph{R: r, W: int8(RuneWidth(r))})
	}
	return out
}
