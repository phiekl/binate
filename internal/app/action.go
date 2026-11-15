// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/view"
)

// action is something done to the selected entry, chosen in the Actions
// popup, whose result on each side is shown in place of the preview.
//
// To add one, define it in a file of its own, as sha256.go does, and add
// it to actions. This file runs actions and shows their results; an action
// only says what it accepts and computes one side's result.
type action struct {
	// key runs the action from the popup; name names it there and over
	// its result, and what says what it does.
	key  rune
	name string
	what string
	// accepts reports whether the action runs on a side of kind k, and
	// needs names what it accepts, to say why it can't run.
	accepts func(k scan.Kind) bool
	needs   string
	// run returns the action's result on a side's entry at path, which
	// is rel from its root, reporting to side as it goes. It stops early
	// once ctx is done.
	run func(ctx context.Context, path, rel string, side *sideRun) view.ActionSide
	// cache keeps each side's result while the file is unchanged, for
	// an action whose result depends on the file alone and that reports
	// the file it reads to sideRun.Read (see actioncache.go).
	cache bool
}

// sideRun is what an action's run on a side reports to as it goes.
type sideRun struct {
	// progress is told how much is done, as a fraction.
	progress func(float64)
	// read, if set, is told of the file the result is computed from.
	read func(*os.File)
}

// Progress reports that the fraction f of the work is done.
func (s *sideRun) Progress(f float64) {
	if s.progress != nil {
		s.progress(f)
	}
}

// Read reports the file the result is computed from, once opened and
// again once read through, so that a cache can tell it is the file it
// checked, and unchanged while read. A result whose file isn't reported
// both times is not cached.
func (s *sideRun) Read(f *os.File) {
	if s.read != nil {
		s.read(f)
	}
}

// actions are the actions, in the popup's order.
var actions = []action{
	sha256Action,
	sha384Action,
	sha512Action,
}

// why says why a cannot run on n, or returns "" if it can: if neither
// side is of a kind it accepts.
func (a *action) why(n *scan.Node) string {
	if n == nil {
		return "nothing is selected"
	}
	if a.accepts(n.L.Kind) || a.accepts(n.R.Kind) {
		return ""
	}
	return a.name + " needs " + a.needs + " on a side"
}

// actionRun is an action running, or done, on the selected entry. It is
// dropped once the selection moves or the entry may have changed.
type actionRun struct {
	act    *action
	path   string
	gen    int
	cancel context.CancelFunc

	mu      sync.Mutex
	sides   [2]view.ActionSide
	running int
}

// actionRefresh is how often the screen shows an action's progress.
const actionRefresh = 100 * time.Millisecond

// runAction starts a on the selected entry n, whose sides are at paths,
// waking the app as each side is done. The scan's lock must be held.
func (t *TreeScreen) runAction(a *action, n *scan.Node) {
	t.stopAction()
	ctx, cancel := context.WithCancel(context.Background())
	run := &actionRun{act: a, path: n.Path(), gen: t.statGen, cancel: cancel}
	// Both sides are set up before either starts, as a side done at once
	// would otherwise change run while the other is set up.
	var paths [2]string
	for i := range 2 {
		kind := n.Side(i == 1).Kind
		switch {
		case kind == scan.Missing:
			run.sides[i].Note = "missing"
		case !a.accepts(kind):
			run.sides[i].Note = "not " + a.needs
		default:
			run.sides[i].Note = "reading…"
			run.running++
			paths[i] = t.sidePath(n, i == 1)
		}
	}
	for i, path := range paths {
		if path == "" {
			continue
		}
		go func() {
			res := t.results.run(ctx, a, path, run.path, func(f float64) {
				run.mu.Lock()
				run.sides[i].Note = fmt.Sprintf("reading… %d%%", int(f*100))
				run.mu.Unlock()
			})
			run.mu.Lock()
			// A cancelled run is dropped, and its result not shown.
			if ctx.Err() == nil {
				run.sides[i] = res
			}
			run.running--
			run.mu.Unlock()
			if t.app != nil {
				t.app.Wake()
			}
		}()
	}
	t.action = run
}

// stopAction drops the action's result, stopping it if it runs.
func (t *TreeScreen) stopAction() {
	if t.action != nil {
		t.action.cancel()
		t.action = nil
	}
}

// actionResult returns what the action shows, or nil if none runs on
// the selected entry n, dropping one for an entry no longer selected or
// that may have changed since. The scan's lock must be held.
func (t *TreeScreen) actionResult(n *scan.Node) *view.ActionResult {
	run := t.action
	if run == nil {
		return nil
	}
	if n == nil || n.Path() != run.path || t.statGen != run.gen {
		t.stopAction()
		return nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return &view.ActionResult{Title: run.act.name, Sides: run.sides}
}

// actionRunning reports whether an action is still running.
func (t *TreeScreen) actionRunning() bool {
	if t.action == nil {
		return false
	}
	t.action.mu.Lock()
	defer t.action.mu.Unlock()
	return t.action.running > 0
}

// openActions opens the Actions popup for the selected entry. The scan's
// lock must be held.
func (t *TreeScreen) openActions() {
	n := t.model.SelectedNode()
	rows := make([]view.ActionRow, len(actions))
	for i := range actions {
		a := &actions[i]
		rows[i] = view.ActionRow{Key: a.key, Name: a.name, What: a.what, Why: a.why(n)}
	}
	t.actions = view.NewActionPopup(rows)
}

// actionKey handles a key in the Actions popup. The scan's lock must be
// held.
func (t *TreeScreen) actionKey(k term.Key) {
	p := t.actions
	switch {
	case listKey(p.Help, t.last, k):
	case k.Code == term.KeyEnter || k.Code == term.KeyRune && !k.Alt && p.SelectKey(k.Rune):
		t.actions = nil
		a := &actions[p.Row()]
		n := t.model.SelectedNode()
		if why := a.why(n); why != "" {
			t.message = why
			return
		}
		t.runAction(a, n)
	case k.Code == term.KeyEsc:
		t.actions = nil
	}
}
