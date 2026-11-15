// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package diff

import (
	"unicode"
	"unicode/utf8"
)

// MaxInline is the longest line, in characters, whose changes are found
// inside it. A longer changed line, such as a minified file on one line,
// is marked as changed whole, since comparing it would take too long.
const MaxInline = 10000

// maxCells bounds the work of comparing the differing middle of two lines:
// a middle with more token pairs than this is marked as changed whole.
const maxCells = 4 << 20

// Span is a range of bytes, Start to End.
type Span struct {
	Start, End int
}

// Inline returns the bytes of a and b that changed: the tokens (words,
// runs of spaces, and single other characters) not in the longest common
// subsequence of the two lines' tokens. A line over MaxInline characters
// is changed whole.
func Inline(a, b []byte) ([]Span, []Span) {
	if utf8.RuneCount(a) > MaxInline || utf8.RuneCount(b) > MaxInline {
		return whole(a), whole(b)
	}
	ta, tb := tokens(a), tokens(b)
	// Trim the common start and end, which is most of a typical line.
	pre := 0
	for pre < len(ta) && pre < len(tb) && token(a, ta[pre]) == token(b, tb[pre]) {
		pre++
	}
	suf := 0
	for suf < len(ta)-pre && suf < len(tb)-pre &&
		token(a, ta[len(ta)-1-suf]) == token(b, tb[len(tb)-1-suf]) {
		suf++
	}
	ma, mb := ta[pre:len(ta)-suf], tb[pre:len(tb)-suf]
	keepA, keepB := make([]bool, len(ma)), make([]bool, len(mb))
	if len(ma) > 0 && len(mb) > 0 && len(ma)*len(mb) <= maxCells {
		lcs(a, b, ma, mb, keepA, keepB)
	}
	return changed(ma, keepA), changed(mb, keepB)
}

func whole(s []byte) []Span {
	if len(s) == 0 {
		return nil
	}
	return []Span{{0, len(s)}}
}

func token(s []byte, sp Span) string {
	return string(s[sp.Start:sp.End])
}

type class int

const (
	classWord class = iota
	classSpace
	classOther
)

func classOf(r rune) class {
	switch {
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return classWord
	case r == ' ' || r == '\t':
		return classSpace
	}
	return classOther
}

// tokens splits s into words, runs of spaces and tabs, and single other
// characters.
func tokens(s []byte) []Span {
	var out []Span
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRune(s[i:])
		c := classOf(r)
		j := i + size
		if c != classOther {
			for j < len(s) {
				r2, size2 := utf8.DecodeRune(s[j:])
				if classOf(r2) != c {
					break
				}
				j += size2
			}
		}
		out = append(out, Span{i, j})
		i = j
	}
	return out
}

// lcs marks the tokens of a longest common subsequence of ma and mb.
func lcs(a, b []byte, ma, mb []Span, keepA, keepB []bool) {
	n, m := len(ma), len(mb)
	// table[i][j] is the LCS length of ma[i:] and mb[j:].
	table := make([]int32, (n+1)*(m+1))
	at := func(i, j int) *int32 { return &table[i*(m+1)+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if token(a, ma[i]) == token(b, mb[j]) {
				*at(i, j) = *at(i+1, j+1) + 1
			} else {
				*at(i, j) = max(*at(i+1, j), *at(i, j+1))
			}
		}
	}
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case token(a, ma[i]) == token(b, mb[j]):
			keepA[i], keepB[j] = true, true
			i++
			j++
		case *at(i+1, j) >= *at(i, j+1):
			i++
		default:
			j++
		}
	}
}

// changed returns the spans of tokens not kept, joining neighbours.
func changed(toks []Span, keep []bool) []Span {
	var out []Span
	for i, t := range toks {
		if keep[i] {
			continue
		}
		if len(out) > 0 && out[len(out)-1].End == t.Start {
			out[len(out)-1].End = t.End
		} else {
			out = append(out, t)
		}
	}
	return out
}
