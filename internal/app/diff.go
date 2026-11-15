// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/diff"
	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/stat"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/view"
)

// Source is a side of the diff view.
type Source struct {
	// Path is shown, and read unless the side is a stream or missing.
	Path string
	// Stream is set for a side read once at startup, such as a pipe or
	// /dev/null, whose contents are in Data, or whose error is in Err.
	Stream bool
	Data   []byte
	Err    error
	// Pipe, for a stream not read yet, is read and closed by Start, in
	// the background: a pipe may take any time to end.
	Pipe io.ReadCloser
	// Stat, or StatErr, is what a stream's panel shows, read when it was
	// opened.
	Stat    *stat.Stat
	StatErr error
	// Missing is set for the absent side of a one-sided file: empty.
	Missing bool
	// Rel is the path relative to the side's root, in directory mode,
	// where HasRel is set.
	Rel    string
	HasRel bool
}

// editSide returns the side as e sees it: only a file read by path can
// be edited.
func (src Source) editSide() EditSide {
	es := EditSide{Path: src.Path, Rel: src.Rel, HasRel: src.HasRel}
	switch {
	case src.Missing:
		es.Why = "missing"
	case src.Stream:
		es.Why = notRegular
	}
	return es
}

// Load reads and splits the side. A file is read by path every time, so
// what an editor saved is read, even if it replaced the file.
func (src Source) Load() (diff.Text, error) {
	switch {
	case src.Missing:
		return diff.Text{}, nil
	case src.Stream:
		if src.Err != nil {
			return diff.Text{}, src.Err
		}
		return diff.Split(src.Data), nil
	}
	// A side given through a symlink on the command line is followed.
	f, err := files.OpenRegular(src.Path, true)
	if err != nil {
		return diff.Text{}, err
	}
	defer func() { _ = f.Close() }()
	data, err := diff.Read(f)
	if err != nil {
		return diff.Text{}, err
	}
	return diff.Split(data), nil
}

// DiffScreen is the diff view.
type DiffScreen struct {
	app         *App
	styles      colors.Styles
	view        *view.DiffView
	left, right Source
	// abs is each side's path made absolute, for the panels.
	abs  [2]string
	help *view.Help
	last *term.Screen
	// back returns to the screen the diff view was opened from; nil quits.
	back func()
	// pendingG is set after g, which goes to the start if repeated.
	pendingG bool
	// edited is called after an edit, as to compare the entry again.
	edited func()
	home   string
	edit   *editor
	// loaded, while streams are read, receives them once read.
	loaded chan [2]Source
	// stats reads the files' metadata, again once statGen is raised, as
	// each time they are loaded.
	stats   statReader
	statGen int
}

// DiffConfig configures the diff view.
type DiffConfig struct {
	Styles      colors.Styles
	Home        string
	Context     int
	Left, Right Source
	// Back returns to the screen the diff view was opened from; nil quits.
	Back func()
	// Edited is called after an edit, as to compare the entry again.
	Edited func()
	// Compare is the attributes whose differences the panels mark: the
	// tree's, or --compare's for two files; 0 means the default.
	Compare scan.Attrs
}

// NewDiffScreen returns the diff view on two sides, not yet loaded.
func NewDiffScreen(a *App, cfg DiffConfig) *DiffScreen {
	return &DiffScreen{
		abs:    [2]string{absPath(cfg.Left.Path), absPath(cfg.Right.Path)},
		app:    a,
		styles: cfg.Styles,
		view: &view.DiffView{
			Styles: cfg.Styles, Home: cfg.Home, Context: cfg.Context,
			Left: cfg.Left.Path, Right: cfg.Right.Path, Compare: cfg.Compare,
		},
		left:   cfg.Left,
		right:  cfg.Right,
		back:   cfg.Back,
		edited: cfg.Edited,
		home:   cfg.Home,
		stats:  statReader{app: a},
	}
}

// Start reads both sides and compares them, showing any error. Streams are
// read in the background, and the screen says so meanwhile; only quitting
// and suspending work until they are read.
func (d *DiffScreen) Start() {
	if d.left.Pipe == nil && d.right.Pipe == nil {
		d.loadOrShow()
		return
	}
	d.loaded = make(chan [2]Source, 1)
	d.view.Message = "reading the input…"
	go func(sides [2]Source) {
		for i := range sides {
			if p := sides[i].Pipe; p != nil {
				sides[i].Data, sides[i].Err = diff.Read(p)
				sides[i].Pipe = nil
				_ = p.Close()
			}
		}
		d.loaded <- sides
		d.app.Wake()
	}([2]Source{d.left, d.right})
}

// loadOrShow loads both sides, or shows why it can't.
func (d *DiffScreen) loadOrShow() {
	if err := d.Load(); err != nil {
		d.ShowError(err)
	}
}

// Load reads both sides and compares them. A side missing before is read
// if it is there now, as when an editor created it. If whether it is can't
// be told, the error is returned, and the side stays missing for the next
// time.
func (d *DiffScreen) Load() error {
	d.statGen++
	for i, src := range []*Source{&d.left, &d.right} {
		if !src.Missing {
			continue
		}
		_, err := os.Lstat(src.Path)
		switch {
		case err == nil:
			src.Missing = false
		case !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ENOTDIR):
			return &sideError{right: i == 1, err: files.UnwrapPath(err)}
		}
	}
	l, leftErr := d.left.Load()
	r, rightErr := d.right.Load()
	if err := loadError(leftErr, rightErr); err != nil {
		return err
	}
	d.view.SetTexts(l, r)
	return nil
}

// loadError returns why the sides, whose errors loading them are left
// and right, can't be compared, or nil if both loaded.
func loadError(left, right error) error {
	switch {
	case left != nil && right != nil:
		return &bothSidesError{
			left:  &sideError{err: files.UnwrapPath(left)},
			right: &sideError{right: true, err: files.UnwrapPath(right)},
		}
	case left != nil:
		return &sideError{err: files.UnwrapPath(left)}
	case right != nil:
		return &sideError{right: true, err: files.UnwrapPath(right)}
	}
	return nil
}

// ShowError replaces the file contents with err. A session started on two
// files uses this to report a load failure on its only screen: in the box
// of each side that failed, and in the other's that it did.
func (d *DiffScreen) ShowError(err error) {
	if texts, ok := sideTexts(err); ok {
		// As "reading the input…", which would be shown instead.
		d.view.Message = ""
		d.view.SideErrors = texts
		return
	}
	d.view.Message = "Unable to compare files. " + errorText(err)
}

// sideTexts returns why each side that err says failed can't be shown,
// for its box, and whether err says that of any side.
func sideTexts(err error) ([2]string, bool) {
	var texts [2]string
	var both *bothSidesError
	var one *sideError
	switch {
	case errors.As(err, &both):
		texts = [2]string{both.left.text(), both.right.text()}
	case errors.As(err, &one) && one.right:
		texts[1] = one.text()
	case errors.As(err, &one):
		texts[0] = one.text()
	default:
		return texts, false
	}
	return texts, true
}

// sideError is why a side couldn't be read.
type sideError struct {
	right bool
	err   error
}

func (e *sideError) side() string {
	if e.right {
		return "right"
	}
	return "left"
}

func (e *sideError) Error() string { return e.side() + ": " + e.err.Error() }

func (e *sideError) Unwrap() error { return e.err }

// Sentence describes the error as a sentence, for the error popup.
func (e *sideError) Sentence() string {
	switch {
	case errors.Is(e.err, diff.ErrBinary):
		return fmt.Sprintf("Only UTF-8 encoded text files can be opened (the %s side is not).", e.side())
	case errors.Is(e.err, diff.ErrTooLarge):
		return fmt.Sprintf("Only files up to %d MiB can be opened (the %s side is larger).", diff.MaxSize>>20, e.side())
	case errors.Is(e.err, files.ErrNotRegular):
		return fmt.Sprintf("The %s side is %v.", e.side(), e.err)
	}
	return fmt.Sprintf("Failed reading the %s side (%v).", e.side(), e.err)
}

// text describes the error as a sentence, for the side's box.
func (e *sideError) text() string {
	switch {
	case errors.Is(e.err, diff.ErrBinary):
		return "Unable to show data that is not UTF-8 encoded."
	case errors.Is(e.err, diff.ErrTooLarge):
		return fmt.Sprintf("Unable to show files larger than %d MiB.", diff.MaxSize>>20)
	case errors.Is(e.err, files.ErrNotRegular):
		return fmt.Sprintf("Unable to show this file, as it is %v.", e.err)
	}
	return fmt.Sprintf("Failed reading this file (%v).", e.err)
}

// bothSidesError reports why neither side could be read.
type bothSidesError struct {
	left, right *sideError
}

func (e *bothSidesError) Error() string { return e.left.Error() + "; " + e.right.Error() }

func (e *bothSidesError) Unwrap() []error { return []error{e.left, e.right} }

// Sentence describes the errors as sentences, one for both sides where
// they failed alike.
func (e *bothSidesError) Sentence() string {
	return strings.Join(e.Sentences(), " ")
}

// Sentences describes the errors for the error popup: one sentence for
// both sides where they failed alike, or one for each.
func (e *bothSidesError) Sentences() []string {
	switch {
	case errors.Is(e.left, diff.ErrBinary) && errors.Is(e.right, diff.ErrBinary):
		return []string{"Only UTF-8 encoded text files can be opened (neither side is)."}
	case errors.Is(e.left, diff.ErrTooLarge) && errors.Is(e.right, diff.ErrTooLarge):
		return []string{fmt.Sprintf("Only files up to %d MiB can be opened (both sides are larger).", diff.MaxSize>>20)}
	}
	return []string{e.left.Sentence(), e.right.Sentence()}
}

// Tick shows the sides once streams are read.
func (d *DiffScreen) Tick() time.Duration {
	if d.loaded == nil {
		return -1
	}
	select {
	case sides := <-d.loaded:
		d.loaded = nil
		d.left, d.right = sides[0], sides[1]
		d.loadOrShow()
	default:
	}
	return -1
}

// Draw draws the diff view, and Help or the Edit popup over it if open.
func (d *DiffScreen) Draw(s *term.Screen) {
	d.last = s
	d.view.Stats = d.panelStats()
	d.view.Draw(s)
	if d.help != nil {
		d.help.Draw(s, d.styles)
	}
	if d.edit != nil {
		d.edit.Draw(s, d.styles)
	}
}

// panelStats returns what the panels show of the two sides.
func (d *DiffScreen) panelStats() [2]view.StatSide {
	srcs := [2]*Source{&d.left, &d.right}
	key := statKey{gen: d.statGen}
	for i, src := range srcs {
		if !src.Missing && !src.Stream {
			key.paths[i] = src.Path
		}
	}
	got, done := d.stats.read(key)
	var sides [2]view.StatSide
	for i, src := range srcs {
		switch {
		case src.Missing:
			sides[i] = view.StatSide{Missing: true, Entries: -1}
		case src.Stream:
			sides[i] = statSide(statResult{src.Stat, src.StatErr}, true, d.app.Names)
		default:
			sides[i] = statSide(got[i], done, d.app.Names)
		}
		sides[i].Path = d.abs[i]
	}
	return sides
}

// startEdit edits a side, then reads both sides again: whether or not the
// editor changed anything, the diff view shows what is on disk now.
func (d *DiffScreen) startEdit() {
	sides := [2]EditSide{d.left.editSide(), d.right.editSide()}
	ed, why := d.app.Edit(sides, d.home, func(note string) {
		if err := d.Load(); err != nil {
			d.ShowError(err)
		}
		d.view.Notice = note
		if d.edited != nil {
			d.edited()
		}
	})
	// With one side editable, the edit is done by now, and its notice set.
	d.edit = ed
	if why != nil {
		d.app.ShowError(unableToEdit, why...)
	}
}

// Key handles a key.
func (d *DiffScreen) Key(k term.Key) bool {
	v := d.view
	v.Notice = ""
	if d.edit != nil {
		if !d.edit.Key(k) {
			d.edit = nil
		}
		return true
	}
	if d.help != nil {
		return helpKey(&d.help, d.last, k)
	}
	// Stream inputs open directly in the diff view. While loading,
	// only application-level keys are available.
	if d.loaded != nil {
		return false
	}
	// g waits for a second g.
	g := d.pendingG
	d.pendingG = false
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
		d.pendingG = true
	case k == term.Key{Code: term.KeyRight}:
		if v.Wrap {
			v.Notice = "no sideways scrolling with wrap on"
		}
		v.ScrollSideways(8)
	case k == term.Key{Code: term.KeyLeft}:
		v.ScrollSideways(-8)
	case k == term.Rune('w'):
		v.Wrap = !v.Wrap
		v.HScroll = 0
	case k == term.Rune('f'):
		v.Full = !v.Full
	case k == term.Rune('n') || k == term.Key{Code: term.KeyDown, Shift: true}:
		if !v.Jump(true) {
			v.Notice = "no more changes below"
		}
	case k == term.Rune('N') || k == term.Key{Code: term.KeyUp, Shift: true}:
		if !v.Jump(false) {
			v.Notice = "no more changes above"
		}
	case k == term.Rune('?'):
		d.help = view.NewHelp(view.DiffHelp(d.back != nil))
	case k == term.Rune('e'):
		d.startEdit()
	case k.Code == term.KeyEsc:
		// Esc goes back to the tree, but never quits.
		if d.back != nil {
			d.back()
		}
	default:
		return false
	}
	return true
}

// helpKey handles a key while Help is open: the keys that move through a
// list move its selection, and any other key closes it, by setting *help
// to nil, without acting on the screen behind it.
func helpKey(help **view.Help, s *term.Screen, k term.Key) bool {
	if !listKey(*help, s, k) {
		// Any other key closes Help, and goes no further.
		*help = nil
	}
	return true
}

// listKey moves the selection of h, Help or a popup like it, if k is one
// of the keys that move through a list, and reports whether it was.
func listKey(h *view.Help, s *term.Screen, k term.Key) bool {
	g := h.PendingG
	h.PendingG = false
	page := 1
	if s != nil {
		page = max(s.H-4, 1)
	}
	switch {
	case k == term.Rune('j') || k == term.Key{Code: term.KeyDown}:
		h.Move(s, 1)
	case k == term.Rune('k') || k == term.Key{Code: term.KeyUp}:
		h.Move(s, -1)
	case k == term.Key{Code: term.KeyPgDn} || k == term.Ctrl('f'):
		h.Move(s, page)
	case k == term.Key{Code: term.KeyPgUp} || k == term.Ctrl('b'):
		h.Move(s, -page)
	case k == term.Key{Code: term.KeyHome} || k == term.Rune('g') && g:
		h.MoveTo(s, true)
	case k == term.Key{Code: term.KeyEnd} || k == term.Rune('G'):
		h.MoveTo(s, false)
	case k == term.Rune('g'):
		h.PendingG = true
	default:
		return false
	}
	return true
}
