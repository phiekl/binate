// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"strings"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/term"
)

// HelpLine is a row of Help: a key, perhaps an alternative to it, and what
// they do. A row without keys is a section's title, or without text a
// horizontal line across the box.
type HelpLine struct {
	Keys []string
	Text string
	// Path, if set, is shown instead of Text: escaped, and shortened in
	// the middle to fit.
	Path []display.Glyph
	// Hot, if not 0, is the position in Text, plus one, of a letter that
	// is a key for the row, highlighted as the quit prompt's x.
	Hot int
	// Dim dims the text, as for a choice that is unavailable.
	Dim bool
}

// width returns the width of the row's text.
func (l HelpLine) width() int {
	if l.Path != nil {
		return display.Width(l.Path)
	}
	return len([]rune(l.Text))
}

// key returns a row of Help for keys, the vim-like one first.
func key(text string, keys ...string) HelpLine { return HelpLine{Keys: keys, Text: text} }

// TreeHelp lists the tree view's keys.
var TreeHelp = []HelpLine{
	{Text: "Keyboard shortcuts"},
	{},
	key("move down", "j", "Down"),
	key("move up", "k", "Up"),
	key("move down a screen", "Ctrl+f", "PgDn"),
	key("move up a screen", "Ctrl+b", "PgUp"),
	key("move to the first row", "gg", "Home"),
	key("move to the last row", "G", "End"),
	key("expand a directory", "l", "Right"),
	key("expand the directory's whole tree", "L", "Shift+Right"),
	key("collapse, or move to the parent", "h", "Left"),
	key("collapse the directory's whole tree", "H", "Shift+Left"),
	key("expand all", "+", "="),
	key("collapse all", "-"),
	key("show or hide identical entries, in a filter too", "*"),
	key("next difference", "n", "Shift+Down"),
	key("previous difference", "N", "Shift+Up"),
	key("compare the files, or expand a directory", "Enter"),
	key("filter by name (switch mode with Tab)", "F"),
	key("search the names", "/"),
	key("switch between regex and literal text in the bar", "Ctrl+r"),
	key("recall earlier queries in the bar", "Up", "Down"),
	key("next match while searching", "n"),
	key("previous match while searching", "N"),
	key(treeEsc, "Esc"),
	key("edit the entry in $VISUAL or $EDITOR", "e"),
	key("choose what to compare", "c"),
	key("run an action on the entry, as a checksum or showing its commit", "a"),
	key("rescan", "r"),
	key("exit binate", "Ctrl+c"),
	{},
	{Text: "Comparison marks"},
	{},
	key("no difference found", "="),
	key("data differs: contents, size, target or device", "|"),
	key("attributes differ: mode or owner", "~"),
	key("modification time differs", "#"),
	key("more than one of |, ~ and #", "*"),
	key("not verified", "?"),
	key("on the left side only", "<"),
	key("on the right side only", ">"),
	key("different entry types", "!"),
}

// treeEsc is what Esc does in the tree, and treeEscBack in one opened
// from another view, as a commit's trees.
const (
	treeEsc     = "close an action's result, end the search, or clear the filter"
	treeEscBack = "close an action's result, end the search, clear the filter, or go back"
)

// TreeHelpFor returns the tree view's keys, with Esc going back once
// nothing else is left to close if back is set.
func TreeHelpFor(back bool) []HelpLine {
	lines := append([]HelpLine{}, TreeHelp...)
	if back {
		for i, l := range lines {
			if l.Text == treeEsc {
				lines[i].Text = treeEscBack
			}
		}
	}
	return lines
}

// TextHelp lists the keys of the view of a text, as a commit's message.
var TextHelp = []HelpLine{
	{Text: "Keyboard shortcuts"},
	{},
	key("scroll down", "j", "Down"),
	key("scroll up", "k", "Up"),
	key("scroll down a screen", "Ctrl+f", "PgDn"),
	key("scroll up a screen", "Ctrl+b", "PgUp"),
	key("go to the start", "gg", "Home"),
	key("go to the end", "G", "End"),
	key("go back", "Esc"),
	key("exit binate", "Ctrl+c"),
}

// DiffHelp returns the diff view's keys, with Esc going back to the tree if
// back is set: the diff view opened from a tree.
func DiffHelp(back bool) []HelpLine {
	lines := append([]HelpLine{}, diffHelp...)
	if !back {
		return lines
	}
	for i, l := range lines {
		if l.Text == "exit binate" {
			return append(lines[:i:i], append([]HelpLine{key("return to the tree", "Esc")}, lines[i:]...)...)
		}
	}
	return lines
}

// diffHelp lists the diff view's keys but Esc.
var diffHelp = []HelpLine{
	{Text: "Keyboard shortcuts"},
	{},
	key("scroll down", "j", "Down"),
	key("scroll up", "k", "Up"),
	key("scroll down a screen", "Ctrl+f", "PgDn"),
	key("scroll up a screen", "Ctrl+b", "PgUp"),
	key("go to the start", "gg", "Home"),
	key("go to the end", "G", "End"),
	key("scroll left, without wrap", "Left"),
	key("scroll right, without wrap", "Right"),
	key("next change", "n", "Shift+Down"),
	key("previous change", "N", "Shift+Up"),
	key("wrap long lines", "w"),
	key("full file or changes with context", "f"),
	key("edit a side in $VISUAL or $EDITOR", "e"),
	key("show, compare or count the commit that last changed a side (git)", "a"),
	key("exit binate", "Ctrl+c"),
	{},
	{Text: "Comparison marks"},
	{},
	key("changed line", "|"),
	key("line on the left side only", "<"),
	key("line on the right side only", ">"),
}

// helpRule separates Help's columns, as in the tree's file stats.
const helpRule = " │ "

// line reports whether the row is a horizontal line.
func (l HelpLine) line() bool { return len(l.Keys) == 0 && l.Text == "" && l.Path == nil }

// selectable reports whether the row can be selected: whether it lists keys.
func (l HelpLine) selectable() bool { return len(l.Keys) > 0 }

// Help is the Help overlay's state.
type Help struct {
	Lines  []HelpLine
	Offset int
	// Selected is the index of the selected row in Lines, a row with keys.
	// Selecting only changes how the row is drawn.
	Selected int
	// PendingG is set after g, which goes to the start if repeated.
	PendingG bool
	// Plain sets the keys apart by spaces rather than rules, and the
	// selection covers only the text, not the keys.
	Plain bool
}

// box returns the overlay's geometry on a screen: its corner, its size,
// and the number of lines shown. As in the loading box, the text is set
// three columns in from the border.
func (h *Help) box(s *term.Screen) (x, y, w, bh, inner int) {
	w = 2
	text := h.textColumn()
	for _, l := range h.Lines {
		if len(l.Keys) > 0 {
			w = max(w, text+l.width()+6)
		} else {
			w = max(w, l.width()+6)
		}
	}
	w = max(min(w, s.W-2), 10)
	bh = min(len(h.Lines)+2, s.H-2)
	inner = max(bh-2, 1)
	x, y = (s.W-w)/2, (s.H-bh)/2
	return
}

// NewHelp returns Help showing lines, with its first row with keys
// selected.
func NewHelp(lines []HelpLine) *Help {
	h := &Help{Lines: lines}
	h.Selected = h.snap(0, 1)
	return h
}

// keyColumns returns the widths of the two key columns.
func (h *Help) keyColumns() (first, second int) {
	for _, l := range h.Lines {
		if len(l.Keys) > 0 {
			first = max(first, len([]rune(l.Keys[0])))
		}
		if len(l.Keys) > 1 {
			second = max(second, len([]rune(l.Keys[1])))
		}
	}
	return
}

// textColumn returns where the text of a row with keys starts, from the
// start of the row.
func (h *Help) textColumn() int {
	first, second := h.keyColumns()
	if h.Plain {
		if second > 0 {
			second++
		}
		return first + second + 2
	}
	if second > 0 {
		second += len([]rune(helpRule))
	}
	return first + second + len([]rune(helpRule))
}

// snap returns the selectable row nearest to i, looking in the direction
// dir (1 or -1) first.
func (h *Help) snap(i, dir int) int {
	i = max(0, min(i, len(h.Lines)-1))
	for _, d := range []int{dir, -dir} {
		for j := i; j >= 0 && j < len(h.Lines); j += d {
			if h.Lines[j].selectable() {
				return j
			}
		}
	}
	return 0
}

// Move moves the selection by delta rows, skipping headings and blank
// rows, and scrolls to keep it shown.
func (h *Help) Move(s *term.Screen, delta int) {
	dir := 1
	if delta < 0 {
		dir = -1
	}
	// The selectable row at or past the target, or the nearest before it
	// at the end of the text.
	h.Selected = h.snap(h.Selected+delta, dir)
	h.follow(s)
}

// MoveTo selects the first row (top true) or the last.
func (h *Help) MoveTo(s *term.Screen, top bool) {
	if top {
		h.Selected = h.snap(0, 1)
		h.Offset = 0
	} else {
		h.Selected = h.snap(len(h.Lines)-1, -1)
		h.Offset = len(h.Lines)
	}
	h.follow(s)
}

// follow scrolls to keep the selected row shown on s, and the title of
// its section when it is the section's first row; without a screen yet,
// Draw does.
func (h *Help) follow(s *term.Screen) {
	if s == nil {
		return
	}
	_, _, _, _, inner := h.box(s)
	h.Offset = max(0, min(h.Offset, len(h.Lines)-inner))
	if h.Selected < h.Offset {
		h.Offset = h.Selected
	}
	top := h.Selected
	for top > 0 && !h.Lines[top-1].selectable() {
		top--
	}
	if top < h.Offset && h.Selected-top < inner {
		h.Offset = top
	}
	if h.Selected >= h.Offset+inner {
		h.Offset = h.Selected - inner + 1
	}
}

// Draw draws the overlay over what s holds: a box like the loading box,
// with arrows on its border while there is more text above or below.
func (h *Help) Draw(s *term.Screen, st colors.Styles) {
	x, y, w, bh, inner := h.box(s)
	Panel(s, st.Accent, x, y, w, bh)
	h.Selected = h.snap(h.Selected, 1)
	h.follow(s)
	// The keys in bold in their columns and the text, separated by rules,
	// or by spaces if Plain; titles in bold between horizontal lines, which
	// join the rules. The selected row is a bar across the box, or across
	// the text if Plain.
	first, second := h.keyColumns()
	text := h.textColumn()
	left, right := x+3, x+w-3
	label := func(col, row int, s2 string, style term.Style) {
		if left+col < right {
			s.Label(left+col, row, right-left-col, s2, style)
		}
	}
	// Where the rules' │ fall, for the horizontal lines to join them.
	var joins []int
	if !h.Plain {
		joins = append(joins, left+first+1)
		if second > 0 {
			joins = append(joins, left+text-2)
		}
	}
	for i := 0; i < inner && h.Offset+i < len(h.Lines); i++ {
		l, row := h.Lines[h.Offset+i], y+1+i
		if l.line() {
			h.drawLine(s, st.Accent, x, w, row, h.Offset+i, joins)
			continue
		}
		style, keyStyle := st.Accent, st.Accent
		if h.Offset+i == h.Selected {
			style = st.HelpSelect
			if h.Plain {
				// From a column before the text to the border.
				from := min(left+text-1, x+w-1)
				s.Fill(from, row, x+w-1-from, style)
			} else {
				keyStyle = style
				s.Fill(x+1, row, w-2, style)
			}
		}
		bold, rule := keyStyle.With(term.Bold), st.Border.On(keyStyle)
		if !l.selectable() {
			label(0, row, l.Text, bold)
			continue
		}
		label(0, row, l.Keys[0], bold)
		if h.Plain {
			if len(l.Keys) > 1 {
				label(first+1, row, l.Keys[1], bold)
			}
		} else {
			label(first, row, helpRule, rule)
		}
		if second > 0 && !h.Plain {
			if len(l.Keys) > 1 {
				label(first+len([]rune(helpRule)), row, l.Keys[1], bold)
			}
			label(text-len([]rune(helpRule)), row, helpRule, rule)
		}
		if l.Path != nil {
			if pw := right - left - text; pw > 0 {
				s.Text(left+text, row, pw, ShortenMiddle(l.Path, pw), style, rowEscape(st.Escape, style))
			}
			continue
		}
		textStyle := style
		if l.Dim {
			textStyle = style.With(term.Dim)
		}
		label(text, row, l.Text, textStyle)
		if l.Hot > 0 {
			if r := []rune(l.Text); l.Hot <= len(r) && text+l.Hot-1 < right-left {
				s.Set(left+text+l.Hot-1, row, r[l.Hot-1], 1, style.On(st.QuitKey))
			}
		}
	}
	// The rules of rows next to the border join it too.
	for _, col := range joins {
		if col >= x+w-1 {
			continue
		}
		if h.Offset < len(h.Lines) && h.Lines[h.Offset].selectable() {
			s.Label(col, y, 1, "┬", st.Accent)
		}
		if last := min(h.Offset+inner, len(h.Lines)) - 1; last >= 0 && h.Lines[last].selectable() {
			s.Label(col, y+bh-1, 1, "┴", st.Accent)
		}
	}
	if h.Offset > 0 {
		s.Label(x+w-4, y, 1, "▲", st.Accent.With(term.Bold))
	}
	if h.Offset+inner < len(h.Lines) {
		s.Label(x+w-4, y+bh-1, 1, "▼", st.Accent.With(term.Bold))
	}
}

// drawLine draws the horizontal line at index i of Lines, at row, joining
// the box's sides and the rules at columns cols of the rows next to it.
func (h *Help) drawLine(s *term.Screen, st term.Style, x, w, row, i int, cols []int) {
	s.Label(x, row, w, "├"+strings.Repeat("─", max(w-2, 0))+"┤", st)
	above := i > 0 && h.Lines[i-1].selectable()
	below := i+1 < len(h.Lines) && h.Lines[i+1].selectable()
	join := map[[2]bool]string{{true, false}: "┴", {false, true}: "┬", {true, true}: "┼"}[[2]bool{above, below}]
	for _, col := range cols {
		if join != "" && col < x+w-1 {
			s.Label(col, row, 1, join, st)
		}
	}
}
