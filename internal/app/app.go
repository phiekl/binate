// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package app is the child's event loop: it reads keys, passes them to the
// current screen, and draws.
package app

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"pxy.se/go/binate/internal/cli"
	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/stat"
	"pxy.se/go/binate/internal/supervise"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/view"
)

// Minimum terminal size to draw in; a smaller one shows only a message.
const (
	MinWidth  = 40
	MinHeight = 5
)

// Screen is what the app shows: the tree view or the diff view, with any
// overlay on top.
type Screen interface {
	// Draw fills s.
	Draw(s *term.Screen)
	// Key handles a key. It returns false if the key means nothing here.
	Key(k term.Key) bool
	// Tick is called on every pass of the loop, as after a Wake, before
	// drawing, and returns how long the loop may wait before calling it again
	// (negative for no limit).
	Tick() time.Duration
}

// App is the running child.
type App struct {
	log    *slog.Logger
	term   *term.Terminal
	client *supervise.Client
	screen *term.Screen

	// Main is the screen shown.
	Main Screen
	// HasEditor is set if VISUAL or EDITOR is, for the parent to run.
	HasEditor bool
	// Names names the owners in the panels. They must be read before the
	// process is restricted.
	Names *stat.Names
	// Styles draw the quit prompt.
	Styles colors.Styles
	// Header, unless IsZero, as when a side's Path is set, is shown in a
	// row above Main, which is drawn below it, unless the terminal is too
	// small for both.
	Header view.Header
	// main is what Main is drawn on below the header.
	main *term.Screen

	quit       bool
	confirming bool // the quit prompt is shown
	// errTitle and errTexts are the error popup's, shown if errTitle is
	// set.
	errTitle   string
	errTexts   []string
	cancelQuit bool // the quit prompt's Cancel button is focused
	exitCode   int
}

// New returns an app drawing on t and talking to the parent through client.
func New(log *slog.Logger, t *term.Terminal, client *supervise.Client) *App {
	return &App{log: log, term: t, client: client, screen: term.NewScreen(0, 0)}
}

// Wake makes the loop run a pass, from any goroutine. Without a terminal,
// as in tests, it does nothing.
func (a *App) Wake() {
	if a.term != nil {
		a.term.Wake()
	}
}

// Quit ends the loop after the current key.
func (a *App) Quit() {
	a.quit = true
}

// RequestQuit shows the quit prompt, its Cancel button focused; a second
// Ctrl+C also quits (see quitPromptKey).
func (a *App) RequestQuit() {
	a.confirming, a.cancelQuit = true, true
}

// Enter takes the terminal and tells the parent.
func (a *App) Enter() error {
	if err := a.term.Enter(); err != nil {
		return err
	}
	a.screen.Invalidate()
	return a.client.Screen(true)
}

// Leave releases the terminal and tells the parent.
func (a *App) Leave() error {
	err := a.term.Leave()
	return errors.Join(err, a.client.Screen(false))
}

// Run runs the loop until the screen quits or a signal ends it, and returns
// the exit status. The terminal must be entered.
func (a *App) Run() (code int) {
	signals := make(chan os.Signal, 8)
	pending := make(chan os.Signal, 64)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	go func() {
		for sig := range signals {
			select {
			case pending <- sig:
			default:
			}
			a.term.Wake()
		}
	}()

	a.resize()
	for !a.quit {
		// Tick first, so what it changes is drawn now, not after a key.
		timeout := time.Duration(-1)
		if a.Main != nil {
			timeout = a.Main.Tick()
		}
		a.draw()
		keys, _, err := a.term.Next(timeout)
		if err != nil {
			// The terminal is gone, as after a hangup.
			cli.Debug(a.log, cli.LevelDebug1, "reading the terminal", "error", err)
			return 128 + int(syscall.SIGHUP)
		}
		for drained := false; !drained; {
			select {
			case sig := <-pending:
				switch sig {
				case syscall.SIGWINCH:
					a.resize()
				default:
					return 128 + int(sig.(syscall.Signal))
				}
			default:
				drained = true
			}
		}
		for _, k := range keys {
			a.key(k)
			if a.quit {
				break
			}
		}
	}
	return a.exitCode
}

func (a *App) resize() {
	w, h, err := a.term.Size()
	if err != nil {
		cli.Debug(a.log, cli.LevelDebug1, "reading the terminal size", "error", err)
		w, h = 80, 24
	}
	a.screen.Resize(w, h)
}

// tooSmall reports whether the terminal is too small to draw in.
func (a *App) tooSmall() bool {
	return a.screen.W < MinWidth || a.screen.H < MinHeight
}

func (a *App) draw() {
	a.screen.Clear()
	if a.tooSmall() {
		msg := fmt.Sprintf("terminal too small (%dx%d, needs %dx%d)", a.screen.W, a.screen.H, MinWidth, MinHeight)
		a.screen.Label(0, 0, a.screen.W, msg, term.Style{})
	} else if a.Main != nil {
		a.drawMain()
	}
	if a.errTitle != "" && !a.tooSmall() {
		view.DrawError(a.screen, a.Styles, a.errTitle, a.errTexts...)
	}
	if a.confirming && !a.tooSmall() {
		view.DrawQuit(a.screen, a.Styles, !a.cancelQuit)
	}
	if err := a.screen.Flush(a.term.Out()); err != nil {
		cli.Debug(a.log, cli.LevelDebug1, "writing the screen", "error", err)
	}
}

// drawMain draws Main, below the header if there is one and room for it.
func (a *App) drawMain() {
	if a.Header.IsZero() || a.screen.H-1 < MinHeight {
		a.Main.Draw(a.screen)
		return
	}
	if a.main == nil {
		a.main = term.NewScreen(0, 0)
	}
	if a.main.W != a.screen.W || a.main.H != a.screen.H-1 {
		a.main.Resize(a.screen.W, a.screen.H-1)
	}
	a.main.Clear()
	a.Main.Draw(a.main)
	a.screen.Copy(a.main, 1)
	view.DrawHeader(a.screen, a.Header, a.Styles)
}

func (a *App) key(k term.Key) {
	switch {
	case k == term.Ctrl('z'):
		a.Suspend()
		return
	case k == term.Ctrl('c') && (a.confirming || a.tooSmall()):
		// A second Ctrl+C quits, as does one when there is no room for the
		// prompt.
		a.confirming = false
		a.Quit()
		return
	case a.confirming:
		a.quitPromptKey(k)
		return
	case a.tooSmall():
		return
	case k == term.Ctrl('c'):
		a.errTitle = ""
		a.RequestQuit()
		return
	case a.errTitle != "":
		// Any key closes the error popup, and goes no further.
		a.errTitle = ""
		return
	}
	if a.Main != nil {
		a.Main.Key(k)
	}
}

// ShowError shows the error popup, titled title, with texts, more than
// one as a list, until a key closes it. Each text is made a sentence:
// capitalized, and ending in a full stop.
func (a *App) ShowError(title string, texts ...string) {
	a.errTitle, a.errTexts = title, nil
	for _, text := range texts {
		a.errTexts = append(a.errTexts, sentence(text))
	}
}

// sentence capitalizes s and ends it in a full stop, unless it ends in
// punctuation already, or a colon, as one leading a list.
func sentence(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	s = string(unicode.ToUpper(r)) + s[n:]
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "!") && !strings.HasSuffix(s, "?") && !strings.HasSuffix(s, ":") {
		s += "."
	}
	return s
}

// errorText returns err's text for the error popup: its sentence, if it
// has one.
func errorText(err error) string {
	var s interface{ Sentence() string }
	if errors.As(err, &s) {
		return s.Sentence()
	}
	return err.Error()
}

// errorTexts returns err's texts for the error popup: its sentences, for
// an error of more than one, or else its text.
func errorTexts(err error) []string {
	var s interface{ Sentences() []string }
	if errors.As(err, &s) {
		return s.Sentences()
	}
	return []string{errorText(err)}
}

// quitPromptKey handles a key while the quit prompt is shown: x or X
// exits, Left, Right and Tab move between the buttons, Enter and Space
// press the focused one, and any other key cancels.
func (a *App) quitPromptKey(k term.Key) {
	switch {
	case k == term.Rune('x') || k == term.Rune('X'):
		a.confirming = false
		a.Quit()
	case k.Code == term.KeyLeft:
		a.cancelQuit = false
	case k.Code == term.KeyRight:
		a.cancelQuit = true
	case k.Code == term.KeyTab:
		a.cancelQuit = !a.cancelQuit
	case (k.Code == term.KeyEnter || k == term.Rune(' ')) && !a.cancelQuit:
		a.confirming = false
		a.Quit()
	default:
		a.confirming = false
	}
}

// Suspend releases the terminal and has the parent stop the job, then
// takes the terminal back once the shell continues it. If it can't, the
// error popup says why.
func (a *App) Suspend() {
	if err := a.Release(a.client.Suspend); err != nil {
		cli.Debug(a.log, cli.LevelDebug1, "suspending", "error", err)
		a.ShowError("Failed to suspend", suspendError(err))
	}
}

// suspendError describes why the job couldn't be stopped, for the error
// popup, with err in parentheses as a system error.
func suspendError(err error) string {
	return "Failed stopping the job (" + err.Error() + ")."
}

// Release releases the terminal, runs fn, and takes the terminal back,
// redrawing it fully. fn's error is returned.
func (a *App) Release(fn func() error) error {
	if err := a.Leave(); err != nil {
		cli.Debug(a.log, cli.LevelDebug1, "releasing the terminal", "error", err)
	}
	err := fn()
	if eerr := a.Enter(); eerr != nil {
		cli.Debug(a.log, cli.LevelDebug1, "taking the terminal back", "error", eerr)
	}
	a.resize()
	return err
}
