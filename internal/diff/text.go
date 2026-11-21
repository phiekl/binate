// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package diff compares two texts line by line for the diff view: it loads
// them, finds the line operations (lines.go, copied from Go), aligns them
// into side-by-side rows, and finds the changes inside changed lines.
package diff

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// MaxSize is the largest side the diff view opens.
const MaxSize = 10 << 20

var (
	// ErrTooLarge is returned for a side over MaxSize.
	ErrTooLarge = fmt.Errorf("larger than %d MiB", MaxSize>>20)
	// ErrBinary is returned for a side that is not valid UTF-8.
	ErrBinary = errors.New("not UTF-8 text")
)

// Read reads a side, at most MaxSize bytes of UTF-8.
func Read(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxSize {
		return nil, ErrTooLarge
	}
	if !utf8.Valid(data) {
		return nil, ErrBinary
	}
	return data, nil
}

// Text is a side split into lines.
type Text struct {
	// Lines are the lines without their line ends.
	Lines [][]byte
	// CRLF is set if every line ends with CR+LF, which then doesn't show.
	// Otherwise, a CR before a LF stays in the line, and shows as \r.
	CRLF bool
	// NoFinalNewline is set if the last line has no line end.
	NoFinalNewline bool
}

// Split splits data into lines. A LF ends a line, and so does CR+LF in a
// text that uses CR+LF throughout.
func Split(data []byte) Text {
	var t Text
	if len(data) == 0 {
		return t
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	if len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	} else {
		t.NoFinalNewline = true
	}
	t.CRLF = true
	for i, l := range lines {
		ended := i < len(lines)-1 || !t.NoFinalNewline
		if ended && !bytes.HasSuffix(l, []byte("\r\n")) {
			t.CRLF = false
			break
		}
	}
	if t.NoFinalNewline && len(lines) == 1 {
		t.CRLF = false
	}
	t.Lines = make([][]byte, len(lines))
	for i, l := range lines {
		l = bytes.TrimSuffix(l, []byte("\n"))
		if t.CRLF && (i < len(lines)-1 || !t.NoFinalNewline) {
			l = bytes.TrimSuffix(l, []byte("\r"))
		}
		t.Lines[i] = l
	}
	return t
}

// keys returns the lines as compared: the last line without a line end
// differs from the same text with one. It is marked with a LF, which no
// line holds, so it can't equal any line that has one.
func (t Text) keys() []string {
	keys := make([]string, len(t.Lines))
	for i, l := range t.Lines {
		keys[i] = string(l)
	}
	if t.NoFinalNewline && len(keys) > 0 {
		keys[len(keys)-1] += "\n"
	}
	return keys
}

// RowKind is what a side-by-side row shows.
type RowKind uint8

const (
	Same      RowKind = iota // the same line on both sides
	Changed                  // a line on each side, paired as changed
	LeftOnly                 // a line on the left side only
	RightOnly                // a line on the right side only
)

// Mark returns the character shown for a row between the sides.
func (k RowKind) Mark() rune {
	return [...]rune{' ', '|', '-', '+'}[k]
}

// Row is a side-by-side row: a line index on each side, or -1.
type Row struct {
	Kind RowKind
	L, R int
}

// Compare aligns the lines of two texts into side-by-side rows. In each
// changed block, lines are paired as changed in order, and the lines left
// over on the longer side are one-sided.
func Compare(l, r Text) []Row {
	var rows []Row
	ops := Lines(l.keys(), r.keys())
	for i := 0; i < len(ops); i++ {
		op := ops[i]
		switch op.Kind {
		case Equal:
			for k := 0; k < op.X1-op.X0; k++ {
				rows = append(rows, Row{Kind: Same, L: op.X0 + k, R: op.Y0 + k})
			}
			continue
		}
		// A block: deleted lines, then inserted ones.
		x0, x1, y0, y1 := op.X0, op.X1, op.Y0, op.Y1
		if op.Kind == Delete && i+1 < len(ops) && ops[i+1].Kind == Insert {
			y0, y1 = ops[i+1].Y0, ops[i+1].Y1
			i++
		}
		n, m := x1-x0, y1-y0
		for k := 0; k < max(n, m); k++ {
			switch {
			case k < n && k < m:
				rows = append(rows, Row{Kind: Changed, L: x0 + k, R: y0 + k})
			case k < n:
				rows = append(rows, Row{Kind: LeftOnly, L: x0 + k, R: -1})
			default:
				rows = append(rows, Row{Kind: RightOnly, L: -1, R: y0 + k})
			}
		}
	}
	return rows
}
