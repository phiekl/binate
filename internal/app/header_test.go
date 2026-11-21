// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"strings"
	"testing"
	"time"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/view"
)

// sizeScreen draws its size at its top, and its last row.
type sizeScreen struct{ w, h int }

func (s *sizeScreen) Draw(scr *term.Screen) {
	s.w, s.h = scr.W, scr.H
	scr.Label(0, 0, scr.W, "top", term.Style{})
	scr.Label(0, scr.H-1, scr.W, "bottom", term.Style{})
}

func (s *sizeScreen) Key(term.Key) bool { return false }

func (s *sizeScreen) Tick() time.Duration { return -1 }

func TestHeader(t *testing.T) {
	long := "/home/u/src/" + strings.Repeat("x", 60) + "/end"
	for _, tc := range []struct {
		h      int
		header bool
	}{
		{10, true},
		// No room for both: the header gives way.
		{MinHeight, false},
	} {
		main := &sizeScreen{}
		a := &App{screen: term.NewScreen(80, tc.h), Main: main, Styles: colors.Default()}
		a.Header = view.Header{Sides: [2]view.HeaderSide{{Label: "(git)", Path: long}, {Label: "(dir)", Path: "/mnt/b"}}, Home: "/home/u"}
		a.drawMain()
		lines := strings.Split(a.screen.String(), "\n")
		p := view.Layout(80)
		switch {
		case !tc.header && (main.h != tc.h || !strings.HasPrefix(lines[0], "top")):
			t.Errorf("height %d: drawn %dx%d:\n%s", tc.h, main.w, main.h, a.screen)
		case tc.header && (main.h != tc.h-1 || !strings.HasPrefix(lines[1], "top") || !strings.HasPrefix(lines[tc.h-1], "bottom")):
			t.Errorf("height %d: drawn %dx%d:\n%s", tc.h, main.w, main.h, a.screen)
		case tc.header:
			// Each side above its box, within its borders, the long path
			// shortened in the middle.
			left, right := []rune(lines[0])[:p.RightBox], string([]rune(lines[0])[p.RightBox:])
			if l := strings.TrimRight(string(left), " "); !strings.HasPrefix(l, " (git) ~/src/xx") || !strings.HasSuffix(l, "xx/end") || len([]rune(l)) > p.LeftBoxW-1 {
				t.Errorf("left header %q", l)
			}
			if strings.TrimRight(right, " ") != " (dir) /mnt/b" {
				t.Errorf("right header %q", right)
			}
		}
		// The paths, shown from home, link to them in full.
		if c := a.screen.Cell(len(" (git) "), 0); tc.header && c.Style.Link != "'"+long+"'" {
			t.Errorf("left header link %q", c.Style.Link)
		}
		if c := a.screen.Cell(p.RightBox+len(" (dir) "), 0); tc.header && c.Style.Link != "'/mnt/b'" {
			t.Errorf("right header link %q", c.Style.Link)
		}
	}
	// One side alone, as none else is set, is still shown.
	a := &App{screen: term.NewScreen(80, 10), Main: &sizeScreen{}, Styles: colors.Default()}
	a.Header = view.Header{Sides: [2]view.HeaderSide{1: {Label: "(git)", Path: "/r"}}}
	a.drawMain()
	if line := strings.Split(a.screen.String(), "\n")[0]; strings.TrimSpace(line) != "(git) /r" {
		t.Errorf("one side %q", line)
	}
}
