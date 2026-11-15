// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"pxy.se/go/binate/internal/display"
)

// EditTitle is the Edit popup's title.
const EditTitle = "Select side to edit"

// EditPopup asks which side to edit: a box like Help listing both paths
// after their keys, set apart by spaces, and the left one selected.
type EditPopup struct {
	*Help
}

// NewEditPopup returns the popup for the paths left and right, with home
// shown as ~.
func NewEditPopup(home, left, right string) *EditPopup {
	h := NewHelp([]HelpLine{
		{Text: EditTitle},
		{},
		{Keys: []string{"h", "←"}, Path: display.Name(HomePath(left, home))},
		{Keys: []string{"l", "→"}, Path: display.Name(HomePath(right, home))},
	})
	h.Plain = true
	return &EditPopup{h}
}

// Side returns the selected side: 0 for the left, 1 for the right.
func (e *EditPopup) Side() int {
	return e.Selected - 2
}
