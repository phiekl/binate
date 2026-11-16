// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"pxy.se/go/binate/internal/display"
)

// EditTitle is the Edit popup's title.
const EditTitle = "Select side to edit"

// SidePopup asks which side to act on, as the Edit popup does: a box like
// Help listing what each side is after its keys, the left one selected.
type SidePopup struct {
	*Help
}

// EditPopup asks which side to edit, listing both paths.
type EditPopup = SidePopup

// NewEditPopup returns the popup for the paths left and right, with home
// shown as ~.
func NewEditPopup(home, left, right string) *EditPopup {
	return NewSidePopup(EditTitle, display.Name(HomePath(left, home)), display.Name(HomePath(right, home)))
}

// NewSidePopup returns the popup titled title for sides shown as left and
// right, each shortened in the middle if it doesn't fit.
func NewSidePopup(title string, left, right []display.Glyph) *SidePopup {
	h := NewHelp([]HelpLine{
		{Text: title},
		{},
		{Keys: []string{"h", "←"}, Path: left},
		{Keys: []string{"l", "→"}, Path: right},
	})
	h.Plain = true
	return &SidePopup{h}
}

// Side returns the selected side: 0 for the left, 1 for the right.
func (e *SidePopup) Side() int {
	return e.Selected - 2
}
