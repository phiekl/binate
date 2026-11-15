// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package term

import (
	"bytes"
	"strings"
	"testing"

	"pxy.se/go/binate/internal/display"
)

func TestParseKey(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want Key
		n    int
	}{
		{"j", Rune('j'), 1},
		{" ", Rune(' '), 1},
		{"é", Rune('é'), 2},
		{"日x", Rune('日'), 3},
		{"\r", Key{Code: KeyEnter}, 1},
		{"\n", Key{Code: KeyEnter}, 1},
		{"\x7f", Key{Code: KeyBackspace}, 1},
		{"\x08", Key{Code: KeyBackspace}, 1},
		{"\t", Key{Code: KeyTab}, 1},
		{"\x02", Ctrl('b'), 1},
		{"\x03", Ctrl('c'), 1},
		{"\x06", Ctrl('f'), 1},
		{"\x1a", Ctrl('z'), 1},
		{"\x00", Key{Code: KeyUnknown}, 1},
		{"\x1c", Key{Code: KeyUnknown}, 1},
		{"\xff", Key{Code: KeyUnknown}, 1},
		{"\x1b[A", Key{Code: KeyUp}, 3},
		{"\x1b[B", Key{Code: KeyDown}, 3},
		{"\x1b[C", Key{Code: KeyRight}, 3},
		{"\x1b[D", Key{Code: KeyLeft}, 3},
		{"L", Rune('L'), 1},
		{"\x1b[1;2D", Key{Code: KeyLeft, Shift: true}, 6},
		{"\x1b[1;2C", Key{Code: KeyRight, Shift: true}, 6},
		{"\x1b[1;4D", Key{Code: KeyLeft, Alt: true, Shift: true}, 6},
		{"\x1bOA", Key{Code: KeyUp}, 3},
		{"\x1bOD", Key{Code: KeyLeft}, 3},
		{"\x1b[H", Key{Code: KeyHome}, 3},
		{"\x1b[F", Key{Code: KeyEnd}, 3},
		{"\x1bOH", Key{Code: KeyHome}, 3},
		{"\x1bOF", Key{Code: KeyEnd}, 3},
		{"\x1b[1~", Key{Code: KeyHome}, 4},
		{"\x1b[7~", Key{Code: KeyHome}, 4},
		{"\x1b[4~", Key{Code: KeyEnd}, 4},
		{"\x1b[8~", Key{Code: KeyEnd}, 4},
		{"\x1b[3~", Key{Code: KeyDelete}, 4},
		{"\x1b[5~", Key{Code: KeyPgUp}, 4},
		{"\x1b[6~", Key{Code: KeyPgDn}, 4},
		{"\x1b[2~", Key{Code: KeyUnknown}, 4},
		{"\x1b[1;3A", Key{Code: KeyUp, Alt: true}, 6},
		{"\x1b[1;3B", Key{Code: KeyDown, Alt: true}, 6},
		{"\x1b[1;5A", Key{Code: KeyUp}, 6},
		{"\x1b[1;7B", Key{Code: KeyDown, Alt: true}, 6},
		{"\x1b\x1b[A", Key{Code: KeyUp, Alt: true}, 4},
		{"\x1b\x1b[B", Key{Code: KeyDown, Alt: true}, 4},
		{"\x1bj", Key{Code: KeyRune, Rune: 'j', Alt: true}, 2},
		{"\x1b\r", Key{Code: KeyEnter, Alt: true}, 2},
		{"\x1b[Z", Key{Code: KeyUnknown}, 3},
		{"\x1b[200~", Key{Code: KeyUnknown}, 6},
		{"\x1b[?1;2c", Key{Code: KeyUnknown}, 7},
		// A byte that can't continue a sequence ends it.
		{"\x1b[1\x01", Key{Code: KeyUnknown}, 3},
	} {
		got, n := ParseKey([]byte(tt.in), false)
		if got != tt.want || n != tt.n {
			t.Errorf("ParseKey(%q) = %v, %d; want %v, %d", tt.in, got, n, tt.want, tt.n)
		}
	}
}

func TestParseKeyWaits(t *testing.T) {
	for _, tt := range []struct {
		in        string
		finalWant Key
		finalN    int
	}{
		{"\x1b", Key{Code: KeyEsc}, 1},
		{"\x1b[", Key{Code: KeyUnknown}, 2},
		{"\x1b[1;3", Key{Code: KeyUnknown}, 5},
		{"\x1b[1;2", Key{Code: KeyUnknown}, 5},
		{"\x1bO", Key{Code: KeyRune, Rune: 'O', Alt: true}, 2},
		{"\x1b\x1b", Key{Code: KeyEsc}, 1},
		{"\x1b\x1b[", Key{Code: KeyEsc}, 1},
		{"\xc3", Key{Code: KeyUnknown}, 1},
		{"\xe6\x97", Key{Code: KeyUnknown}, 1},
		{"\x1b\xc3", Key{Code: KeyEsc}, 1},
	} {
		if k, n := ParseKey([]byte(tt.in), false); n != 0 {
			t.Errorf("ParseKey(%q, false) = %v, %d; want to wait", tt.in, k, n)
		}
		if k, n := ParseKey([]byte(tt.in), true); k != tt.finalWant || n != tt.finalN {
			t.Errorf("ParseKey(%q, true) = %v, %d; want %v, %d", tt.in, k, n, tt.finalWant, tt.finalN)
		}
	}
}

func TestParseKeyLongSequence(t *testing.T) {
	in := "\x1b[" + strings.Repeat("1", 100)
	if _, n := ParseKey([]byte(in), false); n == 0 || n > maxSequence {
		t.Errorf("a long sequence used %d bytes, want 1 to %d", n, maxSequence)
	}
}

func TestParseKeys(t *testing.T) {
	keys, rest := ParseKeys([]byte("ab\x1b[Aé\x1b"), false)
	want := []Key{Rune('a'), Rune('b'), {Code: KeyUp}, Rune('é')}
	if len(keys) != len(want) || string(rest) != "\x1b" {
		t.Fatalf("ParseKeys = %v, %q", keys, rest)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("key %d = %v, want %v", i, keys[i], want[i])
		}
	}
}

func FuzzParseKey(f *testing.F) {
	for _, s := range []string{"j", "\x1b", "\x1b[1;3A", "\x1b\x1b[B", "\x1bO", "é", "\xff", "\x1b[200~x"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		for _, final := range []bool{false, true} {
			if len(in) == 0 {
				return
			}
			_, n := ParseKey(in, final)
			if n < 0 || n > len(in) {
				t.Fatalf("ParseKey(%q, %v) used %d bytes", in, final, n)
			}
			if final && n == 0 {
				t.Fatalf("ParseKey(%q, true) used nothing", in)
			}
		}
		// All input is used up, once final.
		keys, rest := ParseKeys(in, false)
		more, rest := ParseKeys(rest, true)
		if len(rest) != 0 {
			t.Fatalf("ParseKeys(%q) left %q", in, rest)
		}
		_ = append(keys, more...)
	})
}

func TestFlushWritesOnlyChanges(t *testing.T) {
	s := NewScreen(10, 2)
	s.Label(0, 0, 10, "hello", Style{})
	var out bytes.Buffer
	if err := s.Flush(&out); err != nil {
		t.Fatal(err)
	}
	first := out.String()
	if !strings.HasPrefix(first, "\x1b[0m\x1b[H\x1b[2J") || !strings.Contains(first, "hello") {
		t.Errorf("first flush %q, want a full redraw", first)
	}

	out.Reset()
	s.Label(0, 0, 10, "help", Style{})
	if err := s.Flush(&out); err != nil {
		t.Fatal(err)
	}
	if want := "\x1b[?25l\x1b[1;4H\x1b[0mp\x1b[0m"; out.String() != want {
		t.Errorf("second flush %q, want %q", out.String(), want)
	}

	out.Reset()
	if err := s.Flush(&out); err != nil {
		t.Fatal(err)
	}
	if want := "\x1b[?25l\x1b[0m"; out.String() != want {
		t.Errorf("unchanged flush %q, want %q", out.String(), want)
	}
}

func TestFlushStyles(t *testing.T) {
	s := NewScreen(6, 1)
	s.Flush(&bytes.Buffer{})
	st := Style{SGR: "1;34"}
	s.Set(0, 0, 'a', 1, st)
	s.Set(1, 0, 'b', 1, st)
	s.Set(3, 0, 'c', 1, Style{Attr: Reverse | Bold})
	var out bytes.Buffer
	s.Flush(&out)
	want := "\x1b[?25l\x1b[1;1H\x1b[0;1;34mab\x1b[1;4H\x1b[0;1;7mc\x1b[0m"
	if out.String() != want {
		t.Errorf("flush %q, want %q", out.String(), want)
	}
}

func TestFlushLinks(t *testing.T) {
	s := NewScreen(8, 2)
	s.Flush(&bytes.Buffer{})
	link := Style{Link: "'/a b/%x' é"}
	s.Set(0, 0, 'a', 1, link)
	s.Set(1, 0, 'b', 1, link)
	s.Set(2, 0, 'c', 1, Style{})
	s.Set(0, 1, 'd', 1, Style{Link: "/d"})
	var out bytes.Buffer
	s.Flush(&out)
	// The link is written as it is, and ends before unlinked text and at
	// the end.
	open := "\x1b]8;;'/a b/%x' é\x1b\\"
	want := "\x1b[?25l\x1b[1;1H\x1b[0m" + open + "ab\x1b]8;;\x1b\\c" +
		"\x1b[2;1H\x1b]8;;/d\x1b\\d\x1b]8;;\x1b\\\x1b[0m"
	if out.String() != want {
		t.Errorf("flush\n%q, want\n%q", out.String(), want)
	}
	// A cell whose link changes is written again.
	s.Set(0, 1, 'd', 1, Style{Link: "/e"})
	out.Reset()
	s.Flush(&out)
	if !strings.Contains(out.String(), "\x1b]8;;/e\x1b\\d") {
		t.Errorf("changed link not written: %q", out.String())
	}
	// A link that could end the sequence or hold a control is left out,
	// as is one that is not UTF-8.
	for _, bad := range []string{"/a\x1b]8;;evil\x07", "/a\x1b\\", "/a\nb", "/a\u009b", "/a\xff"} {
		s.Set(0, 1, 'd', 1, Style{Link: bad})
		out.Reset()
		s.Flush(&out)
		if strings.Contains(out.String(), "evil") || strings.Contains(out.String(), "/a") {
			t.Errorf("unsafe link %q written: %q", bad, out.String())
		}
	}
}

func TestWideCharacters(t *testing.T) {
	s := NewScreen(5, 1)
	used := s.Text(0, 0, 5, display.Name("日本語"), Style{}, Style{})
	if used != 4 || s.String() != "日本\n" {
		t.Errorf("used %d, screen %q", used, s.String())
	}
	// Overwriting half of a wide character blanks the other half.
	s.Set(1, 0, 'x', 1, Style{})
	if s.String() != " x本\n" {
		t.Errorf("screen %q", s.String())
	}
	// A wide character doesn't fit the last column.
	s.Set(4, 0, '語', 2, Style{})
	if c := s.Cell(4, 0); c.R != ' ' || c.W != 1 {
		t.Errorf("last column %+v", c)
	}
}

func TestEscapesUseTheirStyle(t *testing.T) {
	s := NewScreen(10, 1)
	esc := Style{Attr: Reverse}
	s.Text(0, 0, 10, display.Name("a\x1bb"), Style{}, esc)
	if s.String() != `a\x1bb`+"\n" {
		t.Fatalf("screen %q", s.String())
	}
	for x := 0; x < 6; x++ {
		want := x >= 1 && x <= 4
		if got := s.Cell(x, 0).Style == esc; got != want {
			t.Errorf("column %d escape style %v, want %v", x, got, want)
		}
	}
}

func TestCursor(t *testing.T) {
	s := NewScreen(4, 2)
	s.CursorOn, s.CursorX, s.CursorY = true, 2, 1
	var out bytes.Buffer
	s.Flush(&out)
	if !strings.HasSuffix(out.String(), "\x1b[2;3H\x1b[?25h") {
		t.Errorf("flush %q, want the cursor shown at 2,3", out.String())
	}
}

func TestStyleOn(t *testing.T) {
	bg := Style{SGR: "48;5;22"}
	for _, tt := range []struct {
		s, bg, want Style
	}{
		{Style{}, bg, bg},
		{Style{SGR: "1;34"}, bg, Style{SGR: "1;34;48;5;22"}},
		{Style{SGR: "33"}, Style{}, Style{SGR: "33"}},
		{Style{Attr: Bold}, Style{Attr: Reverse}, Style{Attr: Bold | Reverse}},
	} {
		if got := tt.s.On(tt.bg); got != tt.want {
			t.Errorf("%+v.On(%+v) = %+v, want %+v", tt.s, tt.bg, got, tt.want)
		}
	}
}
