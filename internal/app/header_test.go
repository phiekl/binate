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
	for _, tc := range []struct {
		h      int
		header bool
	}{
		{10, true},
		// No room for both: the header gives way.
		{MinHeight, false},
	} {
		main := &sizeScreen{}
		a := &App{screen: term.NewScreen(50, tc.h), Main: main, Styles: colors.Default()}
		a.Header = view.Header{Label: "(git)", Path: "/home/u/src/" + strings.Repeat("x", 60) + "/end", Home: "/home/u"}
		a.drawMain()
		lines := strings.Split(a.screen.String(), "\n")
		switch {
		case !tc.header && (main.h != tc.h || !strings.HasPrefix(lines[0], "top")):
			t.Errorf("height %d: drawn %dx%d:\n%s", tc.h, main.w, main.h, a.screen)
		case tc.header && (main.h != tc.h-1 || !strings.HasPrefix(lines[1], "top") || !strings.HasPrefix(lines[tc.h-1], "bottom")):
			t.Errorf("height %d: drawn %dx%d:\n%s", tc.h, main.w, main.h, a.screen)
		case tc.header && !strings.HasPrefix(lines[0], " (git) ~/src/xx") || tc.header && !strings.HasSuffix(strings.TrimRight(lines[0], " "), "xx/end"):
			t.Errorf("header %q", lines[0])
		}
		// The path, shown from home, links to it in full.
		if c := a.screen.Cell(len(" (git) "), 0); tc.header && c.Style.Link != "'"+a.Header.Path+"'" {
			t.Errorf("header link %q", c.Style.Link)
		}
	}
}
