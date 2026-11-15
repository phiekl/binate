// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package display

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

// mask shows which columns of glyphs are escapes: 'e' for an escape, '.'
// for plain text, and a second '.' for the right half of a wide glyph.
func mask(glyphs []Glyph) string {
	var b strings.Builder
	for _, g := range glyphs {
		c := "."
		if g.Esc {
			c = "e"
		}
		b.WriteString(strings.Repeat(c, int(g.W)))
	}
	return b.String()
}

func TestControls(t *testing.T) {
	for c := 0; c < 0x20; c++ {
		in := string(rune(c))
		want := fmt.Sprintf(`\x%02x`, c)
		switch c {
		case '\t':
			want = `\t`
		case '\n':
			want = `\n`
		case '\r':
			want = `\r`
		}
		got := Name(in)
		if String(got) != want || mask(got) != strings.Repeat("e", len(want)) {
			t.Errorf("Name(%q) = %q (%s), want %q all escaped", in, String(got), mask(got), want)
		}
	}
	got := Name("\x7f")
	if String(got) != `\x7f` || mask(got) != "eeee" {
		t.Errorf("Name(DEL) = %q (%s)", String(got), mask(got))
	}
}

func TestC1Controls(t *testing.T) {
	for r := rune(0x80); r <= 0x9f; r++ {
		want := fmt.Sprintf(`\u%04x`, r)
		got := Name(string(r))
		if String(got) != want || mask(got) != "eeeeee" {
			t.Errorf("Name(%U) = %q (%s), want %q", r, String(got), mask(got), want)
		}
	}
}

func TestEscapes(t *testing.T) {
	for _, tt := range []struct {
		name, in, want, mask string
	}{
		{"plain", "abc", "abc", "..."},
		{"literal escape text", `\x1b[0m`, `\x1b[0m`, "......."},
		{"escape byte", "\x1b[0m", `\x1b[0m`, "eeee..."},
		{"invalid byte", "a\xffb", `a\xffb`, ".eeee."},
		{"truncated sequence", "a\xe2\x82", `a\xe2\x82`, ".eeeeeeee"},
		{"truncated at end", "\xf0\x9f\x98", `\xf0\x9f\x98`, "eeeeeeeeeeee"},
		{"lone continuation", "\x80", `\x80`, "eeee"},
		// C2 80 is U+0080 (C1), but 80 alone is an invalid byte, not C1.
		{"continuation is not C1", "\xc3\xa9\x85", `é\x85`, ".eeee"},
		{"overlong slash", "\xc0\xaf", `\xc0\xaf`, "eeeeeeee"},
		{"overlong nul", "\xe0\x80\x80", `\xe0\x80\x80`, "eeeeeeeeeeee"},
		{"surrogate", "\xed\xa0\x80", `\xed\xa0\x80`, "eeeeeeeeeeee"},
		{"above U+10FFFF", "\xf4\x90\x80\x80", `\xf4\x90\x80\x80`, strings.Repeat("e", 16)},
		{"zero width space", "a\u200bb", `a\u200bb`, ".eeeeee."},
		{"zero width joiner", "\u200d", `\u200d`, "eeeeee"},
		{"bidi override", "\u202eabc", `\u202eabc`, "eeeeee..."},
		{"bidi isolate", "\u2066", `\u2066`, "eeeeee"},
		{"byte order mark", "\ufeff", `\ufeff`, "eeeeee"},
		{"no-break space", "a\u00a0b", `a\u00a0b`, ".eeeeee."},
		{"ideographic space", "\u3000", `\u3000`, "eeeeee"},
		{"line separator", "\u2028", `\u2028`, "eeeeee"},
		{"combining acute", "e\u0301", `e\u0301`, ".eeeeee"},
		{"enclosing mark", "a\u20dd", `a\u20dd`, ".eeeeee"},
		{"variation selector", "\u2764\ufe0f", "\u2764" + `\ufe0f`, ".eeeeee"},
		{"astral format", "\U000e0001", `\U000e0001`, strings.Repeat("e", 10)},
		{"private use", "\ue000", `\ue000`, "eeeeee"},
		{"unassigned", "\U0003fffe", `\U0003fffe`, strings.Repeat("e", 10)},
		{"wide", "日本", "日本", "...."},
		{"replacement character", "�", "�", "."},
		{"backslash", `a\b`, `a\b`, "..."},
		{"space", " ", " ", "."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Name(tt.in)
			if String(got) != tt.want || mask(got) != tt.mask {
				t.Errorf("Name(%q) = %q (%s), want %q (%s)", tt.in, String(got), mask(got), tt.want, tt.mask)
			}
			if w := Width(got); w != len(tt.mask) {
				t.Errorf("Width = %d, want %d", w, len(tt.mask))
			}
		})
	}
}

func TestContentTabs(t *testing.T) {
	for _, tt := range []struct {
		in, want string
	}{
		{"\tx", "    x"},
		{"a\tx", "a   x"},
		{"ab\tx", "ab  x"},
		{"abc\tx", "abc x"},
		{"abcd\tx", "abcd    x"},
		{"\t\tx", "        x"},
		// Escapes count by their width.
		{"\x1b\tx", `\x1b    x`},
		{"\x01a\tx", `\x01a   x`},
		{"日\tx", "日  x"},
		{"a\u200b\tx", `a\u200b x`},
	} {
		got := Content([]byte(tt.in))
		if String(got) != tt.want {
			t.Errorf("Content(%q) = %q, want %q", tt.in, String(got), tt.want)
		}
		for _, g := range got {
			if g.R == ' ' && g.Esc {
				t.Errorf("Content(%q): expanded tab marked as escape", tt.in)
			}
		}
	}
}

func TestContentCR(t *testing.T) {
	// A CR the loader left in a line is a lone CR, or one in a file that
	// doesn't use CR+LF throughout.
	got := Content([]byte("a\rb\r"))
	if String(got) != `a\rb\r` || mask(got) != ".ee.ee" {
		t.Errorf("Content = %q (%s)", String(got), mask(got))
	}
}

func TestStderr(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"plain/path", "plain/path"},
		{`a\b`, `a\\b`},
		{`\x1b`, `\\x1b`},
		{"\x1b", `\x1b`},
		{"tab\there", `tab\there`},
		{"new\nline", `new\nline`},
		{"\xff", `\xff`},
		{"\u202e", `\u202e`},
		{"日本", "日本"},
	} {
		if got := Stderr(tt.in); got != tt.want {
			t.Errorf("Stderr(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPlainPanicsOnControls(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Plain accepted a control character")
		}
	}()
	Plain("a\x1bb")
}

// checkSafe fails if shown text could control the terminal or hide
// characters.
func checkSafe(t *testing.T, in []byte, out string) {
	t.Helper()
	if !utf8.ValidString(out) {
		t.Fatalf("%q: output %q is not valid UTF-8", in, out)
	}
	for _, r := range out {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("%q: output %q contains control %U", in, out, r)
		}
		if !unicode.IsPrint(r) || unicode.In(r, unicode.Mn, unicode.Me) {
			t.Fatalf("%q: output %q contains unprintable %U", in, out, r)
		}
	}
}

func checkGlyphs(t *testing.T, in []byte, glyphs []Glyph) {
	t.Helper()
	for _, g := range glyphs {
		if g.W != 1 && g.W != 2 {
			t.Fatalf("%q: glyph %U has width %d", in, g.R, g.W)
		}
		if g.Esc && (g.R > 0x7e || g.W != 1) {
			t.Fatalf("%q: escape glyph %U is not ASCII", in, g.R)
		}
	}
	checkSafe(t, in, String(glyphs))
}

var seeds = []string{
	"", "plain", "\x1b[31mred", "\t\tx", "a\r\n", "\xff\xfe", "\xed\xa0\x80",
	"e\u0301", "\u202e\u200b", "\u2764\ufe0f", "日本\t", `\x1b`, "\xc2\x85",
}

func FuzzContent(f *testing.F) {
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		checkGlyphs(t, in, Content(in))
	})
}

// checkPartial checks that ContentSeek, ContentFrom and ContentWraps show
// the columns of a line as Content does, from every column, and that the
// wrapped parts start at glyphs.
func checkPartial(t *testing.T, line []byte, width int) {
	t.Helper()
	all := Content(line)
	starts := map[int]int{} // column of each glyph, to its index
	col := 0
	for i, g := range all {
		starts[col] = i
		col += int(g.W)
	}
	for c := 0; c <= col; c++ {
		p := ContentSeek(line, c)
		got := ContentFrom(line, p, width)
		i, ok := starts[p.Column()]
		if !ok && p.Column() != col {
			t.Fatalf("%q: ContentSeek(%d) = %+v, not at a glyph", line, c, p)
		}
		if p.Column() > c || (p.Column() < c && (!ok || p.Column()+int(all[i].W) <= c)) {
			t.Fatalf("%q: ContentSeek(%d) = %+v, which doesn't cover it", line, c, p)
		}
		// At least width columns, or what is left, and perhaps the rest of
		// the last character's glyphs.
		var rest []Glyph
		if ok {
			rest = all[i:]
		}
		want := rest[:min(len(got), len(rest))]
		if len(got) > len(rest) || String(got) != String(want) || mask(got) != mask(want) ||
			Width(got) < min(width, Width(rest)) {
			t.Fatalf("%q: ContentFrom(%+v, %d) = %q, want a start of %q", line, p, width, String(got), String(rest))
		}
	}
	for _, p := range ContentWraps(line, width) {
		if _, ok := starts[p.Column()]; !ok && len(all) > 0 {
			t.Fatalf("%q: wrapped part starts at %+v, not at a glyph", line, p)
		}
	}
}

func TestContentPartial(t *testing.T) {
	for _, s := range seeds {
		for _, width := range []int{1, 2, 3, 5} {
			checkPartial(t, []byte(s), width)
		}
	}
}

func FuzzContentPartial(f *testing.F) {
	for _, s := range seeds {
		f.Add([]byte(s), 3)
	}
	f.Fuzz(func(t *testing.T, in []byte, width int) {
		if len(in) > 200 {
			in = in[:200]
		}
		checkPartial(t, in, max(1, width%8+1))
	})
}

func FuzzName(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		glyphs := Name(in)
		checkGlyphs(t, []byte(in), glyphs)
		if utf8.ValidString(in) && !strings.ContainsFunc(in, func(r rune) bool {
			return r < 0x20 || hidden(r)
		}) && String(glyphs) != in {
			t.Fatalf("Name(%q) = %q, changed printable text", in, String(glyphs))
		}
	})
}

func FuzzStderr(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		checkSafe(t, []byte(in), Stderr(in))
	})
}

func TestShellQuote(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"/tmp/a b/main.c", `'/tmp/a b/main.c'`},
		{"", `''`},
		{"it's", `'it'\''s'`},
		{`back\slash`, `'back\slash'`},
		// As bash writes them, controls and other bytes in $'...'.
		{"a\x1bb", `$'a\Eb'`},
		{"tab\there\n", `$'tab\there\n'`},
		{"q'\a\\", `$'q\'\a\\'`},
		{"\xff\xfe", `$'\377\376'`},
		{"del\x7f", `$'del\177'`},
		{"C1\u009c", `$'C1\302\234'`},
		// Unlike bash, every byte beyond ASCII is escaped, printable or not.
		{"åäö 日本", `$'\303\245\303\244\303\266 \346\227\245\346\234\254'`},
		{"zero\u200bwidth", `$'zero\342\200\213width'`},
		{"x́", `$'x\314\201'`},
	} {
		if got := ShellQuote(tt.in); got != tt.want {
			t.Errorf("ShellQuote(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// TestShellQuoteInBash checks that bash reads each quoted string back as
// it was.
func TestShellQuoteInBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	for _, s := range seeds {
		if strings.ContainsRune(s, 0) {
			continue // not in an argument
		}
		out, err := exec.Command(bash, "-c", "printf %s "+ShellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("bash read %s as %q, %v; want %q", ShellQuote(s), out, err, s)
		}
	}
}

func FuzzShellQuote(f *testing.F) {
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := ShellQuote(in)
		checkSafe(t, []byte(in), out)
		if strings.ContainsFunc(out, func(r rune) bool { return r >= 0x7f }) {
			t.Fatalf("ShellQuote(%q) = %q, beyond ASCII", in, out)
		}
	})
}
