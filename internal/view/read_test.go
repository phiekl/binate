// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"strings"
	"testing"
	"time"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/term"
)

// TestDrawRead checks the loading box shown while archives are read: the
// count and elapsed time as the scan's, a bar of how much is read, or
// the amount of what has no size, and the path being read.
func TestDrawRead(t *testing.T) {
	st := colors.Default()
	inside := func(scr *term.Screen, row int) string {
		// The box is 52 wide, centered, its text from 6 in.
		x := (scr.W - 52) / 2
		return strings.TrimRight(string([]rune(strings.Split(scr.String(), "\n")[row])[x+6:x+49]), " ")
	}
	// apart is left and right at the ends of the 43 cells of text.
	apart := func(left, right string) string {
		return left + strings.Repeat(" ", 43-len([]rune(left))-len([]rune(right))) + right
	}
	scr := term.NewScreen(80, 24)
	DrawRead(scr, st, "/home/u", "/home/u/a.tar.gz", "/home/u/src", ReadStatus{
		Elapsed: 1500 * time.Millisecond, Entries: 1234, Current: "/home/u/a.tar.gz:usr/bin/ls",
		Read: 250, Total: 1000,
	})
	text := scr.String()
	for _, want := range []string{"~/a.tar.gz", "~/src"} {
		if !strings.Contains(strings.Split(text, "\n")[0], want) {
			t.Errorf("titles %q, want %q", strings.Split(text, "\n")[0], want)
		}
	}
	// 8 high over 24 rows: from row 8, the text from 10.
	if got := inside(scr, 10); !strings.HasPrefix(got, "1,234 entries") || !strings.HasSuffix(got, "1.5s") {
		t.Errorf("count line %q", got)
	}
	// A quarter of 38 cells is 9.5: 9 full and half of one.
	if got, want := inside(scr, 11), strings.Repeat("█", 9)+"▌"+strings.Repeat("░", 28)+"  25%"; got != want {
		t.Errorf("bar %q, want %q", got, want)
	}
	if c := scr.Cell(14+6+20, 11); c.R != '░' || c.Style != st.Border.On(st.Accent) {
		t.Errorf("rest of the bar %+v", c)
	}
	if got := inside(scr, 12); got != apart("250 bytes read", "1000 bytes") {
		t.Errorf("amount line %q", got)
	}
	if got := inside(scr, 13); got != "/home/u/a.tar.gz:usr/bin/ls" {
		t.Errorf("path line %q", got)
	}

	// Without a total, 7 high, as from a pipe: the amount takes the bar's
	// row, and the path is below it.
	for _, tc := range []struct {
		read, total int64
		want        []string
	}{
		{512, 0, []string{"512 bytes read", "x"}},
		{2048, 0, []string{"2.0 KiB read", "x"}},
		{1500, 1000, []string{strings.Repeat("█", 38) + " 100%", apart("1.5 KiB read", "1000 bytes"), "x"}},
		{0, 1000, []string{strings.Repeat("░", 38) + "   0%", apart("0 bytes read", "1000 bytes"), "x"}},
		{1 << 29, 3 << 30, []string{strings.Repeat("█", 6) + "▎" + strings.Repeat("░", 31) + "  16%", apart("512 MiB read", "3.0 GiB"), "x"}},
	} {
		scr := term.NewScreen(80, 24)
		DrawRead(scr, st, "", "a", "b", ReadStatus{Read: tc.read, Total: tc.total, Current: "x"})
		for i, want := range tc.want {
			if got := inside(scr, 11+i); got != want {
				t.Errorf("%d of %d, row %d: %q, want %q", tc.read, tc.total, i, got, want)
			}
		}
	}

	// The smallest screen keeps the count.
	scr = term.NewScreen(40, 5)
	DrawRead(scr, st, "", "a", "b", ReadStatus{Entries: 1, Read: 1, Total: 2})
	if !strings.Contains(scr.String(), "1 entry") {
		t.Errorf("small screen:\n%s", scr.String())
	}
}
