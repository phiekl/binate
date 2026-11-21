// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/diff"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/stat"
	"pxy.se/go/binate/internal/term"
)

func newDiff(l, r string) *DiffView {
	v := &DiffView{Styles: colors.Default(), Left: "a.txt", Right: "b.txt", Context: 1}
	v.SetTexts(diff.Split([]byte(l)), diff.Split([]byte(r)))
	return v
}

func TestDiffScreen(t *testing.T) {
	v := newDiff("one\ntwo\nthree\nfour\nfive\nsix\n", "one\ntwo\nTHREE\nfour\nfive\nsix\nseven\n")
	// Tall enough for the panels below the 7 lines.
	scr := term.NewScreen(54, 22)
	v.Draw(scr)
	want := `       ··· 1 ···                 ··· 1 ···
2 two                     2 two
3 three                 | 3 THREE
4 four                    4 four
       ··· 1 ···                 ··· 1 ···
6 six                     6 six
                        + 7 seven
`
	if got := panes(scr); got != want {
		t.Errorf("panes:\n%s\nwant:\n%s", got, want)
	}
	if top := strings.Split(scr.String(), "\n")[0]; !strings.HasPrefix(top, "┌ (viewing) a.txt ─") || !strings.Contains(top, "┐   ┌ (viewing) b.txt ─") {
		t.Errorf("titles %q", top)
	}
	if status := lastLine(scr); !strings.HasPrefix(status, " [1 changed] [1 right]") {
		t.Errorf("status %q", status)
	}
	// Differing lines are tinted by side, red on the left and green on the
	// right; the changed word is highlighted more strongly.
	st := v.Styles
	left, right := st.Plain.On(st.LeftBg), st.Plain.On(st.RightBg)
	for _, tt := range []struct {
		x, y int
		want term.Style
	}{
		{3, 3, left.On(st.LeftInline)},
		{31, 3, right.On(st.RightInline)},
		{1, 3, st.Plain.With(term.Dim).On(st.LeftBg)}, // the line number
		{8, 3, left},
		{30, 3, right},
		{26, 3, st.Changed}, // the mark
		{2, 2, st.Plain},
		{1, 7, st.Plain}, // opposite a right-only line
		{26, 7, st.RightOnly},
		{35, 7, right},
	} {
		if c := scr.Cell(tt.x, tt.y); c.Style != tt.want {
			t.Errorf("column %d row %d %q %+v, want %+v", tt.x, tt.y, c.R, c.Style, tt.want)
		}
	}

	v.Full = true
	scr.Clear()
	v.Draw(scr)
	if got := panes(scr); !strings.HasPrefix(got, "1 one                     1 one\n") {
		t.Errorf("full view:\n%s", got)
	}
}

func TestDiffEscapesInChangedLines(t *testing.T) {
	v := newDiff("a\x1bb\n", "a\tb\n")
	scr := term.NewScreen(54, 5)
	v.Draw(scr)
	line := strings.Split(panes(scr), "\n")[0]
	if !strings.HasPrefix(line, `1 a\x1bb`) || !strings.Contains(line, "| 1 a   b") {
		t.Fatalf("line %q", line)
	}
	for x := 4; x < 8; x++ {
		if c := scr.Cell(x, 1); c.Style != v.Styles.Escape.On(v.Styles.LeftBg) {
			t.Errorf("column %d %+v, want the escape style", x, c)
		}
	}
	// The expanded tab is changed text, not an escape.
	if c := scr.Cell(32, 1); c.Style != v.Styles.Plain.On(v.Styles.RightBg).On(v.Styles.RightInline) {
		t.Errorf("tab column %+v", c)
	}
}

func TestDiffWrapAndScroll(t *testing.T) {
	long := strings.Repeat("abcdefghij", 5)
	v := newDiff(long+"\n", long+"X\n")
	scr := term.NewScreen(54, 8)
	v.Draw(scr)
	if got := strings.Split(panes(scr), "\n")[0]; got != "1 abcdefghijabcdefghija | 1 abcdefghijabcdefghija" {
		t.Errorf("unwrapped %q", got)
	}
	v.ScrollSideways(40)
	scr.Clear()
	v.Draw(scr)
	if got := strings.Split(panes(scr), "\n")[0]; got != "1 abcdefghij            | 1 abcdefghijX" {
		t.Errorf("scrolled %q", got)
	}
	v.Wrap, v.HScroll = true, 0
	scr.Clear()
	v.Draw(scr)
	want := `1 abcdefghijabcdefghija | 1 abcdefghijabcdefghija
  bcdefghijabcdefghijab     bcdefghijabcdefghijab
  cdefghij                  cdefghijX
`
	if got := panes(scr); !strings.HasPrefix(got, want) {
		t.Errorf("wrapped:\n%s\nwant:\n%s", got, want)
	}
}

func TestDiffWrapKeepsWideCharacters(t *testing.T) {
	line := strings.Repeat("a", 17) + "界z"
	v := newDiff("x\n", line+"\n")
	v.Wrap = true
	scr := term.NewScreen(47, 8)
	v.Draw(scr)
	lines := strings.Split(panes(scr), "\n")
	if !strings.HasSuffix(lines[0], "1 "+strings.Repeat("a", 17)) || !strings.HasSuffix(lines[1], "  界z") {
		t.Errorf("wrapped:\n%s", scr.String())
	}
	for _, tt := range []struct {
		line  string
		width int
		want  []int
	}{
		{"", 4, []int{0}},
		{"abcd", 4, []int{0}},
		{"abcde", 4, []int{0, 4}},
		{"abc界", 4, []int{0, 3}},
		{"界界界", 4, []int{0, 4}},
		{"界a", 1, []int{0, 2}},
	} {
		var got []int
		for _, p := range display.ContentWraps([]byte(tt.line), tt.width) {
			got = append(got, p.Column())
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("ContentWraps(%q, %d) = %v, want %v", tt.line, tt.width, got, tt.want)
		}
	}
}

func TestDiffHugeContext(t *testing.T) {
	v := newDiff("a\nb\nc\n", "a\nB\nc\n")
	v.Context = math.MaxInt
	scr := term.NewScreen(54, 8)
	v.Draw(scr)
	if got := panes(scr); !strings.HasPrefix(got, "1 a") || !strings.Contains(got, "| 2 B") {
		t.Errorf("screen:\n%s", got)
	}
}

func TestDiffContextLayout(t *testing.T) {
	for _, tt := range []struct {
		name    string
		context int
		changes []int
		want    string
	}{
		{"zero", 0, []int{2, 5}, "skip2 2 skip2 5 skip6"},
		{"negative", -1, []int{2, 5}, "skip2 2 skip2 5 skip6"},
		{"adjacent", 1, []int{2, 5}, "skip1 1 2 3 4 5 6 skip5"},
		{"overlap", 2, []int{3, 5}, "skip1 1 2 3 4 5 6 7 skip4"},
		{"separate", 1, []int{2, 8}, "skip1 1 2 3 skip3 7 8 9 skip2"},
		{"edges", 2, []int{0, 11}, "0 1 2 skip6 9 10 11"},
		{"huge", math.MaxInt, []int{2, 5}, "0 1 2 3 4 5 6 7 8 9 10 11"},
		{"identical", 0, nil, "0 1 2 3 4 5 6 7 8 9 10 11"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := &DiffView{Context: tt.context, Rows: make([]diff.Row, 12)}
			for _, i := range tt.changes {
				v.Rows[i].Kind = diff.Changed
			}
			var got []string
			for _, line := range v.layout(100) {
				if line.row < 0 {
					got = append(got, fmt.Sprintf("skip%d", line.skip))
				} else {
					got = append(got, fmt.Sprint(line.row))
				}
			}
			if got := strings.Join(got, " "); got != tt.want {
				t.Errorf("layout = %s; want %s", got, tt.want)
			}
		})
	}
}

func BenchmarkDiffContextLayout(b *testing.B) {
	for _, n := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			v := &DiffView{Context: n, Rows: make([]diff.Row, n)}
			for i := range v.Rows {
				v.Rows[i].Kind = diff.Changed
			}
			b.ReportAllocs()
			for b.Loop() {
				v.lines = nil
				v.layout(100)
			}
		})
	}
}

func TestDiffJumps(t *testing.T) {
	var l, r strings.Builder
	for i := range 100 {
		fmt.Fprintf(&l, "line %d\n", i)
		if i == 10 || i == 50 || i == 51 || i == 90 {
			fmt.Fprintf(&r, "changed %d\n", i)
		} else {
			fmt.Fprintf(&r, "line %d\n", i)
		}
	}
	v := newDiff(l.String(), r.String())
	v.Full = true
	scr := term.NewScreen(64, 12)
	v.Draw(scr)
	var tops []int
	for v.Jump(true) {
		tops = append(tops, v.Top)
	}
	if fmt.Sprint(tops) != "[8 48 88]" {
		t.Errorf("forward jumps put the top at %v", tops)
	}
	tops = nil
	for v.Jump(false) {
		tops = append(tops, v.Top)
	}
	if fmt.Sprint(tops) != "[48 8]" {
		t.Errorf("backward jumps put the top at %v", tops)
	}
	// Scrolled by hand to where a jump would put a block, the jumps go on
	// to the blocks around it, rather than staying.
	for _, forward := range []bool{false, true} {
		v.Top, v.hasJump = 48, false
		if !v.Jump(forward) || v.Top == 48 {
			t.Errorf("jump forward %v from a block shown as jumped to: top %d", forward, v.Top)
		}
	}
}

func TestDiffKeepsPositionOnReload(t *testing.T) {
	var l strings.Builder
	for i := range 100 {
		fmt.Fprintf(&l, "line %d\n", i)
	}
	v := newDiff(l.String(), l.String())
	v.Full = true
	scr := term.NewScreen(64, 12)
	v.Draw(scr)
	v.Scroll(40)
	v.SetTexts(diff.Split([]byte("new first line\n"+l.String())), diff.Split([]byte(l.String())))
	scr.Clear()
	v.Draw(scr)
	if got := strings.Split(panes(scr), "\n")[0]; !strings.Contains(got, "line 40") {
		t.Errorf("top line after the reload %q", got)
	}
}

func TestDiffMessage(t *testing.T) {
	v := &DiffView{Styles: colors.Default(), Left: "a", Right: "b", Message: "Unable to compare files. Something went wrong."}
	scr := term.NewScreen(50, 7)
	v.Draw(scr)
	for _, want := range []string{"Unable to compare", "files. Something went", "wrong."} {
		if !strings.Contains(scr.String(), want) {
			t.Errorf("screen without %q:\n%s", want, scr.String())
		}
	}
}

func TestDiffSideErrors(t *testing.T) {
	st := colors.Default()
	const binary = "Unable to show data that is not UTF-8 encoded."
	for _, tt := range []struct {
		errors [2]string
		want   []string
	}{
		{[2]string{binary, ""}, []string{
			pair("Unable to show data that is not", "Unable to compare files due to an"),
			pair("UTF-8 encoded.", "error on the left side."),
		}},
		{[2]string{"", binary}, []string{
			pair("Unable to compare files due to an", "Unable to show data that is not UTF-8"),
			pair("error on the right side.", "encoded."),
		}},
		{[2]string{binary, "Failed reading this file (permission denied)."}, []string{
			pair("Unable to show data that is not", "Failed reading this file (permission"),
			pair("UTF-8 encoded.", "denied)."),
		}},
	} {
		v := &DiffView{Styles: st, Left: "a", Right: "b", SideErrors: tt.errors}
		scr := term.NewScreen(80, 10)
		v.Draw(scr)
		got := strings.Split(panes(scr), "\n")[:2]
		if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
			t.Errorf("%q:\n%s\nwant:\n%s", tt.errors, strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
		}
		// Only a side that failed is red.
		p := Layout(80)
		for i, x := range []int{p.LeftX, p.RightX} {
			if red := scr.Cell(x, 1).Style == st.Error; red != (tt.errors[i] != "") {
				t.Errorf("%q: side %d red %v", tt.errors, i, red)
			}
		}
		if status := lastLine(scr); strings.Contains(status, "no differences") {
			t.Errorf("status %q", status)
		}
	}
}

func TestDiffPanelsFollowCompare(t *testing.T) {
	l := &stat.Stat{Mode: unix.S_IFREG | 0o644, Mtime: time.Unix(1e9, 0)}
	r := &stat.Stat{Mode: unix.S_IFREG | 0o644, Mtime: time.Unix(2e9, 0)}
	for _, tt := range []struct {
		compare scan.Attrs
		marked  bool
	}{{0, false}, {scan.DefaultAttrs | scan.Mtime, true}} {
		v := newDiff("one\n", "one\n")
		v.Stats = [2]StatSide{{Stat: l, Entries: -1}, {Stat: r, Entries: -1}}
		v.Compare = tt.compare
		scr := term.NewScreen(80, 30)
		v.Draw(scr)
		if got := strings.Contains(scr.String(), "#"); got != tt.marked {
			t.Errorf("compare %v: mtime marked %v, want %v\n%s", tt.compare, got, tt.marked, scr.String())
		}
	}
}
