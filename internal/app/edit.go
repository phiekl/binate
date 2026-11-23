// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/supervise"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/view"
)

// EditSide is a side as e sees it.
type EditSide struct {
	// Path is shown in the popup.
	Path string
	// Rel is the path relative to the side's root, sent to the parent.
	Rel string
	// Why is why the side can't be edited, or empty if it can.
	Why string
}

// whyNotEditable says why a tree entry's side of kind k in root can't be
// edited: only an existing regular file in the file system can.
func whyNotEditable(root source.Root, k scan.Kind) string {
	if k == scan.Missing {
		return "missing"
	}
	if why := editRestriction(root); why != "" {
		return why
	}
	switch k {
	case scan.File:
		return ""
	case scan.Dir:
		return "a directory"
	case scan.Symlink:
		return "a symlink"
	}
	return notRegular
}

// Titles of the error popup: when no side can be edited, when editing
// failed, and when an entry of the tree doesn't open in the diff view.
const (
	unableToEdit = "Unable to edit"
	editFailed   = "Failed to edit"
	unableToOpen = "Unable to open"
)

// notRegular is why a side can't be edited, when nothing more specific is.
const notRegular = "not a regular file"

// notOnDisk is why a side not in the file system, as one in a git
// revision, can't be edited.
const notOnDisk = "in a git revision"

// inIndex is why a side in the git index, which is no revision, can't be
// edited.
const inIndex = "in the git index"

const readOnlyWorkTree = "in the read-only work tree"

// inArchive is why a side in an archive, as a tar file, can't be edited.
const inArchive = "in an archive"

// noFile reports whether why is that of a side in a git revision, the
// index or an archive, which no editor can edit.
func noFile(why string) bool {
	return why == notOnDisk || why == inIndex || why == inArchive
}

// onDisk reports whether root is in the file system.
func onDisk(root source.Root) bool {
	_, ok := root.(source.DiskRoot)
	return ok
}

// isArchive reports whether root is an archive, which records what a file
// system does but holds no files on disk.
func isArchive(root source.Root) bool {
	_, ok := root.(source.Archive)
	return ok
}

// inRevision reports whether root is a revision of a version control
// system, as git's, or its index, which records neither owners nor
// times.
func inRevision(root source.Root) bool {
	return !onDisk(root) && !isArchive(root)
}

func editRestriction(root source.Root) string {
	if x, ok := root.(source.Index); ok && x.IsIndex() {
		return inIndex
	}
	if isArchive(root) {
		return inArchive
	}
	disk, ok := root.(source.DiskRoot)
	if !ok {
		return notOnDisk
	}
	if !disk.Editable() {
		return readOnlyWorkTree
	}
	return ""
}

// EditDone is called after the editor exits. An error editing has been
// shown in the error popup by then.
type EditDone func()

// editor is an edit in progress: the popup, then the request.
type editor struct {
	app   *App
	sides [2]EditSide
	popup *view.EditPopup
	done  EditDone
}

// Edit starts editing one of sides. With either editable, it returns the
// popup's editor, the popup listing only those that are, so that none
// seems edited that isn't, and why the other isn't. With none, or no editor set, it returns why,
// for the error popup: sides that can't be edited for different reasons
// as a list.
func (a *App) Edit(sides [2]EditSide, home string, done EditDone) (*editor, []string) {
	// Setting an editor wouldn't help.
	if sides[0].Why == notOnDisk && sides[1].Why == notOnDisk {
		return nil, []string{"Files in git revisions can't be edited."}
	}
	if noFile(sides[0].Why) && noFile(sides[1].Why) {
		return nil, []string{"Neither side can be edited:", "The left side is " + sides[0].Why + ".", "The right side is " + sides[1].Why + "."}
	}
	if sides[0].Why != "" && sides[1].Why != "" &&
		(sides[0].Why == readOnlyWorkTree || sides[1].Why == readOnlyWorkTree) {
		return nil, []string{"Files in the work tree can't be edited in this comparison."}
	}
	if !a.HasEditor {
		return nil, []string{"Environment variable VISUAL or EDITOR must be set."}
	}
	if sides[0].Why == "" || sides[1].Why == "" {
		return &editor{app: a, sides: sides, done: done,
			popup: view.NewEditPopup(home, sides[0].Path, sides[1].Path, [2]string{sides[0].Why, sides[1].Why})}, nil
	}
	why := "Neither side is a regular file"
	l, r := sides[0].Why, sides[1].Why
	if noFile(l) || noFile(r) {
		return nil, []string{"Neither side can be edited:", "The left side is " + l + ".", "The right side is " + r + "."}
	}
	if l == r || l == notRegular || r == notRegular {
		return nil, []string{why + "."}
	}
	return nil, []string{why + ":", "The left side is " + l + ".", "The right side is " + r + "."}
}

// Key handles a key while the popup is open, and reports whether it's
// still open: h and l, or Left and Right, edit a side the popup lists, the
// keys that move through Help move the selection, and Enter or Space edit
// the selected side. Other keys are ignored.
func (e *editor) Key(k term.Key) bool {
	switch {
	case k == term.Key{Code: term.KeyEnter} || k == term.Rune(' '):
		e.run(e.popup.Side())
		return false
	case listKey(e.popup.Help, e.app.screen, k):
	case (k == term.Rune('h') || k == term.Key{Code: term.KeyLeft}) && e.popup.Offers(0):
		e.run(0)
		return false
	case (k == term.Rune('l') || k == term.Key{Code: term.KeyRight}) && e.popup.Offers(1):
		e.run(1)
		return false
	case k == term.Rune('e') || k.Code == term.KeyEsc:
		return false
	}
	return true
}

// Draw draws the popup.
func (e *editor) Draw(s *term.Screen, st colors.Styles) {
	e.popup.Draw(s, st)
}

// run has the parent edit side i, with the terminal released, and calls
// done afterwards.
func (e *editor) run(i int) {
	side := supervise.Left
	if i == 1 {
		side = supervise.Right
	}
	es := e.sides[i]
	err := e.app.Release(func() error {
		return e.app.client.Edit(side, es.Rel)
	})
	if err != nil {
		e.app.ShowError(editFailed, errorTexts(err)...)
	}
	if e.done != nil {
		e.done()
	}
}
