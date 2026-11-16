// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"strings"
	"testing"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/term"
)

func TestTextView(t *testing.T) {
	var lines []string
	for i := range 20 {
		lines = append(lines, "line "+strings.Repeat("x", i))
	}
	lines = append(lines, "", "a\x1bb", "long "+strings.Repeat("word ", 10))
	v := &TextView{Styles: colors.Default(), Lead: "(commit) ", Title: display.Name("abc subject"), Lines: lines}
	scr := term.NewScreen(30, 12)
	v.Draw(scr)
	got := strings.Split(scr.String(), "\n")
	// The box across the screen, titled, the lines in it, and the status
	// bar below.
	if !strings.HasPrefix(got[0], "┌ (commit) abc subject ─") || !strings.HasPrefix(got[1], "│ line ") || !strings.HasPrefix(got[10], "└") {
		t.Errorf("screen:\n%s", scr.String())
	}
	if !strings.Contains(got[11], "[") || !strings.Contains(got[11], "%]") {
		t.Errorf("no position in the status bar: %q", got[11])
	}
	// Long lines are broken between words to fit, escapes shown.
	v.ScrollTo(false)
	v.Draw(scr)
	got = strings.Split(scr.String(), "\n")
	text := strings.Join(got[1:10], "\n")
	if !strings.Contains(text, "│ a\\x1bb") || !strings.Contains(text, "│ long word word word word") || !strings.Contains(got[9], "│ word") {
		t.Errorf("end of the text:\n%s", scr.String())
	}
	if !strings.Contains(got[11], "[100%]") {
		t.Errorf("status at the end: %q", got[11])
	}
	top := v.Top
	v.Scroll(100)
	if v.Top != top {
		t.Errorf("scrolled past the end, to %d from %d", v.Top, top)
	}
	v.Scroll(-v.Page())
	if v.Top != top-v.Page() || v.Page() != 8 {
		t.Errorf("a page up: %d, page %d", v.Top, v.Page())
	}
	v.ScrollTo(true)
	v.Notice = "note"
	v.Draw(scr)
	if v.Top != 0 || !strings.HasPrefix(strings.TrimSpace(strings.Split(scr.String(), "\n")[11]), "note") {
		t.Errorf("top %d, status %q", v.Top, strings.Split(scr.String(), "\n")[11])
	}
}

func TestSidePopup(t *testing.T) {
	scr := term.NewScreen(80, 24)
	NewSidePopup("Select side's commit", display.Name("abc first"), display.Name("def second")).Draw(scr, colors.Default())
	got := scr.String()
	for _, want := range []string{"│  Select side's commit", "│  h ←  abc first", "│  l →  def second"} {
		if !strings.Contains(got, want) {
			t.Errorf("popup lacks %q:\n%s", want, got)
		}
	}
}

func TestTreeHelpFor(t *testing.T) {
	has := func(lines []HelpLine, text string) bool {
		for _, l := range lines {
			if l.Text == text && len(l.Keys) == 1 && l.Keys[0] == "Esc" {
				return true
			}
		}
		return false
	}
	if l := TreeHelpFor(false); !has(l, treeEsc) || has(l, treeEscBack) {
		t.Error("Esc goes back from the first tree")
	}
	if l := TreeHelpFor(true); has(l, treeEsc) || !has(l, treeEscBack) {
		t.Error("Esc doesn't go back from a commit's trees")
	}
	if !has(TreeHelp, treeEsc) {
		t.Error("TreeHelp changed")
	}
}
