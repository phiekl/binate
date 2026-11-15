// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"strings"
	"testing"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/term"
)

func TestValueLines(t *testing.T) {
	v := strings.Repeat("0123456789abcdef", 4) // 64, as SHA-256
	for _, c := range []struct {
		w, h int
		want string
	}{
		// Lines of the full width.
		{64, 9, v},
		{36, 9, "0123456789abcdef0123456789abcdef0123|456789abcdef0123456789abcdef"},
		{16, 9, "0123456789abcdef|0123456789abcdef|0123456789abcdef|0123456789abcdef"},
		// Too many lines: the last ends in ….
		{16, 2, "0123456789abcdef|0123456789abcde…"},
		{40, 1, "0123456789abcdef0123456789abcdef0123456…"},
	} {
		if got := strings.Join(valueLines(v, c.w, c.h), "|"); got != c.want {
			t.Errorf("%dx%d:\n%s\nwant\n%s", c.w, c.h, got, c.want)
		}
	}
}

func TestActionPopup(t *testing.T) {
	p := NewActionPopup([]ActionRow{
		{Key: '2', Name: "SHA-256", What: "checksum"},
		{Key: 'x', Name: "Longer name", What: "other", Why: "can't"},
	})
	if p.Row() != 0 || !p.SelectKey('x') || p.Row() != 1 || p.SelectKey('q') {
		t.Errorf("selection: row %d", p.Row())
	}
	if p.Lines[2].Dim || !p.Lines[3].Dim {
		t.Error("only the unavailable row is dimmed")
	}
	st := colors.Default()
	scr := term.NewScreen(60, 12)
	p.Draw(scr, st)
	out := scr.String()
	for _, want := range []string{"Actions", "2  SHA-256      checksum", "x  Longer name  other", "Esc closes"} {
		if !strings.Contains(out, want) {
			t.Errorf("popup lacks %q:\n%s", want, out)
		}
	}
	for y, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "Longer"); i >= 0 {
			if c := scr.Cell(len([]rune(line[:i])), y); c.Style.Attr&term.Dim == 0 {
				t.Errorf("unavailable row drawn %+v", c.Style)
			}
		}
	}
}
