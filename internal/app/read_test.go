// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/view"
)

// TestReadScreen checks that the read screen shows the loading box until
// reading is done, then the next screen, or if reading failed, its error
// in the popup, naming the side, until a key closes it and ends the loop,
// with the error.
func TestReadScreen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		title   string
		quitKey term.Key
	}{
		{name: "read"},
		{"left", &ReadError{Side: 0, Path: "a.tar", Err: errors.New("broken")}, "Unable to read the left side", term.Rune('x')},
		{"right", &ReadError{Side: 1, Path: "b.zip", Err: errors.New("broken")}, "Unable to read the right side", term.Key{Code: term.KeyEnter}},
		{"other", errors.New("broken"), "Unable to read", term.Ctrl('c')},
	} {
		a := testApp(80, 24)
		release := make(chan struct{})
		next := &keyScreen{}
		r := NewReadScreen(a, ReadConfig{
			Styles: colors.Default(), Left: "a.tar", Right: "dir",
			Read: func() error {
				<-release
				return tc.err
			},
			Status: func() view.ReadStatus { return view.ReadStatus{Entries: 7} },
			Next:   func() Screen { return next },
		})
		a.Main = r
		if d := r.Tick(); d <= 0 {
			t.Errorf("%s: tick while reading: %v", tc.name, d)
		}
		scr := term.NewScreen(80, 24)
		r.Draw(scr)
		if !strings.Contains(scr.String(), "7 entries") {
			t.Errorf("%s: while reading:\n%s", tc.name, scr.String())
		}
		if r.Err() != nil {
			t.Errorf("%s: error while reading: %v", tc.name, r.Err())
		}
		close(release)
		<-r.done
		if tc.err == nil {
			if d := r.Tick(); d != 0 || a.quit || r.Err() != nil || a.Main != Screen(next) {
				t.Errorf("read: tick %v, quit %t, error %v, main %T", d, a.quit, r.Err(), a.Main)
			}
			continue
		}
		// Shown, over the box, until a key, however many ticks.
		for range 2 {
			if d := r.Tick(); d >= 0 || a.quit || a.Main != Screen(r) {
				t.Errorf("%s: tick %v, quit %t, main %T", tc.name, d, a.quit, a.Main)
			}
		}
		scr.Clear()
		r.Draw(scr)
		if text := scr.String(); strings.Contains(text, "entries") || !strings.Contains(text, "a.tar") {
			t.Errorf("%s: behind the error:\n%s", tc.name, text)
		}
		if a.errTitle != tc.title || !slices.Equal(a.errTexts, []string{"Broken."}) {
			t.Errorf("%s: popup %q %q, want %q", tc.name, a.errTitle, a.errTexts, tc.title)
		}
		a.key(tc.quitKey)
		if d := r.Tick(); d != 0 || !a.quit || r.Err() != tc.err || a.errTitle != "" {
			t.Errorf("%s: after a key: tick %v, quit %t, error %v, popup %q", tc.name, d, a.quit, r.Err(), a.errTitle)
		}
	}
}

// TestReadScreenTooSmall checks that an error of reading, which a
// terminal too small can't show, ends the loop at once, as it does once
// the terminal shrinks under it, for the error to be written.
func TestReadScreenTooSmall(t *testing.T) {
	failed := func(a *App) *ReadScreen {
		r := NewReadScreen(a, ReadConfig{
			Styles: colors.Default(), Left: "a.zip", Right: "dir",
			Read:   func() error { return &ReadError{Path: "a.zip", Err: errors.New("broken")} },
			Status: func() view.ReadStatus { return view.ReadStatus{} },
			Next:   func() Screen { return &keyScreen{} },
		})
		a.Main = r
		<-r.done
		return r
	}
	a := testApp(30, 24)
	r := failed(a)
	if d := r.Tick(); d != 0 || !a.quit || r.Err() == nil || a.errTitle != "" {
		t.Errorf("too small: tick %v, quit %t, error %v, popup %q", d, a.quit, r.Err(), a.errTitle)
	}
	a = testApp(80, 24)
	r = failed(a)
	if d := r.Tick(); d >= 0 || a.quit || a.errTitle == "" {
		t.Errorf("shown: tick %v, quit %t, popup %q", d, a.quit, a.errTitle)
	}
	a.screen.Resize(30, 24)
	if d := r.Tick(); d != 0 || !a.quit {
		t.Errorf("shrunk: tick %v, quit %t", d, a.quit)
	}
}
