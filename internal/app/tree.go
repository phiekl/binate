// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"slices"
	"time"
	"unicode"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/tree"
	"pxy.se/go/binate/internal/view"
)

// scanRefresh is how often the tree is redrawn while a scan runs.
const scanRefresh = 50 * time.Millisecond

// TreeScreen is the tree view.
type TreeScreen struct {
	app    *App
	styles colors.Styles
	opts   scan.Options
	scan   *scan.Scan
	model  *tree.Model
	view   *view.TreeView
	// expandAfterScan expands the initial tree once every entry is loaded.
	expandAfterScan bool
	// pendingG is set after g, which goes to the first row if repeated.
	pendingG bool

	filter  *view.FilterBar
	help    *view.Help
	compare *view.ComparePopup
	actions *view.ActionPopup
	// action is the action run on the selected entry, if any, and
	// results the results kept of earlier ones.
	action  *actionRun
	results resultCache
	message string
	// filterHistory and searchHistory are the queries applied in each bar.
	filterHistory, searchHistory history
	// height is the screen height last drawn, for paging.
	height int
	// last is the screen last drawn on, for scrolling Help.
	last *term.Screen

	home    string
	context int

	edit *editor
	// roots are the sides.
	roots [2]source.Root
	// rechecks are paths to compare again, after an edit, once the
	// recheck running in the background, which recheckDone closes, is done.
	rechecks    []string
	recheckDone chan struct{}
	// stats reads the selected entry's metadata, again once statGen is
	// raised, as after a rescan or an edit.
	stats   statReader
	statGen int
	// previews reads the selected pair's differences, again once statGen
	// is raised.
	previews previewReader
}

// TreeConfig configures the tree view.
type TreeConfig struct {
	Styles      colors.Styles
	Home        string
	Left, Right source.Root
	Options     scan.Options
	// Context is the diff view's number of context lines.
	Context int
}

// NewTreeScreen starts scanning and returns the tree view.
func NewTreeScreen(a *App, cfg TreeConfig) *TreeScreen {
	t := &TreeScreen{
		app:             a,
		styles:          cfg.Styles,
		opts:            cfg.Options,
		home:            cfg.Home,
		context:         cfg.Context,
		expandAfterScan: true,
		view: &view.TreeView{
			Styles: cfg.Styles, Home: cfg.Home,
			Left: cfg.Left.Path(""), Right: cfg.Right.Path(""),
			Compare: cfg.Options.Compare,
		},
	}
	if t.opts.Compare == 0 {
		t.opts.Compare = scan.DefaultAttrs
	}
	t.view.Compare = t.opts.Compare
	t.roots = [2]source.Root{cfg.Left, cfg.Right}
	t.stats.app, t.previews.app = a, a
	t.start()
	t.model = tree.New(t.scan.Root)
	return t
}

func (t *TreeScreen) start() {
	// The scan reports progress often, and Tick redraws while it runs:
	// wake only to show the end.
	t.scan = scan.Start(t.roots[0], t.roots[1], t.opts, func(done bool) {
		if done {
			t.app.Wake()
		}
	})
}

// Scan returns the running or finished scan.
func (t *TreeScreen) Scan() *scan.Scan {
	return t.scan
}

// Rescan cancels a running scan and starts another.
func (t *TreeScreen) Rescan() {
	t.statGen++
	// The new scan reads everything again.
	t.rechecks = nil
	t.scan.Cancel()
	t.start()
	t.scan.Lock()
	t.model.SetRoot(t.scan.Root)
	t.scan.Unlock()
}

// Tick refreshes the screen while scanning, and starts rechecks queued
// while another ran.
func (t *TreeScreen) Tick() time.Duration {
	if t.recheckDone != nil {
		select {
		case <-t.recheckDone:
			t.recheckDone = nil
			t.statGen++
			t.startRechecks()
		default:
		}
	}
	if t.scan.Done() {
		if t.actionRunning() {
			return actionRefresh
		}
		return -1
	}
	return scanRefresh
}

// Draw draws the tree, and Help or the Edit popup over it if open.
func (t *TreeScreen) Draw(s *term.Screen) {
	t.height, t.last = s.H, s
	t.scan.Lock()
	scanning := !t.scan.Done()
	if !scanning && t.expandAfterScan {
		t.model.ExpandAll()
		t.expandAfterScan = false
	}
	scanned, elapsed := t.scan.Progress()
	t.view.Draw(s, t.model, view.TreeStatus{
		Scanning: scanning, Scanned: scanned, Elapsed: elapsed, Current: t.scan.Current(),
		Message: t.message, Filter: t.filter, Stats: t.panelStats,
		Preview: t.preview, Action: t.actionResult(t.model.SelectedNode()),
	})
	t.scan.Unlock()
	if t.help != nil {
		t.help.Draw(s, t.styles)
	}
	if t.edit != nil {
		t.edit.Draw(s, t.styles)
	}
	if t.compare != nil {
		t.compare.Draw(s, t.styles)
	}
	if t.actions != nil {
		t.actions.Draw(s, t.styles)
	}
}

// panelStats returns what the panels show of the selected entry n. The
// scan's lock must be held.
func (t *TreeScreen) panelStats(n *scan.Node) [2]view.StatSide {
	key := statKey{gen: t.statGen}
	for i := range 2 {
		if n.Side(i == 1).Kind != scan.Missing {
			key.sides[i] = t.sideKey(n, i == 1)
		}
	}
	got, done := t.stats.read(key)
	var sides [2]view.StatSide
	for i := range 2 {
		sd := n.Side(i == 1)
		if sd.Kind == scan.Missing {
			sides[i] = view.StatSide{Missing: true, Entries: -1}
		} else {
			sides[i] = statSide(got[i], done, t.app.Names)
			sides[i].ScanErr = sd.Err
			if sd.Kind == scan.Dir {
				sides[i].Entries = sd.Count
			}
		}
		sides[i].Path = panelPath(t.roots[i], n.Path())
	}
	return sides
}

// Recheck compares the entry at path again in the background, once the
// running scan is done, so the two never update the same entry, and the
// screen stays usable while a large file is read.
func (t *TreeScreen) Recheck(path string) {
	t.rechecks = append(t.rechecks, path)
	t.startRechecks()
}

// startRechecks starts comparing the queued paths again, unless a recheck
// is running: Scan.Recheck runs one at a time.
func (t *TreeScreen) startRechecks() {
	if t.recheckDone != nil || len(t.rechecks) == 0 {
		return
	}
	paths, s, done := t.rechecks, t.scan, make(chan struct{})
	t.rechecks, t.recheckDone = nil, done
	go func() {
		for _, p := range paths {
			s.Recheck(p)
		}
		// Closed before waking, so the woken Tick sees it.
		close(done)
		if t.app != nil {
			t.app.Wake()
		}
	}()
}

// startEdit edits the selected entry. The scan's lock must not be held, as
// the entry is compared again afterwards.
func (t *TreeScreen) startEdit() {
	t.scan.Lock()
	n := t.model.SelectedNode()
	var sides [2]EditSide
	var path string
	if n != nil {
		path = n.Path()
		for i := range 2 {
			sides[i] = EditSide{
				Path: t.roots[i].Path(path), Rel: path, HasRel: true,
				Why: whyNotEditable(t.roots[i], n.Side(i == 1).Kind),
			}
		}
	}
	t.scan.Unlock()
	if n == nil {
		return
	}
	ed, why := t.app.Edit(sides, t.home, func(note string) {
		t.statGen++
		t.Recheck(path)
		t.message = note
	})
	// With one side editable, the edit is done by now, and its message set.
	t.edit = ed
	if why != nil {
		t.app.ShowError(unableToEdit, why...)
	}
}

// Key handles a key.
func (t *TreeScreen) Key(k term.Key) bool {
	t.message = ""
	switch {
	case t.edit != nil:
		if !t.edit.Key(k) {
			t.edit = nil
		}
		return true
	case t.help != nil:
		return helpKey(&t.help, t.last, k)
	case t.compare != nil:
		t.compareKey(k)
		return true
	case t.actions != nil:
		t.scan.Lock()
		defer t.scan.Unlock()
		t.actionKey(k)
		return true
	case t.filter != nil:
		t.scan.Lock()
		defer t.scan.Unlock()
		return t.filterKey(k)
	}
	if k == term.Rune('r') {
		t.Rescan()
		return true
	}
	if k == term.Rune('?') {
		t.help = view.NewHelp(view.TreeHelp)
		return true
	}
	if k == term.Rune('e') {
		t.startEdit()
		return true
	}
	if k == term.Rune('c') {
		t.compare = view.NewComparePopup(t.opts.Compare, t.opts.Quick)
		return true
	}
	t.scan.Lock()
	defer t.scan.Unlock()
	return t.treeKey(k)
}

// compareKey handles a key while the Compare popup is open: the keys
// that move through Help move the selection, Space and Enter change the
// selected row, a row's letter selects and changes it, * turns all on,
// or all off if they are, and Esc closes the popup. Other keys are ignored.
func (t *TreeScreen) compareKey(k term.Key) {
	c := t.compare
	switch {
	case listKey(c.Help, t.last, k):
	case k == term.Rune('*'):
		// All on, or all off if they are.
		compare := t.opts.Compare | scan.AllAttrs
		if t.opts.Compare&scan.AllAttrs == scan.AllAttrs {
			compare = 0
		}
		t.setCompare(compare, t.opts.Quick)
		c.Set(t.opts.Compare, t.opts.Quick)
	case k == term.Rune(' ') || k.Code == term.KeyEnter || k == term.Rune(k.Rune) && c.SelectKey(k.Rune):
		compare, quick := t.opts.Compare, t.opts.Quick
		switch attr := c.Attr(); attr {
		case scan.Content:
			// The contents go on and off, compared in full; quick mode, from
			// -q, only saves reading at startup, so it is only left.
			switch {
			case compare&scan.Content == 0:
				compare |= scan.Content
			case quick:
				quick = false
			default:
				compare &^= scan.Content
			}
		default:
			compare ^= attr
		}
		t.setCompare(compare, quick)
		c.Set(t.opts.Compare, t.opts.Quick)
	case k.Code == term.KeyEsc:
		t.compare = nil
	}
}

// setCompare compares the tree again with the attributes compare and
// quick mode: at once from what the scan read, or with a new scan if it
// is still running, or contents must be read that it didn't read.
func (t *TreeScreen) setCompare(compare scan.Attrs, quick bool) {
	if compare &^= scan.None; compare == 0 {
		compare = scan.None
	}
	t.opts.Compare, t.opts.Quick = compare, quick && compare&scan.Content != 0
	t.view.Compare = t.opts.Compare
	switch {
	case !t.scan.Done():
		t.Rescan()
		t.message = "scanning again with the new settings"
	case t.scan.Reclassify(t.opts):
		t.Rescan()
		t.message = "scanning again to compare contents"
	}
}

// page is the distance PgUp and PgDn move: a page of the tree as last
// drawn, which a preview may have shortened.
func (t *TreeScreen) page() int {
	rows := t.view.Rows()
	if rows == 0 {
		rows = view.TreeRows(t.height)
	}
	return max(rows-1, 1)
}

func (t *TreeScreen) treeKey(k term.Key) bool {
	m := t.model
	// g waits for a second g.
	g := t.pendingG
	t.pendingG = false
	switch {
	case k == term.Rune('j') || k == term.Key{Code: term.KeyDown}:
		m.Move(1)
	case k == term.Rune('k') || k == term.Key{Code: term.KeyUp}:
		m.Move(-1)
	case k == term.Key{Code: term.KeyPgDn} || k == term.Ctrl('f'):
		m.Move(t.page())
	case k == term.Key{Code: term.KeyPgUp} || k == term.Ctrl('b'):
		m.Move(-t.page())
	case k == term.Key{Code: term.KeyHome} || k == term.Rune('g') && g:
		m.MoveTo(true)
	case k == term.Key{Code: term.KeyEnd} || k == term.Rune('G'):
		m.MoveTo(false)
	case k == term.Rune('g'):
		t.pendingG = true
	case k == term.Rune('l') || k == term.Key{Code: term.KeyRight}:
		m.Expand()
	case k == term.Rune('L') || k == term.Key{Code: term.KeyRight, Shift: true}:
		if m.Filtered {
			t.message = "expanding is unavailable while a filter is applied"
		} else {
			m.ExpandSubtree()
		}
	case k == term.Rune('h') || k == term.Key{Code: term.KeyLeft}:
		m.Collapse()
	case k == term.Rune('H') || k == term.Key{Code: term.KeyLeft, Shift: true}:
		if m.Filtered {
			t.message = "collapsing is unavailable while a filter is applied"
		} else {
			m.CollapseSubtree()
		}
	case k == term.Rune('+') || k == term.Rune('='):
		if m.Filtered {
			t.message = "expanding is unavailable while a filter is applied"
		} else {
			m.ExpandAll()
		}
	case k == term.Rune('-'):
		if m.Filtered {
			t.message = "collapsing is unavailable while a filter is applied"
		} else {
			m.CollapseAll()
		}
	case k == term.Rune('*') && m.Filtered:
		// The filter lists hidden identical entries too, unless in
		// diffs-only mode: switch the applied filter's mode instead.
		m.DiffsOnly = !m.DiffsOnly
		if m.DiffsOnly {
			t.message = "hiding identical entries from the filter"
		} else {
			t.message = "showing identical entries in the filter"
		}
	case k == term.Rune('*'):
		m.ShowSame = !m.ShowSame
		if m.ShowSame {
			t.message = "showing identical entries"
		} else {
			t.message = "hiding identical entries"
		}
	case (k == term.Rune('n') || k == term.Rune('N')) && m.Search != "":
		t.searchNext(k == term.Rune('n'))
	case k == term.Rune('n') || k == term.Key{Code: term.KeyDown, Shift: true}:
		if !m.Jump(true) {
			t.message = "no differences"
		}
	case k == term.Rune('N') || k == term.Key{Code: term.KeyUp, Shift: true}:
		if !m.Jump(false) {
			t.message = "no differences"
		}
	case k == term.Rune('/'):
		t.filter = &view.FilterBar{Search: true, Literal: m.SearchLiteral}
		t.searchHistory.open()
	case k == term.Rune('F'):
		t.filter = &view.FilterBar{Query: []rune(m.Query), DiffsOnly: m.DiffsOnly, Literal: m.Literal}
		t.filter.Cursor = len(t.filter.Query)
		t.filterHistory.open()
	case k.Code == term.KeyEnter:
		t.enter()
	case k == term.Rune('a'):
		t.openActions()
	case k.Code == term.KeyEsc:
		// Esc closes an action's result first, then ends a search, then
		// clears the filter.
		if t.action != nil {
			t.stopAction()
		} else if m.Search != "" {
			m.Search = ""
		} else if m.Filtered {
			m.Filtered, m.Query, m.DiffsOnly = false, "", false
		}
	default:
		return false
	}
	return true
}

// enter opens the diff view on the selected pair of files, or expands or
// collapses a directory.
func (t *TreeScreen) enter() {
	m := t.model
	n := m.SelectedNode()
	if n == nil {
		return
	}
	if n.IsDir() {
		// A filter result is shown in the tree as it is, expanded or not.
		if m.Filtered {
			m.Reveal(n)
		} else {
			m.Toggle()
		}
		return
	}
	if why := whyNotOpened(n); why != nil {
		t.app.ShowError(unableToOpen, why...)
		return
	}
	t.openFile(n)
}

// searchNext selects the next (forward) or previous match of the search,
// saying so if there is none or the search wrapped.
func (t *TreeScreen) searchNext(forward bool) {
	found, wrapped := t.model.SearchNext(forward)
	switch {
	case !found:
		t.message = "no match for " + t.model.Search
	case wrapped && forward:
		t.message = "search hit the bottom, continuing at the top"
	case wrapped:
		t.message = "search hit the top, continuing at the bottom"
	}
}

// filterKey handles a key in the filter or search bar.
func (t *TreeScreen) filterKey(k term.Key) bool {
	f := t.filter
	m := t.model
	hist := &t.filterHistory
	if f.Search {
		hist = &t.searchHistory
	}
	switch {
	case k.Code == term.KeyEsc:
		t.filter = nil
	case k == term.Ctrl('r'):
		f.Literal = !f.Literal
	case k.Code == term.KeyUp || k == term.Ctrl('p'):
		hist.step(f, -1)
	case k.Code == term.KeyDown || k == term.Ctrl('n'):
		hist.step(f, 1)
	case k.Code == term.KeyEnter && !f.Valid():
		// The bar stays open, marked invalid, to fix the expression.
	case k.Code == term.KeyEnter && f.Search:
		// An empty search ends searching.
		m.Search, m.SearchLiteral = string(f.Query), f.Literal
		t.filter = nil
		hist.add(m.Search, f.Literal)
		if m.Search != "" {
			t.searchNext(true)
		}
	case k.Code == term.KeyTab && f.Search:
		// Search has no modes.
	case k.Code == term.KeyEnter:
		// An empty filter matches everything, listing all entries flat.
		m.Query, m.DiffsOnly, m.Literal, m.Filtered = string(f.Query), f.DiffsOnly, f.Literal, true
		t.filter = nil
		hist.add(m.Query, f.Literal)
		// Select the first result, unless the selection is one.
		if rows := m.Rows(); len(rows) > 0 && !m.SelectionShown(rows) {
			m.Select(rows[0].Node)
		}
	case k.Code == term.KeyTab:
		f.DiffsOnly = !f.DiffsOnly
	case k.Code == term.KeyLeft || k == term.Ctrl('b'):
		f.Cursor = max(f.Cursor-1, 0)
	case k.Code == term.KeyRight || k == term.Ctrl('f'):
		f.Cursor = min(f.Cursor+1, len(f.Query))
	case k.Code == term.KeyHome || k == term.Ctrl('a'):
		f.Cursor = 0
	case k.Code == term.KeyEnd || k == term.Ctrl('e'):
		f.Cursor = len(f.Query)
	case k == term.Ctrl('u'):
		f.Query = slices.Delete(f.Query, 0, f.Cursor)
		f.Cursor = 0
	case k == term.Ctrl('k'):
		f.Query = f.Query[:f.Cursor]
	case k == term.Key{Code: term.KeyBackspace, Alt: true}:
		start := wordBack(f.Query, f.Cursor)
		f.Query = slices.Delete(f.Query, start, f.Cursor)
		f.Cursor = start
	case k == term.Key{Code: term.KeyRune, Rune: 'd', Alt: true}:
		f.Query = slices.Delete(f.Query, f.Cursor, wordForward(f.Query, f.Cursor))
	case k == term.Key{Code: term.KeyRune, Rune: 'f', Alt: true}:
		f.Cursor = wordForward(f.Query, f.Cursor)
	case k == term.Key{Code: term.KeyRune, Rune: 'b', Alt: true}:
		f.Cursor = wordBack(f.Query, f.Cursor)
	case k.Code == term.KeyBackspace:
		if f.Cursor > 0 {
			f.Query = slices.Delete(f.Query, f.Cursor-1, f.Cursor)
			f.Cursor--
		}
	case k.Code == term.KeyDelete || k == term.Ctrl('d'):
		if f.Cursor < len(f.Query) {
			f.Query = slices.Delete(f.Query, f.Cursor, f.Cursor+1)
		}
	case k.Code == term.KeyRune && !k.Alt && unicode.IsPrint(k.Rune):
		// Every printable character is typed, never a shortcut.
		f.Query = slices.Insert(f.Query, f.Cursor, k.Rune)
		f.Cursor++
	default:
		return false
	}
	return true
}

// isWord reports whether r is part of a word, as in bash: a letter or a
// digit.
func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// wordBack returns where the word before i starts, past any non-word
// characters just before i, as bash's Alt+Backspace and Alt+b see it.
func wordBack(q []rune, i int) int {
	for i > 0 && !isWord(q[i-1]) {
		i--
	}
	for i > 0 && isWord(q[i-1]) {
		i--
	}
	return i
}

// wordForward returns where the word at or after i ends, as bash's Alt+f
// and Alt+d see it.
func wordForward(q []rune, i int) int {
	for i < len(q) && !isWord(q[i]) {
		i++
	}
	for i < len(q) && isWord(q[i]) {
		i++
	}
	return i
}

// sideKey returns n on one side, to read.
func (t *TreeScreen) sideKey(n *scan.Node, right bool) sideKey {
	if right {
		return sideKey{t.roots[1], n.Path()}
	}
	return sideKey{t.roots[0], n.Path()}
}

// openFile opens the diff view on a pair of files, or on a file present on one
// side only, against an empty side. A side that can't be shown, as one not
// UTF-8 or too large, keeps the tree and says why.
func (t *TreeScreen) openFile(n *scan.Node) {
	path := n.Path()
	side := func(right bool) Source {
		sk := t.sideKey(n, right)
		return Source{
			Root: sk.root, Rel: path, HasRel: true,
			Missing: n.Side(right).Kind == scan.Missing,
		}
	}
	d := NewDiffScreen(t.app, DiffConfig{
		Styles: t.styles, Home: t.home, Context: t.context,
		Left: side(false), Right: side(true),
		Back: func() { t.app.Main = t },
		// Edited in the diff view, the entry's panels are read again too, as
		// after editing in the tree.
		Edited: func() {
			t.statGen++
			t.Recheck(path)
		},
		Compare: t.opts.Compare,
	})
	if err := d.Load(); err != nil {
		t.app.ShowError(unableToOpen, errorTexts(err)...)
		return
	}
	t.app.Main = d
}

// whyNotOpened says why a pair that isn't a directory doesn't open in
// the diff view, for the error popup, or is nil if it does: only regular
// files do, or one missing. Sides of different types are a list.
func whyNotOpened(n *scan.Node) []string {
	opens := func(k scan.Kind) bool { return k == scan.File || k == scan.Missing }
	l, r := n.L.Kind, n.R.Kind
	switch {
	case opens(l) && opens(r):
		return nil
	case l == r:
		return []string{fmt.Sprintf("Only regular files can be opened (both sides are %s).", kindPlural(l))}
	case opens(l):
		return []string{fmt.Sprintf("Only regular files can be opened (the right side is %s).", kindName(r))}
	case opens(r):
		return []string{fmt.Sprintf("Only regular files can be opened (the left side is %s).", kindName(l))}
	}
	return []string{
		"Only regular files can be opened:",
		fmt.Sprintf("The left side is %s.", kindName(l)),
		fmt.Sprintf("The right side is %s.", kindName(r)),
	}
}

// kindName names an entry of kind k, after "is".
func kindName(k scan.Kind) string {
	switch k {
	case scan.Unknown:
		return "of unknown type"
	case scan.FIFO:
		return "a FIFO"
	}
	return "a " + k.String()
}

// kindPlural names entries of kind k, after "are".
func kindPlural(k scan.Kind) string {
	switch k {
	case scan.Unknown:
		return "of unknown type"
	case scan.FIFO:
		return "FIFOs"
	}
	return k.String() + "s"
}

// historyLimit is how many queries a bar's history keeps.
const historyLimit = 100

// history is the queries applied in the filter or search bar, oldest
// first, recalled with Up and Down as in bash.
type history struct {
	entries []historyEntry
	// pos is the entry shown while the bar is open; len(entries) is the
	// query being typed, kept in draft while an entry is shown.
	pos   int
	draft historyEntry
}

// historyEntry is a query and whether it is literal text.
type historyEntry struct {
	query   string
	literal bool
}

// open starts browsing the history as a bar opens.
func (h *history) open() {
	h.pos = len(h.entries)
}

// add records an applied query, moving a repeat of an earlier one to the
// end. An empty query is not recorded.
func (h *history) add(query string, literal bool) {
	if query == "" {
		return
	}
	e := historyEntry{query, literal}
	h.entries = slices.DeleteFunc(h.entries, func(o historyEntry) bool { return o == e })
	h.entries = append(h.entries, e)
	if len(h.entries) > historyLimit {
		h.entries = slices.Delete(h.entries, 0, len(h.entries)-historyLimit)
	}
}

// step shows in f the next older (dir -1) or newer (dir 1) entry,
// skipping any the same as f shows, or after the newest the query that
// was being typed. Past the oldest, f is left as it is.
func (h *history) step(f *view.FilterBar, dir int) {
	cur := historyEntry{string(f.Query), f.Literal}
	pos := h.pos + dir
	for pos >= 0 && pos < len(h.entries) && h.entries[pos] == cur {
		pos += dir
	}
	if pos < 0 {
		return
	}
	if h.pos == len(h.entries) {
		h.draft = cur
	}
	h.pos = min(pos, len(h.entries))
	e := h.draft
	if h.pos < len(h.entries) {
		e = h.entries[h.pos]
	}
	f.Query, f.Literal = []rune(e.query), e.literal
	f.Cursor = len(f.Query)
}
