// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/view"
)

// fakeChanges is a root with a history whose changes are shown as their
// IDs.
type fakeChanges struct {
	source.Disk
}

func (fakeChanges) HasHistory() bool { return true }
func (fakeChanges) LastChange(context.Context, string) (source.Change, error) {
	return source.Change{}, nil
}
func (fakeChanges) RepoPath(rel string) (string, bool) { return rel, true }
func (fakeChanges) ShowChange(id string) (string, error) {
	return "commit " + id + "\n", nil
}
func (fakeChanges) ChangeTrees(string) (before, after source.Root, parents int, err error) {
	return nil, nil, 0, errors.New("no trees")
}

func TestCommitSides(t *testing.T) {
	root := fakeChanges{"/r"}
	key := changeKey{sides: [2]sideKey{{root, "f"}, {}}}
	if _, wait := commitSides(key, [2]changeResult{}, false); wait == "" {
		t.Error("no note while the commits are found")
	}
	sides, wait := commitSides(key, [2]changeResult{{ch: source.Change{ID: "c1"}}}, true)
	if wait != "" || sides[0].root == nil || sides[0].ch.ID != "c1" || sides[0].rel != "f" || sides[1].why != notInCommit {
		t.Errorf("sides %+v, note %q", sides, wait)
	}
	sides, _ = commitSides(key, [2]changeResult{{err: errors.New("broken")}}, true)
	if sides[0].root != nil || !strings.Contains(sides[0].why, "broken") {
		t.Errorf("an error's side: %+v", sides[0])
	}
	if sides, _ := commitSides(changeKey{}, [2]changeResult{}, true); sides[0].why != notInCommit || sides[1].why != notInCommit {
		t.Errorf("no history: %+v", sides)
	}
}

func TestPickCommit(t *testing.T) {
	root := fakeChanges{"/r"}
	c := func(id string) commitSide { return commitSide{root: root, ch: source.Change{ID: id}} }
	none := func(why string) commitSide { return commitSide{why: why} }
	for _, tt := range []struct {
		name   string
		sides  [2]commitSide
		ran    string // the commit run at once, and its note
		note   string
		popup  bool
		errors []string
	}{
		{"the same", [2]commitSide{c("a"), c("a")}, "a", "", false, nil},
		{"different", [2]commitSide{c("a"), c("b")}, "", "", true, nil},
		{"left", [2]commitSide{c("a"), none("missing")}, "a", "the left side's commit, as the right side is missing", false, nil},
		{"right", [2]commitSide{none(notInCommit), c("b")}, "b", "the right side's commit, as the left side is " + notInCommit, false, nil},
		{"neither", [2]commitSide{none(notInCommit), none(notInCommit)}, "", "", false, []string{"Neither side is in a git commit."}},
		{"neither, why not differs", [2]commitSide{none(notInCommit), none("x")}, "", "", false,
			[]string{"Neither side has a commit:", "The left side is " + notInCommit + ".", "The right side is x."}},
	} {
		var ran, note string
		p, why := pickCommit(tt.sides, "title", func(cs commitSide, n string) { ran, note = cs.ch.ID, n })
		if ran != tt.ran || note != tt.note || (p != nil) != tt.popup || !slices.Equal(why, tt.errors) {
			t.Errorf("%s: ran %q, note %q, popup %v, errors %q", tt.name, ran, note, p != nil, why)
		}
	}
}

func TestCommitPickerKeys(t *testing.T) {
	s := term.NewScreen(100, 30)
	root := fakeChanges{"/r"}
	sides := [2]commitSide{{root: root, ch: source.Change{ID: "a"}}, {root: root, ch: source.Change{ID: "b"}}}
	for _, tt := range []struct {
		keys []term.Key
		ran  string
	}{
		{[]term.Key{term.Rune('h')}, "a"},
		{[]term.Key{term.Key{Code: term.KeyRight}}, "b"},
		{[]term.Key{term.Rune('j'), term.Key{Code: term.KeyEnter}}, "b"},
		{[]term.Key{term.Rune(' ')}, "a"},
		{[]term.Key{term.Rune('x'), term.Rune('m'), term.Key{Code: term.KeyEsc}}, ""},
		{[]term.Key{term.Key{Code: term.KeyEsc}}, ""},
	} {
		var ran string
		p, _ := pickCommit(sides, "title", func(cs commitSide, _ string) { ran = cs.ch.ID })
		for i, k := range tt.keys {
			if open := p.Key(s, k); open != (i < len(tt.keys)-1) {
				t.Errorf("%v: open %v after %v", tt.keys, open, k)
			}
		}
		if ran != tt.ran {
			t.Errorf("%v: ran %q, want %q", tt.keys, ran, tt.ran)
		}
		// The side chosen is selected, should the popup open again.
		if tt.ran != "" && sides[p.popup.Side()].ch.ID != tt.ran {
			t.Errorf("%v: side %d selected after choosing %q", tt.keys, p.popup.Side(), tt.ran)
		}
	}
}

func TestShowMessage(t *testing.T) {
	a := testApp(60, 20)
	back := false
	cv := commitView{app: a, styles: colors.Default(), back: func() { back = true }}
	root := fakeChanges{"/r"}
	cv.showMessage(commitSide{root: root, ch: source.Change{ID: "abc", Subject: "subject"}}, "the left side's commit")
	ts, ok := a.Main.(*TextScreen)
	if !ok {
		t.Fatalf("Main is %T", a.Main)
	}
	if !slices.Equal(ts.view.Lines, []string{"commit abc"}) || ts.view.Notice != "showing the left side's commit" {
		t.Errorf("lines %q, notice %q", ts.view.Lines, ts.view.Notice)
	}
	if ts.Key(term.Rune('x')) {
		t.Error("x handled")
	}
	ts.Key(term.Rune('?'))
	if ts.help == nil {
		t.Fatal("no help")
	}
	// Esc closes Help first, then goes back.
	ts.Key(term.Key{Code: term.KeyEsc})
	if back || ts.help != nil {
		t.Error("Esc didn't close Help alone")
	}
	ts.Key(term.Key{Code: term.KeyEsc})
	if !back {
		t.Error("Esc didn't go back")
	}
	// A failure to show it is shown in the error popup.
	cv.showTrees(commitSide{root: root, ch: source.Change{ID: "abc"}}, "")
	if a.errTitle != showFailed || a.Main != ts {
		t.Errorf("error %q, Main %T", a.errTitle, a.Main)
	}
}

func TestTextHelp(t *testing.T) {
	var keys []string
	for _, l := range view.TextHelp {
		keys = append(keys, l.Keys...)
	}
	for _, k := range []string{"j", "k", "Esc", "Ctrl+c", "gg", "G"} {
		if !slices.Contains(keys, k) {
			t.Errorf("help lacks %s", k)
		}
	}
}

func TestCommitRows(t *testing.T) {
	root := fakeChanges{"/r"}
	if !hasCommits(source.Disk("/d"), root) || hasCommits(source.Disk("/d"), nil) {
		t.Error("hasCommits wrong")
	}
	rows := commitRows(changeKey{sides: [2]sideKey{{root, "f"}, {}}})
	if len(rows) != 2 || rows[0].Key != 'm' || rows[1].Key != 'd' || rows[0].Why != "" {
		t.Errorf("rows %+v", rows)
	}
	if rows := commitRows(changeKey{}); rows[0].Why == "" || rows[1].Why == "" {
		t.Errorf("rows without a commit not dimmed: %+v", rows)
	}
}

func TestCommitBackToActions(t *testing.T) {
	a := testApp(60, 20)
	root := fakeChanges{"/r"}
	var main Screen = &TextScreen{}
	a.Main = main
	reopened := 0
	cv := commitView{
		app: a, styles: colors.Default(),
		back:   func() { a.Main = main },
		reopen: func() { reopened++ },
	}
	// Both sides last changed by the same commit: shown at once.
	var changes changeReader
	changes.app = a
	key := changeKey{sides: [2]sideKey{{root, "l"}, {root, "r"}}}
	for deadline := time.Now().Add(5 * time.Second); ; {
		p, wait := cv.start(commitActions[0], key, &changes)
		if wait == "" {
			if p != nil {
				t.Fatal("a popup for the same commit")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the commits were not found")
		}
		time.Sleep(time.Millisecond)
	}
	ts, ok := a.Main.(*TextScreen)
	if !ok || ts == main {
		t.Fatalf("Main is %T", a.Main)
	}
	ts.Key(term.Key{Code: term.KeyEsc})
	if a.Main != main || reopened != 1 {
		t.Errorf("back to %T, Actions opened %d times", a.Main, reopened)
	}
}
