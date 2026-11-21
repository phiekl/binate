// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"fmt"

	"pxy.se/go/binate/internal/term"
)

// ActionsTitle is the Actions popup's title.
const ActionsTitle = "Actions"

// actionsFooter says how to use the Actions popup.
const actionsFooter = "Enter or a key runs, Esc closes"

// ActionRow is a row of the Actions popup: an action, the key that runs
// it, and what it does. Why, if set, says why it can't run on the
// selected entry, and dims the row.
type ActionRow struct {
	Key  rune
	Name string
	What string
	Why  string
}

// firstActionRow is the index in Lines of the first row.
const firstActionRow = 2

// ActionPopup chooses an action to run on the selected entry: a box like
// Help listing each action with its key.
type ActionPopup struct {
	*Help
	Rows []ActionRow
}

// NewActionPopup returns the popup listing rows, with the first selected.
func NewActionPopup(rows []ActionRow) *ActionPopup {
	a := &ActionPopup{Help: &Help{Plain: true}, Rows: rows}
	width := 0
	for _, r := range rows {
		width = max(width, len([]rune(r.Name)))
	}
	lines := []HelpLine{{Text: ActionsTitle}, {}}
	for _, r := range rows {
		lines = append(lines, HelpLine{
			Keys: []string{string(r.Key)},
			Text: fmt.Sprintf("%-*s  %s", width, r.Name, r.What),
			Dim:  r.Why != "",
		})
	}
	a.Lines = append(lines, HelpLine{}, HelpLine{Text: actionsFooter})
	a.Selected = firstActionRow
	return a
}

// Row returns the index in Rows of the selected row.
func (a *ActionPopup) Row() int {
	return a.Selected - firstActionRow
}

// SelectKey selects the row whose key is r, and reports whether there is
// one.
func (a *ActionPopup) SelectKey(r rune) bool {
	for i, row := range a.Rows {
		if row.Key == r {
			a.Selected = firstActionRow + i
			return true
		}
	}
	return false
}

// ActionResult is what an action found of the selected entry, shown in
// place of the preview, or of the panels where there is no preview.
type ActionResult struct {
	// Title names the action, in the divider above the result.
	Title string
	Sides [2]ActionSide
}

// ActionSide is an action's result on one side.
type ActionSide struct {
	// Value is the result, broken into lines of the pane's width. Link,
	// if set, links it all.
	Value string
	Link  string
	// Note, shown dimmed instead of a value, says what is being done, or
	// why there is no value, as for a side missing.
	Note string
	// Err, shown in red instead, says why the action failed.
	Err string
}

// Mark returns the mark between the sides: = if both have a value and
// they are the same, | if they differ, and 0 otherwise.
func (r *ActionResult) Mark() rune {
	l, rt := r.Sides[0].Value, r.Sides[1].Value
	switch {
	case l == "" || rt == "":
		return 0
	case l == rt:
		return '='
	}
	return '|'
}

// valueLines breaks value into lines of w characters. Beyond h lines,
// the last ends in … instead.
func valueLines(value string, w, h int) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	r := []rune(value)
	var lines []string
	for len(r) > 0 {
		n := min(w, len(r))
		lines = append(lines, string(r[:n]))
		r = r[n:]
	}
	if len(lines) > h {
		lines = lines[:h]
		last := []rune(lines[h-1])
		if len(last) >= w {
			last = last[:w-1]
		}
		lines[h-1] = string(last) + "…"
	}
	return lines
}

// drawAction draws the result res in h rows from row y, below a divider
// at row y-1 titled with the action's name.
func (v *TreeView) drawAction(s *term.Screen, p Panes, y, h int, res *ActionResult) {
	st := v.Styles
	for i, x := range []int{p.LeftBox, p.RightBox} {
		if w := []int{p.LeftBoxW, p.RightBoxW}[i]; w > 6 {
			s.Label(x+2, y-1, w-4, " "+res.Title+" ", st.Border.With(term.Bold))
		}
	}
	for i, x := range []int{p.LeftX, p.RightX} {
		w := []int{p.LeftW, p.RightW}[i]
		sd := res.Sides[i]
		switch {
		case sd.Err != "":
			drawNoteIn(s, x, y, w, h, sd.Err, st.Error, st.Escape)
		case sd.Value == "":
			drawNoteIn(s, x, y, w, h, sd.Note, st.Plain.With(term.Dim), st.Escape)
		default:
			style := st.Plain
			style.Link = sd.Link
			for j, line := range valueLines(sd.Value, w, h) {
				s.Label(x, y+j, w, line, style)
			}
		}
	}
	switch res.Mark() {
	case '=':
		s.Label(p.Mark, y, 1, "=", st.Plain)
	case '|':
		s.Label(p.Mark, y, 1, "|", st.Changed)
	}
}
