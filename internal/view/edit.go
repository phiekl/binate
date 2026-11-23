// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"slices"

	"pxy.se/go/binate/internal/display"
)

// EditTitle is the Edit popup's title.
const EditTitle = "Select side to edit"

// SidePopup asks which side to act on, as the Edit popup does: a box like
// Help listing what each side offered is after its keys, the first one
// selected, and why a side isn't offered, dimmed, below.
type SidePopup struct {
	*Help
	// sides are the sides of the rows after the title, 0 for the left.
	sides []int
}

// EditPopup asks which side to edit, listing the paths of those that can
// be.
type EditPopup = SidePopup

// NewEditPopup returns the popup for the paths left and right, with home
// shown as ~, listing those whose why, why they can't be edited, is empty.
func NewEditPopup(home, left, right string, why [2]string) *EditPopup {
	return NewSidePopup(EditTitle, display.Name(HomePath(left, home)), display.Name(HomePath(right, home)), why)
}

// NewSidePopup returns the popup titled title for sides shown as left and
// right, each shortened in the middle if it doesn't fit, listing those
// whose why is empty: a side with a why is left out, and why is said
// instead, as "The left side is missing.".
func NewSidePopup(title string, left, right []display.Glyph, why [2]string) *SidePopup {
	keys := [2][]string{{"h", "←"}, {"l", "→"}}
	names := [2][]display.Glyph{left, right}
	lines := []HelpLine{{Text: title}, {}}
	var sides []int
	for i := range 2 {
		if why[i] == "" {
			lines = append(lines, HelpLine{Keys: keys[i], Path: names[i]})
			sides = append(sides, i)
		}
	}
	for i, side := range [2]string{"left", "right"} {
		if why[i] != "" {
			lines = append(lines, HelpLine{Text: "The " + side + " side is " + why[i] + ".", Dim: true})
		}
	}
	h := NewHelp(lines)
	h.Plain = true
	return &SidePopup{Help: h, sides: sides}
}

// Side returns the selected side: 0 for the left, 1 for the right.
func (e *SidePopup) Side() int {
	return e.sides[e.Selected-2]
}

// Select selects side i, if listed.
func (e *SidePopup) Select(i int) {
	if j := slices.Index(e.sides, i); j >= 0 {
		e.Selected = 2 + j
	}
}

// Offers reports whether the popup lists side i, 0 for the left.
func (e *SidePopup) Offers(i int) bool {
	return slices.Contains(e.sides, i)
}
