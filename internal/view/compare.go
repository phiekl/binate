// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"fmt"
	"strings"

	"pxy.se/go/binate/internal/scan"
)

// CompareTitle is the Compare popup's title.
const CompareTitle = "Compare"

// compareFooter says how to use the Compare popup, starting with what *
// does, which a narrow box would otherwise cut off.
const compareFooter = "* turns all %s, Space or a letter toggles, Esc closes"

// CompareAll returns what * makes compare, of sides that don't record
// the attributes unrecorded: nothing if anything is compared, and
// everything recorded if nothing is.
func CompareAll(compare, unrecorded scan.Attrs) scan.Attrs {
	all := scan.AllAttrs &^ unrecorded
	if compare&all != 0 {
		return 0
	}
	return all
}

// compareRows are the Compare popup's rows, in order, with what each
// compares.
var compareRows = []struct {
	attr scan.Attrs
	name string
	// key toggles the row; it is a letter of name.
	key  rune
	what string
}{
	{scan.Left, "left", 'l', "entries on the left only (-)"},
	{scan.Right, "right", 'r', "entries on the right only (+)"},
	{scan.Type, "type", 'y', "entries of different types (!)"},
	{scan.Content, "content", 'c', "files' size and contents"},
	{scan.Target, "target", 'a', "symlinks' targets"},
	{scan.Device, "device", 'd', "devices' numbers"},
	{scan.Mode, "mode", 'm', "permission bits"},
	{scan.Owner, "owner", 'o', "user and group"},
	{scan.Mtime, "mtime", 't', "modification time"},
}

// firstCompareRow is the index in Lines of the first row.
const firstCompareRow = 2

// ComparePopup chooses what the tree compares: a box like Help listing
// each attribute, checked if compared, and entries on one side only or of
// different types, checked if shown; the contents may be compared quickly.
type ComparePopup struct {
	*Help
	// unrecorded are the attributes the sides don't record, as a git
	// revision has no owners or times: shown as such, and never compared.
	unrecorded scan.Attrs
}

// NewComparePopup returns the popup for the attributes compare and quick
// mode, with the first row selected, of sides that don't record the
// attributes unrecorded.
func NewComparePopup(compare scan.Attrs, quick bool, unrecorded scan.Attrs) *ComparePopup {
	c := &ComparePopup{&Help{Plain: true}, unrecorded}
	c.Set(compare, quick)
	c.Selected = firstCompareRow
	return c
}

// Set shows compare and quick mode, keeping the selection.
func (c *ComparePopup) Set(compare scan.Attrs, quick bool) {
	lines := []HelpLine{{Text: CompareTitle}, {}}
	for _, r := range compareRows {
		box := "[ ]"
		if compare&r.attr != 0 {
			box = "[x]"
		}
		l := HelpLine{Keys: []string{box}, Text: fmt.Sprintf("%-9s %s", r.name, r.what), Hot: strings.IndexRune(r.name, r.key) + 1}
		if r.attr&c.unrecorded != 0 {
			l.Keys[0] = "[-]"
			l.Text = fmt.Sprintf("%-9s %s", r.name, "not recorded in git revisions")
		}
		if r.attr == scan.Content && compare&scan.Content != 0 && quick {
			l.Keys[0] = "[q]"
			l.Text = fmt.Sprintf("%-9s %s", r.name, "files' size and mtime only (quick)")
		}
		lines = append(lines, l)
	}
	star := "on"
	if CompareAll(compare, c.unrecorded) == 0 {
		star = "off"
	}
	c.Lines = append(lines, HelpLine{}, HelpLine{Text: fmt.Sprintf(compareFooter, star)})
}

// Attr returns the selected row's attribute.
func (c *ComparePopup) Attr() scan.Attrs {
	return compareRows[c.Selected-firstCompareRow].attr
}

// SelectKey selects the row whose key is r, and reports whether there is
// one.
func (c *ComparePopup) SelectKey(r rune) bool {
	for i, row := range compareRows {
		if row.key == r {
			c.Selected = firstCompareRow + i
			return true
		}
	}
	return false
}
