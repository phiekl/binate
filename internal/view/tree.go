// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/tree"
)

// FilterBar is the filter, or with Search the search, being edited.
type FilterBar struct {
	Query     []rune
	Cursor    int
	DiffsOnly bool
	Search    bool
	// Literal matches Query as text, not a regular expression.
	Literal bool
}

// Valid reports whether the query compiles.
func (f *FilterBar) Valid() bool {
	_, err := tree.Compile(string(f.Query), f.Literal)
	return err == nil
}

// TreeStatus is the scan progress, status bar and panels beside the tree.
type TreeStatus struct {
	Scanning bool
	Scanned  int64
	Elapsed  time.Duration
	// Current is the path, relative to the roots, being read.
	Current string
	// Message, if set, replaces the counts until the next key.
	Message string
	// Filter is set while the filter or search bar is open.
	Filter *FilterBar
	// Stats returns what the panels show of the selected entry.
	Stats func(*scan.Node) [2]StatSide
	// Preview returns the selected entry's differences, nil while they are
	// read, and whether it has any to show above the panels. Where the
	// preview fits, its rows are kept for it whether or not it does, so
	// the tree keeps its height as the selection moves.
	Preview func(*scan.Node) (*DiffView, bool)
	// Action, if set, is an action's result on the selected entry, shown
	// in place of the preview, or of the panels where the preview doesn't
	// fit.
	Action *ActionResult
}

// TreeView draws the tree view.
type TreeView struct {
	Styles      colors.Styles
	Home        string
	Left, Right string
	// Compare is the attributes the scan compares, whose differences the
	// panels highlight.
	Compare scan.Attrs
	top     int
	// The displayed count follows scan progress between frames, rather
	// than jumping by a whole directory's worth of entries.
	loading     bool
	loaded      int64
	loadElapsed time.Duration
	// rows is the number of tree rows last drawn.
	rows int
}

// Rows returns the number of tree rows last drawn, or 0 before any: fewer
// than TreeRows with a preview.
func (v *TreeView) Rows() int {
	return v.rows
}

// TreeRows is the number of tree rows shown on a screen of height h: all
// but the boxes' borders, the panels and the status bar.
func TreeRows(h int) int {
	rows, _ := paneRows(h)
	return rows
}

// Draw draws the tree in the two boxes, the selected entry's metadata in
// the panels below it, and the status bar or loading box.
func (v *TreeView) Draw(s *term.Screen, m *tree.Model, st TreeStatus) {
	p := Layout(s.W)
	titles := [2][]display.Glyph{display.Name(HomePath(v.Left, v.Home)), display.Name(HomePath(v.Right, v.Home))}
	if st.Scanning {
		drawBoxes(s, p, boxHeight(s.H), nil, titles, [2]string{}, v.Styles)
		s.Fill(0, s.H-1, s.W, v.Styles.Plain)
		if st.Filter != nil {
			v.drawFilterBar(s, s.H-1, st.Filter)
		}
		v.drawLoading(s, st)
		return
	}
	v.loading = false
	rows := m.Rows()
	sel := m.Selected(rows)
	height, panel := paneRows(s.H)
	// The preview, where it fits, takes from the tree's rows.
	var preview int
	if panel > 0 && st.Preview != nil {
		height, preview = previewRows(height)
	}
	v.rows = height
	if sel < v.top {
		v.top = sel
	}
	if sel >= v.top+height {
		v.top = sel - height + 1
	}
	v.top = max(0, min(v.top, len(rows)-height))
	var label string
	if len(rows) > 0 {
		label = fmt.Sprintf("%d/%d", sel+1, len(rows))
	}
	drawBoxes(s, p, boxHeight(s.H), nil, titles, [2]string{label, label}, v.Styles)
	for y := 0; y < height && v.top+y < len(rows); y++ {
		i := v.top + y
		v.drawRow(s, p, 1+y, m, rows[i], i == sel)
	}
	if len(rows) == 0 {
		v.drawEmpty(s, p, height, m, st)
	}
	var selNode *scan.Node
	if len(rows) > 0 {
		selNode = rows[sel].Node
		panelY := 1 + height
		if preview > 0 {
			drawDividers(s, p, panelY, v.Styles)
			if st.Action != nil {
				v.drawAction(s, p, panelY+1, preview, st.Action)
			} else if pv, ok := st.Preview(selNode); ok {
				v.drawPreview(s, p, panelY+1, preview, pv)
			} else {
				text := dotted(noPreview(selNode, v.Compare), min(p.LeftW, p.RightW))
				drawCentered(s, p, panelY+1, text, v.Styles)
			}
			panelY += 1 + preview
		}
		switch {
		case panel > 0 && st.Action != nil && preview == 0:
			drawDividers(s, p, panelY, v.Styles)
			v.drawAction(s, p, panelY+1, panel, st.Action)
		case panel > 0 && st.Stats != nil:
			drawPanels(s, p, panelY, panel, st.Stats(selNode), v.Compare, v.Home, v.rowStyle(selNode), v.Styles)
		}
	}
	if st.Action != nil && panel == 0 && st.Message == "" {
		st.Message = st.Action.Title + " needs a taller terminal"
	}
	// Without the panels, what could not be read shows in the status bar.
	if panel == 0 && selNode != nil && st.Message == "" {
		st.Message = sideErrors(selNode)
	}
	if st.Filter != nil {
		v.drawFilterBar(s, s.H-1, st.Filter)
	} else {
		v.drawStatus(s, s.H-1, m, st)
	}
}

// drawLoading shows scan progress in a centered box over the tree: a
// spinner, the entry count, the elapsed time and the path being read.
func (v *TreeView) drawLoading(s *term.Screen, st TreeStatus) {
	const countEase = 150 * time.Millisecond
	if !v.loading || st.Elapsed < v.loadElapsed || st.Scanned < v.loaded {
		v.loaded = st.Scanned
	} else if dt := st.Elapsed - v.loadElapsed; dt > 0 && st.Scanned > v.loaded {
		gap := st.Scanned - v.loaded
		step := int64(float64(gap) * (1 - math.Exp(-float64(dt)/float64(countEase))))
		v.loaded += min(gap, max(step, 1))
	}
	v.loading, v.loadElapsed = true, st.Elapsed

	// A fixed size keeps the box still as the count and path change.
	w, h := min(52, max(s.W-4, 0)), min(6, max(s.H-2, 3))
	x, y := (s.W-w)/2, max((s.H-h)/2, 0)
	accent := v.Styles.Accent
	dim := v.Styles.Border.On(accent)
	Panel(s, v.Styles.Accent, x, y, w, h)

	// The spinner sits left of the count, the elapsed time at the right
	// end of its line, and the path, if there is room, below.
	const spinner = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
	frames := []rune(spinner)
	frame := frames[int(max(st.Elapsed, 0)/(80*time.Millisecond))%len(frames)]
	textX, textW := x+6, max(w-9, 0)
	rows := min(2, h-2)
	top := y + 1 + (h-2-rows)/2
	s.Set(x+3, top, frame, 1, accent.With(term.Bold))
	elapsed := fmt.Sprintf("%.1fs", st.Elapsed.Seconds())
	count := ShortenEnd(display.Plain(entries(v.loaded)), max(textW-len(elapsed)-2, 0))
	s.Text(textX, top, textW, count, accent, accent)
	s.Label(textX+max(textW-len(elapsed), 0), top, min(len(elapsed), textW), elapsed, dim)
	if rows == 2 {
		path := ShortenMiddle(display.Name(st.Current), textW)
		s.Text(textX, top+1, textW, path, dim, rowEscape(v.Styles.Escape, accent))
	}
}

// entries counts entries in words.
func entries(n int64) string {
	if n == 1 {
		return "1 entry"
	}
	return groupDigits(n) + " entries"
}

// groupDigits writes n with commas between groups of three digits.
func groupDigits(n int64) string {
	digits := fmt.Sprint(n)
	if n < 0 {
		return "-" + groupDigits(-n)
	}
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

// drawEmpty says why no rows are shown: in the left pane, or, for roots
// that could not be listed, each in its own pane.
func (v *TreeView) drawEmpty(s *term.Screen, p Panes, height int, m *tree.Model, st TreeStatus) {
	if m.Root.L.Err != nil || m.Root.R.Err != nil {
		for i, sd := range []*scan.Side{&m.Root.L, &m.Root.R} {
			if sd.Err != nil {
				x, w := p.LeftX, p.LeftW
				if i == 1 {
					x, w = p.RightX, p.RightW
				}
				drawNote(s, x, 1, w, height, "failed listing this directory ("+sd.Err.Error()+")", v.Styles)
			}
		}
		return
	}
	msg := "no differences"
	if !m.ShowSame {
		msg += " shown, * shows identical entries"
	}
	if m.Filtered {
		msg = "nothing matches the filter"
	}
	drawNote(s, p.LeftX, 1, p.LeftW, height, msg, v.Styles)
}

// drawRow draws a row across both panes and the mark column between them,
// with a background only for the selection.
func (v *TreeView) drawRow(s *term.Screen, p Panes, y int, m *tree.Model, row tree.Row, isSel bool) {
	n := row.Node
	var bg term.Style
	if isSel {
		bg = v.Styles.Select
	}
	v.drawSide(s, p.LeftX, p.LeftW, y, m, row, &n.L, bg)
	v.drawSide(s, p.RightX, p.RightW, y, m, row, &n.R, bg)
	gap := v.Styles.Plain.On(bg)
	markStyle := v.rowStyle(n)
	if n.IsDir() {
		markStyle = withoutBold(markStyle)
	}
	s.Set(p.Mark-1, y, ' ', 1, gap)
	s.Set(p.Mark, y, n.Mark(), 1, markStyle.On(bg))
	s.Set(p.Mark+1, y, ' ', 1, gap)
}

// markStyle returns the style of a pair's mark. One for more than one
// kind of difference takes the colour of the most significant: data, then
// attributes, then mtime.
func (v *TreeView) markStyle(n *scan.Node) term.Style {
	switch n.Mark() {
	case '|', '~', '#', '*':
		switch {
		case n.Own&scan.Data != 0:
			return v.Styles.Changed
		case n.Own&scan.Meta != 0:
			return v.Styles.Attrs
		case n.Own&scan.Mtime != 0:
			return v.Styles.Mtime
		}
		return v.Styles.Changed
	case '<':
		return v.Styles.LeftOnly
	case '>':
		return v.Styles.RightOnly
	case '?':
		return v.Styles.Unverified
	case '!':
		return v.Styles.Type
	}
	return v.Styles.Plain
}

// rowStyle uses the bright palette for differences involving a file or
// symlink.
func (v *TreeView) rowStyle(n *scan.Node) term.Style {
	style := v.markStyle(n)
	fileOrLink := func(k scan.Kind) bool { return k == scan.File || k == scan.Symlink }
	if n.State.IsDiff() && (fileOrLink(n.L.Kind) || fileOrLink(n.R.Kind)) {
		style = brighten(style)
	}
	return style
}

// brighten returns style in the bright palette if it is one of the eight
// ANSI colours, bold or not. Custom extended colours keep the values the
// user chose.
func brighten(style term.Style) term.Style {
	colour := strings.TrimPrefix(style.SGR, "1;")
	if len(colour) == 2 && colour[0] == '3' && colour[1] >= '0' && colour[1] <= '7' {
		style.SGR = "9" + colour[1:]
	}
	return style
}

// withoutBold resets bold set through Attr, SGR, or both.
func withoutBold(style term.Style) term.Style {
	style.Attr &^= term.Bold
	if style.SGR != "" {
		style.SGR += ";22"
	}
	return style
}

// typeLabelWidth is the width of the type labels, one more than the
// widest, so the rule lines up with the panels'.
const typeLabelWidth = 5

// typeColumn is the columns the type takes before a side's name: the
// label, right aligned, then " │ ".
const typeColumn = typeLabelWidth + 3

// typeLabel returns the label of sd's type, and its style: ls's colours.
func (v *TreeView) typeLabel(sd *scan.Side) (string, term.Style) {
	st := v.Styles
	switch sd.Kind {
	case scan.File:
		if sd.Executable() {
			return "file", st.Exec
		}
		return "file", st.Plain
	case scan.Dir:
		return "dir", withoutBold(st.Dir)
	case scan.Symlink:
		return "link", st.Link
	case scan.FIFO:
		return "pipe", st.Pipe
	case scan.CharDev:
		return "char", st.Device
	case scan.BlockDev:
		return "bdev", st.Device
	case scan.Socket:
		return "sock", st.Socket
	}
	return "?", st.Plain
}

// drawSide draws a side of a row over the row's background bg.
func (v *TreeView) drawSide(s *term.Screen, x, w, y int, m *tree.Model, row tree.Row, sd *scan.Side, bg term.Style) {
	plain, esc := v.Styles.Plain.On(bg), rowEscape(v.Styles.Escape, bg)
	nameStyle := withoutBold(v.rowStyle(row.Node)).On(bg)
	s.Fill(x, y, w, plain)
	if sd.Kind == scan.Missing || w <= 0 {
		return
	}
	n := row.Node

	// The type, right aligned, and a rule before the name.
	label, labelStyle := v.typeLabel(sd)
	labelStyle, rule := labelStyle.On(bg), v.Styles.Border.On(bg)
	s.Label(x+typeLabelWidth-len(label), y, len(label), label, labelStyle)
	s.Label(x+typeLabelWidth, y, w-typeLabelWidth, " │ ", rule)
	x, w = x+typeColumn, max(w-typeColumn, 0)

	var lead []display.Glyph
	switch {
	case row.Flat:
		if parent := n.Parent.Path(); parent != "" {
			lead = append(display.Name(parent), display.Plain(" › ")...)
		}
	default:
		lead = display.Plain(strings.Repeat("  ", row.Depth))
		switch {
		case sd.Kind != scan.Dir:
			lead = append(lead, display.Plain("  ")...)
		case m.Expanded(n):
			lead = append(lead, display.Plain("▾ ")...)
		default:
			lead = append(lead, display.Plain("▸ ")...)
		}
	}
	name := display.Name(n.Name)
	if sd.Kind == scan.Dir {
		name = append(name, display.Glyph{R: '/', W: 1})
	}
	var tail []display.Glyph
	if sd.Kind == scan.Symlink {
		tail = append(display.Plain(" -> "), display.Name(sd.Target)...)
	}

	textW := w
	// The name matters most: shorten the parent path of a filter result
	// first, then the symlink target, then the name.
	nameW := display.Width(name)
	leadW := display.Width(lead)
	if row.Flat && leadW+nameW > textW {
		lead = ShortenMiddle(lead, max(textW-nameW, 0))
		leadW = display.Width(lead)
	}
	used := s.Text(x, y, textW, lead, plain, esc)
	if used < leadW {
		return
	}
	nameRoom := textW - used
	if nameW+display.Width(tail) > nameRoom {
		tail = ShortenEnd(tail, max(nameRoom-nameW, 0))
	}
	used += s.Text(x+used, y, nameRoom, ShortenEnd(name, nameRoom), nameStyle, esc)
	s.Text(x+used, y, textW-used, tail, plain, esc)
}

// sideErrors describes what could not be read of n, or is "".
func sideErrors(n *scan.Node) string {
	var parts []string
	if n.L.Err != nil {
		parts = append(parts, sideError("left", n.L.Err))
	}
	if n.R.Err != nil {
		parts = append(parts, sideError("right", n.R.Err))
	}
	return strings.Join(parts, ", ")
}

// countItems shows counts as status bar items, each in the style of its
// mark in the tree, leaving out kinds that are zero.
func countItems(c tree.Counts, st colors.Styles) []barItem {
	items := []barItem{{text: fmt.Sprintf("%d items total", c.Total), style: st.Plain}}
	for _, k := range []struct {
		n     int
		label string
		style term.Style
	}{
		{c.Same, "identical", st.Plain},
		{c.LeftOnly, "left", st.LeftOnly},
		{c.RightOnly, "right", st.RightOnly},
		{c.Type, "type", st.Type},
		{c.Data, "contents", st.Changed},
		{c.Meta, "attrs", st.Attrs},
		{c.Mtime, "mtime", st.Mtime},
		{c.Unverified, "unverified", st.Unverified},
		{c.Pending, "pending", st.Plain},
	} {
		if k.n > 0 {
			items = append(items, barItem{text: fmt.Sprintf("%d %s", k.n, k.label), style: k.style})
		}
	}
	return items
}

// drawStatus draws the status bar: the counts, or a message instead, and
// the applied filter and search.
func (v *TreeView) drawStatus(s *term.Screen, y int, m *tree.Model, st TreeStatus) {
	var items []barItem
	if st.Message != "" {
		items = append(items, barItem{text: st.Message, style: v.Styles.Status, note: true})
	} else {
		items = countItems(tree.Count(m.Root), v.Styles)
	}
	if m.Filtered {
		f := strings.ToLower(barLabel(false, m.DiffsOnly, m.Literal, true, true))
		if m.Query != "" {
			f += ": " + m.Query
		}
		items = append(items, barItem{text: f, style: v.Styles.Status})
	}
	if m.Search != "" {
		f := strings.ToLower(barLabel(true, false, m.SearchLiteral, true, true))
		items = append(items, barItem{text: f + ": " + m.Search, style: v.Styles.Status})
	}
	drawBar(s, y, items, v.Styles)
}

// barLabel names the filter, saying which entries it filters, or the
// search, and how the query matches. With short, a regular expression,
// the usual, is not mentioned, to save room in the status bar.
func barLabel(search, diffsOnly, literal, valid, short bool) string {
	var modes []string
	if !search {
		modes = append(modes, "all")
		if diffsOnly {
			modes[0] = "diffs"
		}
	}
	switch {
	case literal:
		modes = append(modes, "literal")
	case !valid:
		modes = append(modes, "invalid regex")
	case !short:
		modes = append(modes, "regex")
	}
	label := "Filter"
	if search {
		label = "Search"
	}
	if len(modes) > 0 {
		label += " (" + strings.Join(modes, ", ") + ")"
	}
	return label
}

func (v *TreeView) drawFilterBar(s *term.Screen, y int, f *FilterBar) {
	style := v.Styles.Accent
	esc := rowEscape(v.Styles.Escape, style)
	s.Fill(0, y, s.W, style)
	label := barLabel(f.Search, f.DiffsOnly, f.Literal, f.Valid(), false) + ":"
	x := 1 + s.Label(1, y, len(label), label, style.With(term.Bold)) + 1
	room := max(s.W-x-1, 1)
	// Each rune of the query as shown, so the cursor can be placed.
	var glyphs [][]display.Glyph
	for _, r := range f.Query {
		glyphs = append(glyphs, display.Name(string(r)))
	}
	cursorCol := 0
	for _, g := range glyphs[:f.Cursor] {
		cursorCol += display.Width(g)
	}
	// Scroll so the cursor stays shown.
	skip := max(cursorCol-room+1, 0)
	col := 0
	for _, g := range glyphs {
		w := display.Width(g)
		if col >= skip && col-skip+w <= room {
			s.Text(x+col-skip, y, w, g, style, esc)
		}
		col += w
	}
	s.CursorOn, s.CursorX, s.CursorY = true, x+cursorCol-skip, y
}

// sideError describes what could not be read of a side, the left or right.
func sideError(side string, err error) string {
	var nl scan.NotListedError
	if errors.As(err, &nl) {
		return "failed listing the " + side + " side's directory (" + nl.Err.Error() + ")"
	}
	return "failed reading the " + side + " side (" + err.Error() + ")"
}
