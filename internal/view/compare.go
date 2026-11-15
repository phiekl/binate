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

// compareFooter says how to use the Compare popup.
const compareFooter = "Space or a letter toggles, * toggles all, Esc closes"

// compareRows are the Compare popup's rows, in order, with what each
// compares.
var compareRows = []struct {
	attr scan.Attrs
	name string
	// key toggles the row; it is a letter of name.
	key  rune
	what string
}{
	{scan.Left, "left", 'l', "entries on the left only (<)"},
	{scan.Right, "right", 'r', "entries on the right only (>)"},
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
}

// NewComparePopup returns the popup for the attributes compare and quick
// mode, with the first row selected.
func NewComparePopup(compare scan.Attrs, quick bool) *ComparePopup {
	c := &ComparePopup{&Help{Plain: true}}
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
		if r.attr == scan.Content && compare&scan.Content != 0 && quick {
			l.Keys[0] = "[q]"
			l.Text = fmt.Sprintf("%-9s %s", r.name, "files' size and mtime only (quick)")
		}
		lines = append(lines, l)
	}
	c.Lines = append(lines, HelpLine{}, HelpLine{Text: compareFooter})
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
