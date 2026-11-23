// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"time"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/view"
)

// ReadConfig configures the read screen.
type ReadConfig struct {
	Styles colors.Styles
	Home   string
	// Left and Right title the boxes.
	Left, Right string
	// Read reads the sides, in the background, and Status says how far it
	// has got, from the loop's goroutine as Read runs.
	Read   func() error
	Status func() view.ReadStatus
	// Next is the screen shown once Read is done without an error.
	Next func() Screen
}

// ReadError is why reading side Side, 0 for the left and 1 for the right,
// from Path, failed, as Read returns it.
type ReadError struct {
	Side int
	Path string
	Err  error
}

func (e *ReadError) Error() string { return e.Path + ": " + e.Err.Error() }

func (e *ReadError) Unwrap() error { return e.Err }

// ReadScreen shows a loading box while the sides are read before they
// can be scanned, as archives are, then the screen Next makes. If reading
// fails, it shows the error in the error popup over the box, and ends the
// loop once a key closes it, with the error for Err.
type ReadScreen struct {
	app   *App
	cfg   ReadConfig
	start time.Time
	done  chan struct{}
	err   error
	// shown is whether the error has been shown.
	shown bool
}

// NewReadScreen starts reading and returns the screen showing it.
func NewReadScreen(a *App, cfg ReadConfig) *ReadScreen {
	r := &ReadScreen{app: a, cfg: cfg, start: time.Now(), done: make(chan struct{})}
	go func() {
		r.err = cfg.Read()
		close(r.done)
		a.Wake()
	}()
	return r
}

// Err returns why reading failed, once the loop has ended, or nil.
func (r *ReadScreen) Err() error {
	select {
	case <-r.done:
		return r.err
	default:
		return nil
	}
}

// Tick shows the next screen once reading is done, or if it failed, the
// error until a key closes it, then ends the loop, and else redraws as
// often as the tree while it scans.
func (r *ReadScreen) Tick() time.Duration {
	select {
	case <-r.done:
	default:
		return scanRefresh
	}
	switch {
	case r.err == nil:
		r.app.Main = r.cfg.Next()
		return 0
	case !r.shown && !r.app.tooSmall():
		r.shown = true
		title, err := "Unable to read", r.err
		var re *ReadError
		if errors.As(r.err, &re) {
			title = [2]string{"Unable to read the left side", "Unable to read the right side"}[re.Side]
			err = re.Err
		}
		r.app.ShowError(title, errorTexts(err)...)
	case r.app.errTitle == "" || r.app.tooSmall():
		// Closed, by any key, or Ctrl+c, which then quits too, or with no
		// room to show it, as the error is written once the loop ends.
		// Not waiting for another key, for the loop to end.
		r.app.Quit()
		return 0
	}
	// Until a key.
	return -1
}

func (r *ReadScreen) Draw(s *term.Screen) {
	st := r.cfg.Status()
	st.Elapsed = time.Since(r.start)
	st.Failed = r.Err() != nil
	view.DrawRead(s, r.cfg.Styles, r.cfg.Home, r.cfg.Left, r.cfg.Right, st)
}

// Key handles no keys: those of the app, as Ctrl+c, still work.
func (r *ReadScreen) Key(term.Key) bool {
	return false
}
