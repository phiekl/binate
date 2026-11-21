// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package diff

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// layout shows rows as "mark left right" with 1-based line numbers.
func layout(rows []Row) string {
	var b strings.Builder
	for _, r := range rows {
		num := func(i int) string {
			if i < 0 {
				return "."
			}
			return fmt.Sprint(i + 1)
		}
		fmt.Fprintf(&b, "%c %s %s\n", r.Kind.Mark(), num(r.L), num(r.R))
	}
	return b.String()
}

func TestCompare(t *testing.T) {
	for _, tt := range []struct {
		name, l, r, want string
	}{
		{"same", "a\nb\n", "a\nb\n", "  1 1\n  2 2\n"},
		{"changed line", "a\nb\nc\n", "a\nB\nc\n", "  1 1\n| 2 2\n  3 3\n"},
		{"inserted", "a\nc\n", "a\nb\nc\n", "  1 1\n+ . 2\n  2 3\n"},
		{"deleted", "a\nb\nc\n", "a\nc\n", "  1 1\n- 2 .\n  3 2\n"},
		{"block longer on the left", "a\nx\ny\nz\nc\n", "a\nX\nc\n", "  1 1\n| 2 2\n- 3 .\n- 4 .\n  5 3\n"},
		{"block longer on the right", "a\nx\nc\n", "a\nX\nY\nc\n", "  1 1\n| 2 2\n+ . 3\n  3 4\n"},
		{"empty left", "", "a\nb\n", "+ . 1\n+ . 2\n"},
		{"empty right", "a\n", "", "- 1 .\n"},
		{"both empty", "", "", ""},
		{"missing final newline", "a\nb", "a\nb\n", "  1 1\n| 2 2\n"},
		{"missing final newline, not text", "x", "x\x00no newline\n", "| 1 1\n"},
		{"missing final newline on both", "a\nb", "a\nb", "  1 1\n  2 2\n"},
		{"anchored on unique lines", "}\nfoo\n}\n", "}\nbar\n}\nfoo\n}\n", "  1 1\n+ . 2\n+ . 3\n  2 4\n  3 5\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := layout(Compare(Split([]byte(tt.l)), Split([]byte(tt.r))))
			if got != tt.want {
				t.Errorf("rows:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestSplitLineEnds(t *testing.T) {
	for _, tt := range []struct {
		name, in string
		lines    []string
		crlf     bool
		noFinal  bool
	}{
		{"lf", "a\nb\n", []string{"a", "b"}, false, false},
		{"crlf throughout", "a\r\nb\r\n", []string{"a", "b"}, true, false},
		{"crlf without final newline", "a\r\nb", []string{"a", "b"}, true, true},
		{"mixed keeps cr", "a\r\nb\n", []string{"a\r", "b"}, false, false},
		{"lone cr", "a\rb\n", []string{"a\rb"}, false, false},
		{"cr at the end", "a\r", []string{"a\r"}, false, true},
		{"empty", "", nil, false, false},
		{"empty line", "\n", []string{""}, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Split([]byte(tt.in))
			var lines []string
			for _, l := range got.Lines {
				lines = append(lines, string(l))
			}
			if strings.Join(lines, "|") != strings.Join(tt.lines, "|") || len(lines) != len(tt.lines) ||
				got.CRLF != tt.crlf || got.NoFinalNewline != tt.noFinal {
				t.Errorf("Split(%q) = %q crlf %v no final %v", tt.in, lines, got.CRLF, got.NoFinalNewline)
			}
		})
	}
}

func TestRead(t *testing.T) {
	if _, err := Read(bytes.NewReader([]byte("ok\n"))); err != nil {
		t.Errorf("text: %v", err)
	}
	if _, err := Read(bytes.NewReader([]byte("a\xffb"))); !errors.Is(err, ErrBinary) {
		t.Errorf("binary: %v", err)
	}
	if _, err := Read(bytes.NewReader(make([]byte, MaxSize))); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	if _, err := Read(bytes.NewReader(make([]byte, MaxSize+1))); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over the limit: %v", err)
	}
}

// marked shows a line with its changed spans in brackets.
func marked(s string, spans []Span) string {
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(s[last:sp.Start] + "[" + s[sp.Start:sp.End] + "]")
		last = sp.End
	}
	b.WriteString(s[last:])
	return b.String()
}

func TestInline(t *testing.T) {
	for _, tt := range []struct {
		a, b, wantA, wantB string
	}{
		{"return x + 1", "return x + 2", "return x + [1]", "return x + [2]"},
		{"foo(a, b)", "foo(a, c, b)", "foo(a, b)", "foo(a, [c, ]b)"},
		{"same", "same", "same", "same"},
		{"", "new", "", "[new]"},
		{"old words here", "new words there", "[old] words [here]", "[new] words [there]"},
		{"tab\there", "tab  here", "tab[\t]here", "tab[  ]here"},
		{"日本語 text", "日本 text", "[日本語] text", "[日本] text"},
		{"\x1b[0m", "[0m", "[\x1b][0m", "[0m"},
	} {
		sa, sb := Inline([]byte(tt.a), []byte(tt.b))
		if got := marked(tt.a, sa); got != tt.wantA {
			t.Errorf("Inline(%q, %q) left %q, want %q", tt.a, tt.b, got, tt.wantA)
		}
		if got := marked(tt.b, sb); got != tt.wantB {
			t.Errorf("Inline(%q, %q) right %q, want %q", tt.a, tt.b, got, tt.wantB)
		}
	}
}

func TestInlineLengthCap(t *testing.T) {
	long := strings.Repeat("ab ", MaxInline/3+1)
	a, b := long+"x", long+"y"
	sa, sb := Inline([]byte(a), []byte(b))
	if len(sa) != 1 || sa[0] != (Span{0, len(a)}) || len(sb) != 1 || sb[0] != (Span{0, len(b)}) {
		t.Errorf("a line over the cap: %v %v, want changed whole", sa, sb)
	}
	// Just under the cap, the change is found inside the line.
	short := strings.Repeat("ab ", (MaxInline-2)/3)
	sa, _ = Inline([]byte(short+"x"), []byte(short+"y"))
	if len(sa) != 1 || sa[0].Start != len(short) {
		t.Errorf("a line under the cap: %v", sa)
	}
}

func FuzzLines(f *testing.F) {
	f.Add("a\nb\nc\n", "a\nB\nc\nd\n")
	f.Add("", "x")
	f.Add("}\n}\n}\n", "}\nx\n}\n")
	f.Fuzz(func(t *testing.T, l, r string) {
		lt, rt := Split([]byte(l)), Split([]byte(r))
		rows := Compare(lt, rt)
		// Every line appears once, in order, and same rows match.
		nl, nr := 0, 0
		for _, row := range rows {
			if row.L >= 0 {
				if row.L != nl {
					t.Fatalf("left line %d out of order", row.L)
				}
				nl++
			}
			if row.R >= 0 {
				if row.R != nr {
					t.Fatalf("right line %d out of order", row.R)
				}
				nr++
			}
			if row.Kind == Same && string(lt.Lines[row.L]) != string(rt.Lines[row.R]) {
				t.Fatalf("same row of different lines %q, %q", lt.Lines[row.L], rt.Lines[row.R])
			}
		}
		if nl != len(lt.Lines) || nr != len(rt.Lines) {
			t.Fatalf("rows cover %d and %d lines of %d and %d", nl, nr, len(lt.Lines), len(rt.Lines))
		}
	})
}

func FuzzInline(f *testing.F) {
	f.Add("return x + 1", "return y + 1")
	f.Add("", "a")
	f.Add("\xff", "é")
	f.Fuzz(func(t *testing.T, a, b string) {
		sa, sb := Inline([]byte(a), []byte(b))
		for _, c := range []struct {
			s     string
			spans []Span
		}{{a, sa}, {b, sb}} {
			last := 0
			for _, sp := range c.spans {
				if sp.Start < last || sp.End <= sp.Start || sp.End > len(c.s) {
					t.Fatalf("bad span %v in %q", sp, c.s)
				}
				last = sp.End
			}
		}
		// What isn't changed is the same on both sides.
		keep := func(s string, spans []Span) string {
			var b strings.Builder
			last := 0
			for _, sp := range spans {
				b.WriteString(s[last:sp.Start])
				last = sp.End
			}
			return b.String() + s[last:]
		}
		if ka, kb := keep(a, sa), keep(b, sb); ka != kb {
			t.Fatalf("kept %q and %q differ", ka, kb)
		}
	})
}

func TestCount(t *testing.T) {
	for _, tt := range []struct {
		a, b     string
		add, del int
	}{
		{"", "", 0, 0},
		{"", "a\nb\n", 2, 0},
		{"a\nb\n", "", 0, 2},
		{"a\nb\nc\n", "a\nB\nc\n", 1, 1},
		{"a", "a\n", 1, 1},
		{"a\nb\nc\nd\n", "b\nc\nd\na\n", 1, 1},
		{"x\ny\nx\ny\n", "y\nx\ny\nx\n", 1, 1},
		{"a\n\n}\n\nb\n", "c\n\n}\n\nd\n", 2, 2},
	} {
		add, del := Count([]byte(tt.a), []byte(tt.b))
		if add != tt.add || del != tt.del {
			t.Errorf("Count(%q, %q) = +%d -%d, want +%d -%d", tt.a, tt.b, add, del, tt.add, tt.del)
		}
	}
}

func FuzzCount(f *testing.F) {
	f.Add([]byte("a\nb\nc\n"), []byte("a\nc\nd\n"))
	f.Fuzz(func(t *testing.T, a, b []byte) {
		x, y := splitLines(a), splitLines(b)
		add, del := Count(a, b)
		// No more than the anchored diff's, and what remains the same on
		// each side as many lines.
		common := 0
		for _, op := range Lines(x, y) {
			if op.Kind == Equal {
				common += op.X1 - op.X0
			}
		}
		if len(x)-del != len(y)-add || len(y)-add < common {
			t.Errorf("Count(%q, %q) = +%d -%d, more than the anchored diff's", a, b, add, del)
		}
		// Exactly the fewest, as a longest common subsequence found by
		// dynamic programming has.
		if len(x)*len(y) <= 10000 {
			dp := make([][]int, len(x)+1)
			for i := range dp {
				dp[i] = make([]int, len(y)+1)
			}
			for i := len(x) - 1; i >= 0; i-- {
				for j := len(y) - 1; j >= 0; j-- {
					if x[i] == y[j] {
						dp[i][j] = dp[i+1][j+1] + 1
					} else {
						dp[i][j] = max(dp[i+1][j], dp[i][j+1])
					}
				}
			}
			if len(y)-add != dp[0][0] {
				t.Errorf("Count(%q, %q) = +%d -%d, keeping %d lines, not %d", a, b, add, del, len(y)-add, dp[0][0])
			}
		}
	})
}
