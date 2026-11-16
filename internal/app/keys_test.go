// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/tree"
	"pxy.se/go/binate/internal/view"
)

// keyScreen records the keys it is given.
type keyScreen struct {
	keys []term.Key
}

func (k *keyScreen) Draw(*term.Screen)   {}
func (k *keyScreen) Tick() time.Duration { return -1 }
func (k *keyScreen) Key(key term.Key) bool {
	k.keys = append(k.keys, key)
	return true
}

func testApp(w, h int) *App {
	return &App{screen: term.NewScreen(w, h), Styles: colors.Default()}
}

func TestQuitConfirmation(t *testing.T) {
	press := func(a *App, keys ...term.Key) {
		for _, k := range keys {
			a.key(k)
		}
	}
	left, right, tab := term.Key{Code: term.KeyLeft}, term.Key{Code: term.KeyRight}, term.Key{Code: term.KeyTab}
	enter, esc := term.Key{Code: term.KeyEnter}, term.Key{Code: term.KeyEsc}
	for _, tt := range []struct {
		name  string
		keys  []term.Key
		quit  bool
		shown bool
	}{
		{"Ctrl+C", []term.Key{term.Ctrl('c')}, false, true},
		{"Ctrl+C twice", []term.Key{term.Ctrl('c'), term.Ctrl('c')}, true, false},
		{"x", []term.Key{term.Ctrl('c'), term.Rune('x')}, true, false},
		{"X", []term.Key{term.Ctrl('c'), term.Rune('X')}, true, false},
		{"x on Cancel", []term.Key{term.Ctrl('c'), term.Rune('x')}, true, false},
		{"x on Exit", []term.Key{term.Ctrl('c'), left, term.Rune('x')}, true, false},
		{"Enter on Cancel", []term.Key{term.Ctrl('c'), enter}, false, false},
		{"Space on Cancel", []term.Key{term.Ctrl('c'), term.Rune(' ')}, false, false},
		{"Enter on Exit", []term.Key{term.Ctrl('c'), left, enter}, true, false},
		{"Space on Exit", []term.Key{term.Ctrl('c'), tab, term.Rune(' ')}, true, false},
		{"Right stays on Cancel", []term.Key{term.Ctrl('c'), right, enter}, false, false},
		{"Left stays on Exit", []term.Key{term.Ctrl('c'), left, left, enter}, true, false},
		{"back to Cancel", []term.Key{term.Ctrl('c'), left, right, enter}, false, false},
		{"Tab back to Cancel", []term.Key{term.Ctrl('c'), tab, tab, enter}, false, false},
		{"Ctrl+C on Exit", []term.Key{term.Ctrl('c'), left, term.Ctrl('c')}, true, false},
		{"moving keeps the prompt", []term.Key{term.Ctrl('c'), right, tab, left}, false, true},
		{"Esc", []term.Key{term.Ctrl('c'), esc}, false, false},
		{"q cancels", []term.Key{term.Ctrl('c'), term.Rune('q')}, false, false},
		{"y cancels", []term.Key{term.Ctrl('c'), term.Rune('y')}, false, false},
		{"Down cancels", []term.Key{term.Ctrl('c'), {Code: term.KeyDown}}, false, false},
		{"cancelled then x", []term.Key{term.Ctrl('c'), term.Rune('j'), term.Rune('x')}, false, false},
		{"reopened on Cancel", []term.Key{term.Ctrl('c'), left, esc, term.Ctrl('c'), enter}, false, false},
		{"q", []term.Key{term.Rune('q')}, false, false},
		{"Esc alone", []term.Key{esc}, false, false},
	} {
		a := testApp(80, 24)
		main := &keyScreen{}
		a.Main = main
		press(a, tt.keys...)
		if a.quit != tt.quit || a.confirming != tt.shown {
			t.Errorf("%s: quit %v, prompt %v; want %v, %v", tt.name, a.quit, a.confirming, tt.quit, tt.shown)
		}
		// Keys reach the screen only without the prompt.
		for _, k := range main.keys {
			if k != term.Rune('q') && k != esc && !(tt.name == "cancelled then x" && k == term.Rune('x')) {
				t.Errorf("%s: %v passed on", tt.name, k)
			}
		}
	}
	// Too small for the prompt: Ctrl+C quits at once, q and Esc do not.
	for _, k := range []term.Key{term.Rune('q'), term.Ctrl('c'), {Code: term.KeyEsc}} {
		a := testApp(20, 3)
		a.key(k)
		if a.quit != (k == term.Ctrl('c')) {
			t.Errorf("%v on a small terminal: quit %v", k, a.quit)
		}
	}
}

func writeTestFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestTreeKeys(t *testing.T) {
	left := writeTestFiles(t, map[string]string{"a/x": "1", "b": "1", "c": "1", "d": "same"})
	right := writeTestFiles(t, map[string]string{"a/x": "2", "b": "2", "c": "2", "d": "same"})
	s := scan.Start(source.Disk(left), source.Disk(right), scan.Options{}, nil)
	s.Wait()
	a := testApp(80, 24)
	ts := &TreeScreen{
		app: a, scan: s, model: tree.New(s.Root), height: 24,
		view:  &view.TreeView{Styles: colors.Default(), Left: left, Right: right},
		roots: [2]source.Root{source.Disk(left), source.Disk(right)},
	}
	a.Main = ts
	selected := func() string { return ts.model.SelectedNode().Path() }

	ts.Key(term.Rune('G'))
	if selected() != "c" {
		t.Errorf("G selected %q, want the last row", selected())
	}
	ts.Key(term.Rune('g'))
	if selected() != "c" {
		t.Error("a single g moved")
	}
	ts.Key(term.Rune('g'))
	if selected() != "a" {
		t.Errorf("gg selected %q, want the first row", selected())
	}
	// A key between the two g's cancels the first.
	ts.Key(term.Rune('g'))
	ts.Key(term.Rune('j'))
	ts.Key(term.Rune('g'))
	if selected() != "b" {
		t.Errorf("g j g selected %q, want b", selected())
	}

	// Shift+Down and Shift+Up jump between differences; Alt no longer does.
	ts.Key(term.Rune('g'))
	ts.Key(term.Rune('g'))
	ts.Key(term.Key{Code: term.KeyDown, Shift: true})
	if selected() != "a/x" {
		t.Errorf("Shift+Down selected %q, want a/x", selected())
	}
	ts.Key(term.Key{Code: term.KeyDown, Shift: true})
	ts.Key(term.Key{Code: term.KeyUp, Shift: true})
	if selected() != "a/x" {
		t.Errorf("Shift+Up selected %q, want a/x", selected())
	}
	if ts.Key(term.Key{Code: term.KeyDown, Alt: true}) {
		t.Error("Alt+Down is still handled")
	}

	// Space and Backspace mean nothing.
	for _, k := range []term.Key{term.Rune(' '), {Code: term.KeyBackspace}} {
		ts.model.Query, ts.model.Filtered = "x", true
		before := ts.model.Expanded(s.Root.Find("a"))
		if ts.Key(k) || !ts.model.Filtered || ts.model.Expanded(s.Root.Find("a")) != before {
			t.Errorf("%v is still handled", k)
		}
	}
	// Esc clears the filter, and otherwise does nothing: it never quits
	// or hides identical entries.
	ts.Key(term.Key{Code: term.KeyEsc})
	if ts.model.Filtered {
		t.Error("Esc did not clear the filter")
	}
	ts.model.ShowSame = true
	ts.Key(term.Key{Code: term.KeyEsc})
	if a.quit || a.confirming || !ts.model.ShowSame {
		t.Errorf("Esc: quit %v, prompt %v, identical shown %v", a.quit, a.confirming, ts.model.ShowSame)
	}
	// In Help, j, k and the arrows move the selection, which starts on
	// the first row; G and gg go to the ends.
	ts.Key(term.Rune('?'))
	for _, step := range []struct {
		k    term.Key
		want int
	}{
		// The first row with keys follows a title and a line.
		{term.Rune('j'), 3}, {term.Key{Code: term.KeyDown}, 4}, {term.Rune('k'), 3},
		{term.Key{Code: term.KeyUp}, 2}, {term.Key{Code: term.KeyUp}, 2},
		{term.Rune('G'), len(view.TreeHelp) - 1}, {term.Rune('g'), len(view.TreeHelp) - 1}, {term.Rune('g'), 2},
	} {
		if !ts.Key(step.k) || ts.help == nil || ts.help.Selected != step.want {
			t.Fatalf("%v in Help: selected %v, want %d", step.k, ts.help, step.want)
		}
	}
	// Any other key closes Help, without reaching the tree.
	for _, k := range []term.Key{{Code: term.KeyEsc}, term.Rune('?'), term.Rune('*'), term.Rune('q'), {Code: term.KeyEnter}, term.Rune('r')} {
		ts.Key(term.Rune('?'))
		shown := ts.model.ShowSame
		if !ts.Key(k) || ts.help != nil || ts.model.ShowSame != shown {
			t.Errorf("%v in Help: handled, open %v, identical shown %v", k, ts.help != nil, ts.model.ShowSame)
		}
	}
	// q is not a key at all.
	if ts.Key(term.Rune('q')) || a.confirming || a.quit {
		t.Errorf("q: prompt %v, quit %v; want nothing", a.confirming, a.quit)
	}
}

func TestDiffKeys(t *testing.T) {
	var l, r strings.Builder
	for i := range 200 {
		fmt.Fprintf(&l, "line %d\n", i)
		fmt.Fprintf(&r, "line %d\n", i)
	}
	l.WriteString("left\n")
	r.WriteString("right\n")
	dir := writeTestFiles(t, map[string]string{"l": "first\n" + l.String(), "r": "1st\n" + r.String()})
	a := testApp(80, 24)
	d := NewDiffScreen(a, DiffConfig{
		Styles: colors.Default(), Context: 3,
		Left:  Source{Root: source.Disk(filepath.Join(dir, "l"))},
		Right: Source{Root: source.Disk(filepath.Join(dir, "r"))},
	})
	if err := d.Load(); err != nil {
		t.Fatal(err)
	}
	a.Main = d
	d.Key(term.Rune('f')) // the full file, longer than the screen
	d.view.Draw(term.NewScreen(80, 24))
	d.Key(term.Rune('G'))
	end := d.view.Top
	if end == 0 {
		t.Fatal("G did not scroll to the end")
	}
	d.Key(term.Rune('g'))
	d.Key(term.Rune('g'))
	if d.view.Top != 0 {
		t.Errorf("gg left the top at %d", d.view.Top)
	}
	// The first change is at the top; the second at the end.
	d.Key(term.Key{Code: term.KeyDown, Shift: true})
	d.Key(term.Key{Code: term.KeyDown, Shift: true})
	if d.view.Top == 0 {
		t.Error("Shift+Down did not jump to the next change")
	}
	d.Key(term.Key{Code: term.KeyUp, Shift: true})
	if d.view.Top != 0 {
		t.Errorf("Shift+Up left the top at %d", d.view.Top)
	}
	// In two-file mode, Esc does nothing and q asks to quit.
	d.Key(term.Key{Code: term.KeyEsc})
	if a.quit || a.confirming {
		t.Error("Esc quit two-file mode or asked to")
	}
	if d.Key(term.Rune('q')) || a.confirming || a.quit {
		t.Errorf("q: prompt %v, quit %v; want nothing", a.confirming, a.quit)
	}
}

func TestErrorPopup(t *testing.T) {
	a := testApp(80, 24)
	main := &keyScreen{}
	a.Main = main
	// Any key closes it, and goes no further.
	for _, k := range []term.Key{term.Rune('j'), {Code: term.KeyEsc}, {Code: term.KeyEnter}, term.Rune('q')} {
		a.ShowError(unableToEdit, "why")
		a.key(k)
		if a.errTitle != "" || a.confirming || len(main.keys) != 0 {
			t.Errorf("%v: popup %q, prompt %v, passed on %v", k, a.errTitle, a.confirming, main.keys)
		}
	}
	// Ctrl+C closes it and asks to quit.
	a.ShowError(unableToEdit, "why")
	a.key(term.Ctrl('c'))
	if a.errTitle != "" || !a.confirming || a.quit {
		t.Errorf("Ctrl+C: popup %q, prompt %v, quit %v", a.errTitle, a.confirming, a.quit)
	}
}

func TestSearchKeys(t *testing.T) {
	left := writeTestFiles(t, map[string]string{"a/x": "1", "b": "1", "c": "1", "d": "same"})
	right := writeTestFiles(t, map[string]string{"a/x": "2", "b": "2", "c": "2", "d": "same"})
	s := scan.Start(source.Disk(left), source.Disk(right), scan.Options{}, nil)
	s.Wait()
	a := testApp(80, 24)
	ts := &TreeScreen{
		app: a, scan: s, model: tree.New(s.Root), height: 24,
		view:  &view.TreeView{Styles: colors.Default(), Left: left, Right: right},
		roots: [2]source.Root{source.Disk(left), source.Disk(right)},
	}
	a.Main = ts
	m := ts.model
	selected := func() string { return m.SelectedNode().Path() }
	typeKeys := func(text string) {
		for _, r := range text {
			ts.Key(term.Rune(r))
		}
	}
	enter := term.Key{Code: term.KeyEnter}

	// / opens the search bar, where Tab does nothing, and Enter selects
	// the next match after the selection.
	typeKeys("/C")
	ts.Key(term.Key{Code: term.KeyTab})
	if ts.filter.DiffsOnly {
		t.Error("Tab switched the search bar's mode")
	}
	ts.Key(enter)
	if selected() != "c" || m.Search != "C" || ts.filter != nil {
		t.Errorf("search selected %q, search %q, bar %v", selected(), m.Search, ts.filter)
	}
	// n and N go to the next and previous match, saying when they wrap.
	ts.Key(term.Rune('n'))
	if selected() != "c" || !strings.Contains(ts.message, "hit the bottom") {
		t.Errorf("n selected %q, message %q", selected(), ts.message)
	}
	ts.Key(term.Rune('N'))
	if selected() != "c" || !strings.Contains(ts.message, "hit the top") {
		t.Errorf("N selected %q, message %q", selected(), ts.message)
	}
	// Shift+Up and Shift+Down still jump between differences; p means
	// nothing.
	ts.Key(term.Key{Code: term.KeyUp, Shift: true})
	if selected() != "b" {
		t.Errorf("Shift+Up while searching selected %q, want b", selected())
	}
	ts.Key(term.Key{Code: term.KeyDown, Shift: true})
	if selected() != "c" {
		t.Errorf("Shift+Down while searching selected %q, want c", selected())
	}
	if ts.Key(term.Rune('p')) {
		t.Error("p is handled")
	}
	// Esc ends the search first, then clears the filter.
	m.DiffsOnly, m.Filtered = true, true
	ts.Key(term.Key{Code: term.KeyEsc})
	if m.Search != "" || !m.Filtered {
		t.Errorf("Esc: search %q, filtered %v", m.Search, m.Filtered)
	}
	ts.Key(term.Key{Code: term.KeyEsc})
	if m.Filtered {
		t.Error("a second Esc kept the filter")
	}
	// Without a search, n and N jump between differences.
	m.MoveTo(true)
	ts.Key(term.Rune('n'))
	if selected() != "a/x" {
		t.Errorf("n selected %q, want the next difference", selected())
	}
	ts.Key(term.Rune('N'))
	if selected() != "c" {
		t.Errorf("N selected %q, want the previous difference, wrapping", selected())
	}
	// The search finds entries in collapsed directories, and shows them.
	m.CollapseAll()
	m.MoveTo(true)
	typeKeys("/x")
	ts.Key(enter)
	if selected() != "a/x" || !m.Expanded(s.Root.Find("a")) {
		t.Errorf("search in a collapsed directory selected %q", selected())
	}
	// Hidden identical entries are not searched, shown ones are.
	typeKeys("/d")
	ts.Key(enter)
	if selected() != "a/x" || ts.message != "no match for d" {
		t.Errorf("search for a hidden entry selected %q, message %q", selected(), ts.message)
	}
	ts.Key(term.Rune('*'))
	ts.Key(term.Rune('n'))
	if selected() != "d" {
		t.Errorf("search for a shown identical entry selected %q", selected())
	}
	// A search without a match says so; an empty one ends searching.
	typeKeys("/zz")
	ts.Key(enter)
	if selected() != "d" || ts.message != "no match for zz" {
		t.Errorf("search without a match selected %q, message %q", selected(), ts.message)
	}
	ts.Key(term.Rune('/'))
	ts.Key(enter)
	if m.Search != "" {
		t.Errorf("an empty search left %q", m.Search)
	}
	// While a filter is applied, * switches it to diffs-only and back.
	typeKeys("F")
	ts.Key(enter)
	rows := func() int { return len(m.Rows()) }
	all := rows()
	ts.Key(term.Rune('*'))
	if !m.DiffsOnly || rows() != all-1 || !m.ShowSame {
		t.Errorf("* in a filter: diffs-only %v, %d rows of %d, show same %v", m.DiffsOnly, rows(), all, m.ShowSame)
	}
	ts.Key(term.Rune('*'))
	if m.DiffsOnly || rows() != all {
		t.Errorf("* again in a filter: diffs-only %v, %d rows of %d", m.DiffsOnly, rows(), all)
	}
}

func TestCompareKeys(t *testing.T) {
	left := writeTestFiles(t, map[string]string{"content": "1", "mode": "x"})
	right := writeTestFiles(t, map[string]string{"content": "2", "mode": "x"})
	if err := os.Chmod(filepath.Join(right, "mode"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The same size and time: the same in quick mode.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, root := range []string{left, right} {
		if err := os.Chtimes(filepath.Join(root, "content"), old, old); err != nil {
			t.Fatal(err)
		}
	}
	a := testApp(80, 24)
	ts := NewTreeScreen(a, TreeConfig{Styles: colors.Default(), Left: source.Disk(left), Right: source.Disk(right)})
	a.Main = ts
	ts.scan.Wait()
	mark := func(p string) rune {
		ts.scan.Lock()
		defer ts.scan.Unlock()
		return ts.scan.Root.Find(p).Mark()
	}
	space, esc := term.Rune(' '), term.Key{Code: term.KeyEsc}
	// selectRow selects attr's row, moving down from the first.
	selectRow := func(attr scan.Attrs) {
		ts.Key(term.Key{Code: term.KeyHome})
		for i := 0; i < 20 && ts.compare.Attr() != attr; i++ {
			ts.Key(term.Rune('j'))
		}
	}

	if !ts.Key(term.Rune('c')) || ts.compare == nil || ts.compare.Attr() != scan.Left {
		t.Fatal("c didn't open the popup on its first row")
	}
	selectRow(scan.Mode)
	scan1 := ts.scan
	ts.Key(space)
	if mark("mode") != '=' || ts.scan != scan1 {
		t.Errorf("mode off: mark %c, rescanned %v", mark("mode"), ts.scan != scan1)
	}
	if line := ts.compare.Lines[ts.compare.Selected]; line.Keys[0] != "[ ]" {
		t.Errorf("mode's row %+v", line)
	}
	ts.Key(space)
	if mark("mode") != '~' {
		t.Errorf("mode on: mark %c", mark("mode"))
	}

	// The contents go off and on again, at once, as they were read; never
	// to quick mode.
	selectRow(scan.Content)
	for _, want := range []struct {
		quick   bool
		content bool
		mark    rune
	}{{false, false, '='}, {false, true, '|'}, {false, false, '='}, {false, true, '|'}} {
		ts.Key(term.Key{Code: term.KeyEnter})
		if ts.opts.Quick != want.quick || (ts.opts.Compare&scan.Content != 0) != want.content ||
			ts.scan != scan1 {
			t.Errorf("contents: options %+v, rescanned %v", ts.opts, ts.scan != scan1)
		}
		ts.scan.Wait()
		if got := mark("content"); got != want.mark {
			t.Errorf("contents quick %v, compared %v: mark %c, want %c", want.quick, want.content, got, want.mark)
		}
	}

	// A row's letter selects and toggles it: m the mode, t the mtime.
	ts.Key(term.Key{Code: term.KeyHome})
	ts.Key(term.Rune('m'))
	if ts.compare.Attr() != scan.Mode || ts.opts.Compare&scan.Mode != 0 || mark("mode") != '=' {
		t.Errorf("m: selected %v, compare %v, mark %c", ts.compare.Attr(), ts.opts.Compare, mark("mode"))
	}
	ts.Key(term.Rune('m'))
	ts.Key(term.Rune('t'))
	if ts.compare.Attr() != scan.Mtime || ts.opts.Compare != scan.DefaultAttrs|scan.Mtime {
		t.Errorf("t: selected %v, compare %v", ts.compare.Attr(), ts.opts.Compare)
	}
	ts.Key(term.Rune('t'))
	ts.Key(term.Rune('y'))
	if ts.compare.Attr() != scan.Type || ts.opts.Compare&scan.Type != 0 {
		t.Errorf("y: selected %v, compare %v", ts.compare.Attr(), ts.opts.Compare)
	}
	ts.Key(term.Rune('y'))
	// With Alt, a letter is not the row's.
	ts.Key(term.Key{Code: term.KeyRune, Rune: 'm', Alt: true})
	if ts.opts.Compare != scan.DefaultAttrs {
		t.Errorf("Alt+m: compare %v", ts.opts.Compare)
	}

	// * turns everything on, then everything off, and on again.
	for _, want := range []scan.Attrs{scan.AllAttrs, scan.None, scan.AllAttrs} {
		ts.Key(term.Rune('*'))
		if ts.opts.Compare != want {
			t.Errorf("*: compare %v, want %v", ts.opts.Compare, want)
		}
		if want == scan.None && (mark("mode") != '=' || mark("content") != '=') {
			t.Errorf("nothing compared: marks %c %c", mark("mode"), mark("content"))
		}
	}
	ts.Key(term.Rune('t'))
	if ts.opts.Compare != scan.DefaultAttrs {
		t.Fatalf("back to the default: compare %v", ts.opts.Compare)
	}

	// Other keys don't reach the tree; Esc closes the popup.
	shown := ts.model.ShowSame
	ts.Key(term.Rune('G'))
	ts.Key(term.Rune('x'))
	if ts.model.ShowSame != shown || ts.compare == nil || ts.opts.Compare != scan.DefaultAttrs {
		t.Error("x did something")
	}
	if !ts.Key(esc) || ts.compare != nil {
		t.Error("Esc didn't close the popup")
	}

	// Started in quick mode, the contents leave it for good, read by a new
	// scan: then off, and back on in full.
	ts = NewTreeScreen(a, TreeConfig{Styles: colors.Default(), Left: source.Disk(left), Right: source.Disk(right), Options: scan.Options{Quick: true}})
	ts.scan.Wait()
	if got := mark("content"); got != '=' {
		t.Errorf("quick: mark %c", got)
	}
	ts.Key(term.Rune('c'))
	selectRow(scan.Content)
	for _, want := range []struct {
		quick, content bool
		rescan         bool
	}{{false, true, true}, {false, false, false}, {false, true, false}} {
		scan1 = ts.scan
		ts.Key(space)
		if ts.opts.Quick != want.quick || (ts.opts.Compare&scan.Content != 0) != want.content || (ts.scan != scan1) != want.rescan {
			t.Errorf("from quick mode: options %+v, rescanned %v, want %+v", ts.opts, ts.scan != scan1, want)
		}
		ts.scan.Wait()
	}
	if got := mark("content"); got != '|' {
		t.Errorf("read in full: mark %c", got)
	}

	// Contents never read are read by a new scan.
	ts = NewTreeScreen(a, TreeConfig{Styles: colors.Default(), Left: source.Disk(left), Right: source.Disk(right), Options: scan.Options{Compare: scan.Mode}})
	ts.scan.Wait()
	scan1 = ts.scan
	ts.Key(term.Rune('c'))
	selectRow(scan.Content)
	ts.Key(space)
	if ts.scan == scan1 || ts.message != "scanning again to compare contents" {
		t.Errorf("contents not read: rescanned %v, message %q", ts.scan != scan1, ts.message)
	}
	ts.scan.Wait()
	if got := mark("content"); got != '|' {
		t.Errorf("after the new scan: mark %c", got)
	}
}

// An entry that doesn't open in the diff view says why in an error popup,
// staying on the tree.
func TestCantOpen(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	for _, root := range []string{left, right} {
		if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
	}
	a := testApp(80, 24)
	ts := NewTreeScreen(a, TreeConfig{Styles: colors.Default(), Left: source.Disk(left), Right: source.Disk(right)})
	a.Main = ts
	ts.scan.Wait()
	ts.Key(term.Rune('*'))
	ts.scan.Lock()
	ts.model.Select(ts.scan.Root.Find("link"))
	ts.scan.Unlock()
	a.key(term.Key{Code: term.KeyEnter})
	if a.errTitle != unableToOpen || len(a.errTexts) != 1 || a.errTexts[0] != "Only regular files can be opened (both sides are symlinks)." || a.Main != ts || ts.message != "" {
		t.Errorf("popup %q %q, main %T, message %q", a.errTitle, a.errTexts, a.Main, ts.message)
	}
}

// TestDiffEditRefreshesTreePanels checks that an edit in the diff view opened
// from the tree has the tree's panels read the entry again, as an edit in
// the tree does, rather than show its size from before.
func TestDiffEditRefreshesTreePanels(t *testing.T) {
	left := writeTestFiles(t, map[string]string{"f": "12345"})
	right := writeTestFiles(t, map[string]string{"f": "12345"})
	s := scan.Start(source.Disk(left), source.Disk(right), scan.Options{}, nil)
	s.Wait()
	a := testApp(80, 40)
	ts := &TreeScreen{
		app: a, scan: s, model: tree.New(s.Root), height: 40, stats: statReader{app: a},
		view:  &view.TreeView{Styles: colors.Default(), Left: left, Right: right},
		roots: [2]source.Root{source.Disk(left), source.Disk(right)},
	}
	a.Main = ts
	n := s.Root.Find("f")
	size := func() int64 {
		t.Helper()
		for range 500 {
			s.Lock()
			got := ts.panelStats(n)[0]
			s.Unlock()
			if got.Stat != nil {
				return got.Stat.Size
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatal("the panel was never read")
		return 0
	}
	if got := size(); got != 5 {
		t.Fatalf("size %d before the edit", got)
	}
	ts.openFile(n)
	d, ok := a.Main.(*DiffScreen)
	if !ok {
		t.Fatal("The diff view did not open")
	}
	if err := os.WriteFile(filepath.Join(left, "f"), []byte(strings.Repeat("x", 25)), 0o644); err != nil {
		t.Fatal(err)
	}
	d.edited()
	d.back()
	if got := size(); got != 25 {
		t.Errorf("size %d after the edit in the diff view, want 25", got)
	}
}

// Sides not in the file system, as git revisions, record no owners or
// times: those are never compared, whatever is asked.
func TestCompareUnrecorded(t *testing.T) {
	left := writeTestFiles(t, map[string]string{"f": "1"})
	right := writeTestFiles(t, map[string]string{"f": "1"})
	a := testApp(80, 24)
	ts := NewTreeScreen(a, TreeConfig{
		Styles: colors.Default(), Left: otherRoot{source.Disk(left)}, Right: otherRoot{source.Disk(right)},
		Options: scan.Options{Compare: scan.AllAttrs},
	})
	a.Main = ts
	ts.scan.Wait()
	all := scan.AllAttrs &^ (scan.Owner | scan.Mtime)
	if ts.opts.Compare != all || ts.view.Compare != all {
		t.Errorf("compare %v, view %v", ts.opts.Compare, ts.view.Compare)
	}
	ts.Key(term.Rune('c'))
	for _, k := range "ot" {
		ts.Key(term.Rune(k))
		if ts.opts.Compare != all {
			t.Errorf("%c: compare %v", k, ts.opts.Compare)
		}
		if line := ts.compare.Lines[ts.compare.Selected]; line.Keys[0] != "[-]" {
			t.Errorf("%c: row %+v", k, line)
		}
	}
	// * turns all off, and on again, but those.
	ts.Key(term.Rune('*'))
	if ts.opts.Compare != scan.None {
		t.Errorf("* off: compare %v", ts.opts.Compare)
	}
	ts.Key(term.Rune('*'))
	if ts.opts.Compare != all {
		t.Errorf("* on: compare %v", ts.opts.Compare)
	}
}
