// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"strings"
	"time"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/view"
)

// Titles of the error popup when no commit can be shown, and when showing
// one failed.
const (
	unableToShow = "Unable to show the commit"
	showFailed   = "Failed to show the commit"
)

// commitSide is a side's commit that last changed its entry, as shown by m
// and d, or why there is none.
type commitSide struct {
	root source.Changes
	// rel is the entry's path in root.
	rel string
	ch  source.Change
	why string
}

// commitSides returns the sides' commits from the changes read for key,
// as res, if done; else why not, for the status bar.
func commitSides(key changeKey, res [2]changeResult, done bool) ([2]commitSide, string) {
	var sides [2]commitSide
	if key == (changeKey{}) {
		sides[0].why, sides[1].why = notInCommit, notInCommit
		return sides, ""
	}
	if !done {
		return sides, "still finding the commit that last changed it"
	}
	for i, sk := range key.sides {
		c, ok := sk.root.(source.Changes)
		switch {
		case sk.root == nil || !ok:
			sides[i].why = notInCommit
		case res[i].err != nil:
			sides[i].why = "without a commit found: " + errorText(files.UnwrapPath(res[i].err))
		default:
			sides[i] = commitSide{root: c, rel: sk.rel, ch: res[i].ch}
		}
	}
	return sides, ""
}

// notInCommit is why a side has no commit: it is missing, or not in a git
// commit.
const notInCommit = "not in a git commit"

// commitPicker is the popup choosing whose commit to show, open until a
// side is chosen or it is closed.
type commitPicker struct {
	popup *view.SidePopup
	sides [2]commitSide
	run   func(commitSide)
	// closed, if set, is called once Esc closes it.
	closed func()
}

// pickCommit runs run on one of sides' commits: at once if only one has
// one, or both the same, with a note on which if they differ; else it
// returns the popup choosing. With none, it returns why, for the error
// popup.
func pickCommit(sides [2]commitSide, title string, run func(cs commitSide, note string)) (*commitPicker, []string) {
	l, r := sides[0], sides[1]
	switch {
	case l.root != nil && r.root != nil && l.ch.ID == r.ch.ID:
		run(l, "")
	case l.root != nil && r.root != nil:
		return &commitPicker{
			popup: view.NewSidePopup(title, commitLabel(l.ch), commitLabel(r.ch)),
			sides: sides,
			run:   func(cs commitSide) { run(cs, "") },
		}, nil
	case l.root != nil:
		run(l, "the left side's commit, as the right side is "+r.why)
	case r.root != nil:
		run(r, "the right side's commit, as the left side is "+l.why)
	case l.why == r.why:
		return nil, []string{"Neither side is " + strings.TrimPrefix(l.why, "not ") + "."}
	default:
		return nil, []string{"Neither side has a commit:", "The left side is " + l.why + ".", "The right side is " + r.why + "."}
	}
	return nil, nil
}

// commitLabel shows a commit in the popup: abbreviated, and its subject.
func commitLabel(ch source.Change) []display.Glyph {
	return display.Name(ch.ID[:min(len(ch.ID), 12)] + " " + ch.Subject)
}

// Key handles a key while the popup is open, and reports whether it's
// still open: h and l, or Left and Right, choose a side, the keys that
// move through Help move the selection, Enter or Space choose the selected
// side, and Esc closes it.
func (p *commitPicker) Key(s *term.Screen, k term.Key) bool {
	switch {
	case k == term.Key{Code: term.KeyEnter} || k == term.Rune(' '):
		p.choose(p.popup.Side())
		return false
	case listKey(p.popup.Help, s, k):
	case k == term.Rune('h') || k == term.Key{Code: term.KeyLeft}:
		p.choose(0)
		return false
	case k == term.Rune('l') || k == term.Key{Code: term.KeyRight}:
		p.choose(1)
		return false
	case k.Code == term.KeyEsc:
		if p.closed != nil {
			p.closed()
		}
		return false
	}
	return true
}

// choose runs the side i, selected should the popup open again.
func (p *commitPicker) choose(i int) {
	p.popup.Selected = 2 + i
	p.run(p.sides[i])
}

// Draw draws the popup.
func (p *commitPicker) Draw(s *term.Screen, st colors.Styles) {
	p.popup.Draw(s, st)
}

// Titles of the popups choosing whose commit the commit actions show.
const (
	messageTitle = "Select side's commit to show"
	treesTitle   = "Select side's commit to compare"
)

// commitView opens the views of a commit from a screen: its message, or
// its trees, going back to the screen with Esc.
type commitView struct {
	app    *App
	styles colors.Styles
	home   string
	// opts and context are the scan options and context lines of the
	// commit's trees.
	opts    scan.Options
	context int
	back    func()
	// repick opens the popup choosing a side again on the screen, as when
	// going back from a side chosen in it, to choose the other.
	repick func(*commitPicker)
	// reopen opens the Actions popup again on the screen, as when going
	// back from the action run from it.
	reopen func()
}

// commitAction is an action of the Actions popup, in git mode, on the
// commit that last changed the selected entry: showing it, or comparing
// the trees before and after it.
type commitAction struct {
	key        rune
	name, what string
	trees      bool
}

// commitActions are the commit actions, in the popup's order, after the
// other actions.
var commitActions = []commitAction{
	{key: 'm', name: "Commit message", what: "commit that last changed it, as git show"},
	{key: 'd', name: "Commit diff", what: "trees before and after that commit", trees: true},
}

// hasCommits reports whether any of roots has commits to show, as in git
// mode a commit's tree has.
func hasCommits(roots ...source.Root) bool {
	for _, r := range roots {
		if c, ok := r.(source.Changes); ok && c.HasHistory() {
			return true
		}
	}
	return false
}

// commitRows returns the Actions popup's rows of the commit actions on
// the entry whose changes ckey asks for, dimmed if it has none.
func commitRows(ckey changeKey) []view.ActionRow {
	rows := make([]view.ActionRow, len(commitActions))
	for i, a := range commitActions {
		rows[i] = view.ActionRow{Key: a.key, Name: a.name, What: a.what}
		if ckey == (changeKey{}) {
			rows[i].Why = a.name + " needs a side in a git commit"
		}
	}
	return rows
}

// start runs the commit action a on the commit that last changed a side,
// of those requested as ckey from changes, chosen if the sides' differ. It
// returns the popup choosing, if open, or a note for the status bar while
// the commits are still being found.
func (c commitView) start(a commitAction, ckey changeKey, changes *changeReader) (*commitPicker, string) {
	var res [2]changeResult
	done := true
	if ckey != (changeKey{}) {
		res, done = changes.read(ckey)
	}
	sides, wait := commitSides(ckey, res, done)
	if wait != "" {
		return nil, wait
	}
	show, title := commitView.showMessage, messageTitle
	if a.trees {
		show, title = commitView.showTrees, treesTitle
	}
	// Going back from a commit shown at once, or closing the popup
	// choosing a side, opens the Actions popup again.
	direct := c
	if c.reopen != nil {
		direct.back = func() {
			c.back()
			c.reopen()
		}
	}
	picker, why := pickCommit(sides, title, func(cs commitSide, note string) { show(direct, cs, note) })
	if picker != nil {
		picker.closed = c.reopen
	}
	if picker != nil && c.repick != nil {
		// Going back from the side chosen opens the popup again.
		picker.run = func(cs commitSide) {
			cv := c
			cv.back = func() {
				c.back()
				c.repick(picker)
			}
			show(cv, cs, "")
		}
	}
	if why != nil {
		c.app.ShowError(unableToShow, why...)
	}
	return picker, ""
}

// showMessage shows the commit cs as "git show" does, without its diff,
// with note in the status bar.
func (c commitView) showMessage(cs commitSide, note string) {
	text, err := cs.root.ShowChange(cs.ch.ID)
	if err != nil {
		c.app.ShowError(showFailed, errorTexts(err)...)
		return
	}
	v := &view.TextView{
		Styles: c.styles, Lead: "(commit) ", Title: commitLabel(cs.ch),
		Lines: strings.Split(strings.TrimSuffix(text, "\n"), "\n"),
	}
	if note != "" {
		v.Notice = "showing " + note
	}
	c.app.Main = &TextScreen{app: c.app, view: v, back: c.back}
}

// showTrees compares the trees before and after the commit cs, its first
// parent's with its own, with note in the status bar, and the entry
// selected in them.
func (c commitView) showTrees(cs commitSide, note string) {
	before, after, parents, err := cs.root.ChangeTrees(cs.ch.ID)
	if err != nil {
		c.app.ShowError(showFailed, errorTexts(err)...)
		return
	}
	t := NewTreeScreen(c.app, TreeConfig{
		Styles: c.styles, Home: c.home, Left: before, Right: after,
		Options: c.opts, Context: c.context, Back: c.back,
	})
	if p, ok := cs.root.RepoPath(cs.rel); ok && p != "." {
		t.selectPath = p
	}
	switch {
	case note != "":
		t.message = "comparing " + note
	case parents > 1:
		t.message = fmt.Sprintf("comparing with the first of %d parents", parents)
	}
	c.app.Main = t
}

// TextScreen shows a text, as a commit's message.
type TextScreen struct {
	app  *App
	view *view.TextView
	help *view.Help
	last *term.Screen
	back func()
	// pendingG is set after g, which goes to the start if repeated.
	pendingG bool
}

// Draw draws the text, and Help over it if open.
func (t *TextScreen) Draw(s *term.Screen) {
	t.last = s
	t.view.Draw(s)
	if t.help != nil {
		t.help.Draw(s, t.view.Styles)
	}
}

// Tick does nothing: the text doesn't change.
func (t *TextScreen) Tick() time.Duration { return -1 }

// Key handles a key.
func (t *TextScreen) Key(k term.Key) bool {
	v := t.view
	v.Notice = ""
	if t.help != nil {
		return helpKey(&t.help, t.last, k)
	}
	g := t.pendingG
	t.pendingG = false
	switch {
	case k == term.Rune('j') || k == term.Key{Code: term.KeyDown}:
		v.Scroll(1)
	case k == term.Rune('k') || k == term.Key{Code: term.KeyUp}:
		v.Scroll(-1)
	case k == term.Key{Code: term.KeyPgDn} || k == term.Ctrl('f'):
		v.Scroll(v.Page())
	case k == term.Key{Code: term.KeyPgUp} || k == term.Ctrl('b'):
		v.Scroll(-v.Page())
	case k == term.Key{Code: term.KeyHome} || k == term.Rune('g') && g:
		v.ScrollTo(true)
	case k == term.Key{Code: term.KeyEnd} || k == term.Rune('G'):
		v.ScrollTo(false)
	case k == term.Rune('g'):
		t.pendingG = true
	case k == term.Rune('?'):
		t.help = view.NewHelp(view.TextHelp)
	case k.Code == term.KeyEsc:
		t.back()
	default:
		return false
	}
	return true
}
