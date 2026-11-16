// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"fmt"
	"strconv"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/diff"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/term"
)

// DiffView is the diff view: two texts side by side, with the changes inside
// changed lines highlighted.
type DiffView struct {
	Styles      colors.Styles
	Home        string
	Left, Right string // the paths shown in the boxes' titles
	// Context is the number of unchanged lines shown around changes in
	// collapsed view.
	Context int

	L, R diff.Text
	Rows []diff.Row
	// Message is shown instead of the texts, as when a side can't be
	// opened in two-file mode.
	Message string
	// SideErrors, where either is set, are shown instead of the texts,
	// each in its side's box: why the side can't be shown.
	SideErrors [2]string

	Wrap    bool
	Full    bool
	Top     int // the first visual line shown
	HScroll int // columns scrolled sideways, without wrap

	// Notice, if set, replaces the counts in the status bar.
	Notice string
	// Stats is what the panels show of the two files.
	Stats [2]StatSide
	// Compare is the attributes whose differences the panels mark; 0
	// means the default.
	Compare scan.Attrs

	inline map[int][2][]diff.Span
	// wraps is where the wrapped parts of each side's lines start, for the
	// width of the layout, for lines of more than one part.
	wraps    [2]map[int][]display.Pos
	lines    []vline
	layoutOf layoutKey
	height   int
	// jumped is the block last jumped to, which Top showed at jumpTop.
	jumped  int
	jumpTop int
	hasJump bool
	// keep is the line on each side to show at the top once the texts
	// were replaced (or -1), and keeping is set until it is shown.
	keep    [2]int
	keeping bool
}

// vline is a visual line: part of a row, or a separator for unchanged
// lines left out.
type vline struct {
	row  int // index in Rows, or -1 for a separator
	part int // which part of a wrapped row
	skip int // unchanged lines a separator stands for
}

type layoutKey struct {
	width      int
	wrap, full bool
	rows       int
}

// SetTexts replaces the texts, as after an edit, keeping the view near the
// same line.
func (v *DiffView) SetTexts(l, r diff.Text) {
	keepL, keepR := -1, -1
	if v.Top < len(v.lines) && v.lines[v.Top].row >= 0 {
		row := v.Rows[v.lines[v.Top].row]
		keepL, keepR = row.L, row.R
	}
	v.L, v.R = l, r
	v.Rows = diff.Compare(l, r)
	v.inline = nil
	v.lines = nil
	v.wraps = [2]map[int][]display.Pos{}
	v.Message = ""
	v.SideErrors = [2]string{}
	v.hasJump = false
	v.keep, v.keeping = [2]int{keepL, keepR}, keepL >= 0 || keepR >= 0
}

// restore shows the line kept at the top: the first row at or after it on
// each side the row has a line on.
func (v *DiffView) restore() {
	if !v.keeping {
		return
	}
	v.keeping = false
	for i, l := range v.lines {
		if l.row < 0 {
			continue
		}
		r := v.Rows[l.row]
		after := func(line, keep int) bool { return line < 0 || keep < 0 || line >= keep }
		if (r.L >= 0 || r.R >= 0) && after(r.L, v.keep[0]) && after(r.R, v.keep[1]) {
			v.Top = i
			return
		}
	}
	v.Top = len(v.lines)
}

// HasChanges reports whether any row differs.
func (v *DiffView) HasChanges() bool {
	for _, r := range v.Rows {
		if r.Kind != diff.Same {
			return true
		}
	}
	return false
}

// gutter returns the width of the line number column.
func (v *DiffView) gutter() int {
	return len(strconv.Itoa(max(len(v.L.Lines), len(v.R.Lines), 1)))
}

// textWidth returns the width of a side's text on a screen width w.
func (v *DiffView) textWidth(w int) int {
	p := Layout(w)
	return max(min(p.LeftW, p.RightW)-v.gutter()-1, 1)
}

func (v *DiffView) layout(w int) []vline {
	key := layoutKey{width: w, wrap: v.Wrap, full: v.Full, rows: len(v.Rows)}
	if v.lines != nil && v.layoutOf == key {
		return v.lines
	}
	// Keep the line at the top across wrapping and context changes.
	if v.lines != nil && !v.keeping && v.Top < len(v.lines) {
		if l := v.lines[v.Top]; l.row >= 0 {
			r := v.Rows[l.row]
			v.keep, v.keeping = [2]int{r.L, r.R}, true
		}
	}
	v.layoutOf = key
	tw := v.textWidth(w)
	shown := make([]bool, len(v.Rows))
	if v.Full || !v.HasChanges() {
		for i := range shown {
			shown[i] = true
		}
	} else {
		// Clamped first: i+Context could overflow.
		ctx := min(max(v.Context, 0), len(v.Rows))
		marked := 0
		for i, r := range v.Rows {
			if r.Kind == diff.Same {
				continue
			}
			// Ranges advance in row order, so mark each row only once.
			start, end := max(i-ctx, marked), min(i+ctx, len(v.Rows)-1)+1
			for j := start; j < end; j++ {
				shown[j] = true
			}
			marked = end
		}
	}
	// Lines are wrapped without being turned into glyphs whole, which
	// for a long line, as of a minified file, would take a while.
	v.wraps = [2]map[int][]display.Pos{}
	if v.Wrap {
		for side, t := range []diff.Text{v.L, v.R} {
			v.wraps[side] = map[int][]display.Pos{}
			for i, line := range t.Lines {
				if display.Width(display.ContentFrom(line, display.Pos{}, tw+1)) <= tw {
					continue
				}
				v.wraps[side][i] = display.ContentWraps(line, tw)
			}
		}
	}
	lines := []vline{}
	skipped := 0
	for i := range v.Rows {
		if !shown[i] {
			skipped++
			continue
		}
		if skipped > 0 {
			lines = append(lines, vline{row: -1, skip: skipped})
			skipped = 0
		}
		parts := 1
		if v.Wrap {
			r := v.Rows[i]
			for side, idx := range []int{r.L, r.R} {
				if starts, ok := v.wraps[side][idx]; ok {
					parts = max(parts, len(starts))
				}
			}
		}
		for p := range parts {
			lines = append(lines, vline{row: i, part: p})
		}
	}
	if skipped > 0 {
		lines = append(lines, vline{row: -1, skip: skipped})
	}
	v.lines = lines
	v.restore()
	return lines
}

// rowsHeight is the number of visual lines shown on a screen of height h:
// all but the boxes' borders, the panels and the status bar.
func rowsHeight(h int) int {
	rows, _ := paneRows(h)
	return rows
}

// Scroll scrolls by delta visual lines.
func (v *DiffView) Scroll(delta int) {
	v.Top += delta
	v.clamp()
}

// ScrollTo scrolls to the start (true) or end.
func (v *DiffView) ScrollTo(start bool) {
	if start {
		v.Top = 0
	} else {
		v.Top = len(v.lines)
	}
	v.clamp()
}

// Page returns the distance of a page.
func (v *DiffView) Page() int {
	return max(rowsHeight(v.height)-1, 1)
}

func (v *DiffView) clamp() {
	v.Top = max(0, min(v.Top, len(v.lines)-rowsHeight(v.height)))
}

// ScrollSideways scrolls by delta columns, without wrap.
func (v *DiffView) ScrollSideways(delta int) {
	if v.Wrap {
		return
	}
	v.HScroll = max(v.HScroll+delta, 0)
}

// blockStart reports whether a visual line starts a block of changes.
func (v *DiffView) blockStart(i int) bool {
	l := v.lines[i]
	if l.row < 0 || l.part != 0 || v.Rows[l.row].Kind == diff.Same {
		return false
	}
	return l.row == 0 || v.Rows[l.row-1].Kind == diff.Same || (i > 0 && v.lines[i-1].row < 0)
}

// jumpMargin is how many lines are shown above a change jumped to.
const jumpMargin = 2

// Jump scrolls to the next (forward) or previous change block, and
// returns false if there is none that way.
func (v *DiffView) Jump(forward bool) bool {
	// Where a jump to the block starting at i puts the top.
	topFor := func(i int) int {
		return max(0, min(i-jumpMargin, len(v.lines)-rowsHeight(v.height)))
	}
	// From the block last jumped to, while it is shown where the jump put
	// it, the next block in order; else the next one that moves the view,
	// so a block already shown where a jump would put it is passed.
	fromJump := v.hasJump && v.Top == v.jumpTop
	ahead := func(i int) bool {
		if fromJump {
			return i > v.jumped
		}
		return topFor(i) > v.Top
	}
	behind := func(i int) bool {
		if fromJump {
			return i < v.jumped
		}
		return topFor(i) < v.Top
	}
	target := -1
	if forward {
		for i := range v.lines {
			if ahead(i) && v.blockStart(i) {
				target = i
				break
			}
		}
	} else {
		for i := len(v.lines) - 1; i >= 0; i-- {
			if behind(i) && v.blockStart(i) {
				target = i
				break
			}
		}
	}
	if target < 0 {
		return false
	}
	v.Top = topFor(target)
	v.jumped, v.jumpTop, v.hasJump = target, v.Top, true
	return true
}

// Draw draws the two texts in their boxes, and the status bar.
func (v *DiffView) Draw(s *term.Screen) {
	v.height = s.H
	lines := v.layout(s.W)
	v.clamp()
	h, panel := paneRows(s.H)
	p := Layout(s.W)
	titles := [2][]display.Glyph{display.Name(HomePath(v.Left, v.Home)), display.Name(HomePath(v.Right, v.Home))}
	drawBoxes(s, p, boxHeight(s.H), viewingLeads, titles, [2]string{}, v.Styles)
	compare := v.Compare
	if compare == 0 {
		compare = scan.DefaultAttrs
	}
	// The files are the item: changed if their texts differ.
	item := v.Styles.Plain
	if v.HasChanges() {
		item = brighten(v.Styles.Changed)
	}
	drawPanels(s, p, 1+h, panel, v.Stats, compare, v.Home, item, v.Styles)
	if v.Message != "" {
		drawNote(s, p.LeftX, 1, p.LeftW, h, v.Message, v.Styles)
		v.drawStatus(s, s.H-1)
		return
	}
	if v.failed() {
		drawSideErrors(s, p, 1, h, v.SideErrors, v.Styles)
		v.drawStatus(s, s.H-1)
		return
	}
	if len(v.Rows) == 0 {
		for _, x := range []int{p.LeftX, p.RightX} {
			s.Label(x, 1, p.LeftW, "empty", v.Styles.Plain.With(term.Dim))
		}
	}
	v.drawLines(s, p, 1, h, lines[v.Top:])
	v.drawStatus(s, s.H-1)
}

// drawLines draws up to h of lines from row y.
func (v *DiffView) drawLines(s *term.Screen, p Panes, y, h int, lines []vline) {
	for i := 0; i < h && i < len(lines); i++ {
		l := lines[i]
		if l.row < 0 {
			// The same text on both sides, which may differ by a column.
			text := separator(l.skip, min(p.LeftW, p.RightW))
			drawCentered(s, p, y+i, text, v.Styles)
			continue
		}
		v.drawRow(s, p, y+i, l)
	}
}

// drawCentered draws text dimmed, centered in each pane at row y.
func drawCentered(s *term.Screen, p Panes, y int, text string, st colors.Styles) {
	n := len([]rune(text))
	for i, x := range []int{p.LeftX, p.RightX} {
		w := []int{p.LeftW, p.RightW}[i]
		s.Label(x+max((w-n)/2, 0), y, w, text, st.Plain.With(term.Dim))
	}
}

// dotted puts text between dots, as a separator, if that fits in a pane
// w columns wide.
func dotted(text string, w int) string {
	if d := "··· " + text + " ···"; len([]rune(d)) <= w {
		return d
	}
	return text
}

// failed reports whether a side can't be shown.
func (v *DiffView) failed() bool {
	return v.SideErrors != [2]string{}
}

// viewingLeads start the boxes' titles in the diff view, telling it from
// the tree: the first, or, to leave room for the whole path, the second.
var viewingLeads = []string{"(viewing) ", "(v) "}

// separator returns the text of a separator for n unchanged lines left
// out, in a pane w columns wide: shorter if the full text doesn't fit.
func separator(n, w int) string {
	text := fmt.Sprintf("··· %d unchanged lines ···", n)
	if n == 1 {
		text = "··· 1 unchanged line ···"
	}
	if len([]rune(text)) > w {
		text = fmt.Sprintf("··· %d ···", n)
	}
	return text
}

func (v *DiffView) spans(i int) [2][]diff.Span {
	if v.inline == nil {
		v.inline = map[int][2][]diff.Span{}
	}
	if sp, ok := v.inline[i]; ok {
		return sp
	}
	r := v.Rows[i]
	a, b := diff.Inline(v.L.Lines[r.L], v.R.Lines[r.R])
	v.inline[i] = [2][]diff.Span{a, b}
	return v.inline[i]
}

// drawRow draws a visual line across both panes and the mark column,
// over the tint of its row's kind, in the colour of its mark, as the
// tree's rows.
func (v *DiffView) drawRow(s *term.Screen, p Panes, y int, l vline) {
	r := v.Rows[l.row]
	var spans [2][]diff.Span
	if r.Kind == diff.Changed {
		spans = v.spans(l.row)
	}
	st := v.Styles
	markStyle, bg := st.Plain, term.Style{}
	switch r.Kind {
	case diff.Changed:
		markStyle, bg = st.Changed, st.ChangedBg
	case diff.LeftOnly:
		markStyle, bg = st.LeftOnly, st.LeftOnlyBg
	case diff.RightOnly:
		markStyle, bg = st.RightOnly, st.RightOnlyBg
	}
	g := v.gutter()
	tw := v.textWidth(s.W)
	for side := range 2 {
		x, w, text, idx := p.LeftX, p.LeftW, v.L, r.L
		if side == 1 {
			x, w, text, idx = p.RightX, p.RightW, v.R, r.R
		}
		s.Fill(x, y, w, st.Plain.On(bg))
		if idx < 0 {
			continue
		}
		if l.part == 0 {
			num := strconv.Itoa(idx + 1)
			s.Label(x+g-len(num), y, len(num), num, st.Plain.With(term.Dim).On(bg))
		}
		// Only what is shown is turned into glyphs: from the part's start
		// with wrap, or the column scrolled to without.
		line := text.Lines[idx]
		var from display.Pos
		start := v.HScroll
		switch starts, ok := v.wraps[side][idx]; {
		case v.Wrap && ok && l.part < len(starts):
			from = starts[l.part]
			start = from.Column()
		case v.Wrap && l.part > 0:
			// The other side wraps into more parts.
			continue
		case v.Wrap:
			start = 0
		default:
			from = display.ContentSeek(line, start)
		}
		width := min(tw, w-g-1)
		glyphs := display.ContentFrom(line, from, start-from.Column()+width+1)
		v.drawText(s, x+g+1, y, width, glyphs, from.Column(), start, spans[side], bg)
	}
	gap := st.Plain.On(bg)
	s.Set(p.Mark-1, y, ' ', 1, gap)
	s.Set(p.Mark+1, y, ' ', 1, gap)
	if l.part == 0 {
		s.Set(p.Mark, y, r.Kind.Mark(), 1, markStyle.On(bg))
	} else {
		s.Set(p.Mark, y, ' ', 1, gap)
	}
}

// drawText draws the columns from start of glyphs, the first of which is
// at column col, in width columns, over the row's background bg, with
// changed spans in the inline style and escapes in the escape style. A
// wide glyph cut by start shows as a space.
func (v *DiffView) drawText(s *term.Screen, x, y, width int, glyphs []display.Glyph, col, start int, spans []diff.Span, bg term.Style) {
	base := v.Styles.Plain.On(bg)
	inline := base.On(v.Styles.Inline)
	esc := rowEscape(v.Styles.Escape, bg)
	si := 0
	for _, gl := range glyphs {
		w := int(gl.W)
		if col+w <= start {
			col += w
			continue
		}
		if col-start >= width {
			break
		}
		for si < len(spans) && spans[si].End <= int(gl.Src) {
			si++
		}
		st := base
		if si < len(spans) && spans[si].Start <= int(gl.Src) {
			st = inline
		}
		if gl.Esc {
			st = esc
		}
		switch {
		case col < start:
			// The right half of a wide glyph cut by the left edge.
			s.Set(x, y, ' ', 1, st)
		case col-start+w > width:
			s.Set(x+col-start, y, ' ', 1, st)
		default:
			s.Set(x+col-start, y, gl.R, w, st)
		}
		col += w
	}
}

// Counts returns the rows changed, and on the left or right only.
func (v *DiffView) Counts() (changed, left, right int) {
	for _, r := range v.Rows {
		switch r.Kind {
		case diff.Changed:
			changed++
		case diff.LeftOnly:
			left++
		case diff.RightOnly:
			right++
		}
	}
	return
}

func (v *DiffView) drawStatus(s *term.Screen, y int) {
	st := v.Styles
	var items []barItem
	add := func(text string, style term.Style) {
		items = append(items, barItem{text: text, style: style})
	}
	switch {
	case v.Notice != "":
		items = append(items, barItem{text: v.Notice, style: st.Status, note: true})
	case v.Message == "" && !v.failed():
		changed, left, right := v.Counts()
		if changed > 0 {
			add(fmt.Sprintf("%d changed", changed), st.Changed)
		}
		if left > 0 {
			add(fmt.Sprintf("%d left", left), st.LeftOnly)
		}
		if right > 0 {
			add(fmt.Sprintf("%d right", right), st.RightOnly)
		}
		switch {
		case changed+left+right > 0:
		case v.L.CRLF != v.R.CRLF:
			add("only line ends differ", st.Changed)
		default:
			add("no differences", st.Plain)
		}
		for _, n := range []struct {
			side string
			t    diff.Text
		}{{"left", v.L}, {"right", v.R}} {
			if n.t.CRLF {
				add(n.side+": CRLF", st.Status)
			}
		}
		if v.Wrap {
			add("wrap", st.Status)
		}
		if v.Full {
			add("full", st.Status)
		}
		if len(v.lines) > 0 {
			bottom := min(v.Top+rowsHeight(s.H), len(v.lines))
			add(fmt.Sprintf("%d%%", bottom*100/len(v.lines)), st.Status)
		}
	}
	drawBar(s, y, items, st)
}
