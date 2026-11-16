// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/tree"
	"pxy.se/go/binate/internal/view"
)

func TestTreeExpandsWhenScanFinishes(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(left, "a/b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "a/b/only.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pause after the first directory is listed, before its contents load.
	ready, resume := make(chan struct{}, 1), make(chan struct{})
	resumeScan := sync.OnceFunc(func() { close(resume) })
	s := scan.Start(source.Disk(left), source.Disk(right), scan.Options{Workers: 1}, func(done bool) {
		if !done {
			select {
			case ready <- struct{}{}:
			default:
			}
			<-resume
		}
	})
	defer func() { resumeScan(); s.Cancel() }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not report progress")
	}
	treeScreen := &TreeScreen{
		scan: s, model: tree.New(s.Root), expandAfterScan: true,
		view:  &view.TreeView{Styles: colors.Default(), Left: left, Right: right},
		roots: [2]source.Root{source.Disk(left), source.Disk(right)},
	}
	paths := func() string {
		s.Lock()
		defer s.Unlock()
		var paths []string
		for _, row := range treeScreen.model.Rows() {
			paths = append(paths, row.Node.Path())
		}
		return strings.Join(paths, " ")
	}
	scr := term.NewScreen(80, 8)
	treeScreen.Draw(scr)
	if got := paths(); got != "a" || !strings.Contains(scr.String(), "1 entry") {
		t.Fatalf("while scanning, rows %q:\n%s", got, scr.String())
	}
	resumeScan()
	s.Wait()
	scr.Clear()
	treeScreen.Draw(scr)
	if got := paths(); got != "a a/b a/b/only.txt" || strings.Contains(scr.String(), "entr") {
		t.Fatalf("after scanning, rows %q:\n%s", got, scr.String())
	}
	// Expansion happens once: later redraws leave a user's collapse alone.
	treeScreen.Key(term.Rune('-'))
	scr.Clear()
	treeScreen.Draw(scr)
	if got := paths(); got != "a" {
		t.Errorf("rows after collapsing and redrawing: %q, want a", got)
	}
	for _, input := range []string{"L", "\x1b[1;2C"} {
		treeScreen.Key(term.Rune('-'))
		treeScreen.Key(term.Rune('l'))
		key, used := term.ParseKey([]byte(input), true)
		if used != len(input) || !treeScreen.Key(key) {
			t.Fatalf("recursive expansion key %q was not handled", input)
		}
		if got := paths(); got != "a a/b a/b/only.txt" {
			t.Errorf("rows after %q on an open directory: %q", input, got)
		}
		treeScreen.Key(term.Rune('-'))
		treeScreen.model.Query, treeScreen.model.Filtered = "a", true
		treeScreen.Key(key)
		if !strings.Contains(treeScreen.message, "filter is applied") {
			t.Errorf("recursive expansion with a filter: %q", treeScreen.message)
		}
		treeScreen.model.Filtered = false
	}
	for _, input := range []string{"H", "\x1b[1;2D"} {
		treeScreen.Key(term.Rune('+'))
		treeScreen.model.Select(treeScreen.model.Root.Find("a"))
		key, used := term.ParseKey([]byte(input), true)
		if used != len(input) || !treeScreen.Key(key) {
			t.Fatalf("recursive collapse key %q was not handled", input)
		}
		treeScreen.Key(term.Rune('l'))
		if got := paths(); got != "a a/b" {
			t.Errorf("rows after %q and expanding again: %q", input, got)
		}
		treeScreen.model.Query, treeScreen.model.Filtered = "a", true
		treeScreen.Key(key)
		if !strings.Contains(treeScreen.message, "filter is applied") {
			t.Errorf("recursive collapse with a filter: %q", treeScreen.message)
		}
		treeScreen.model.Filtered = false
	}
	// / opens the search bar instead, which Esc closes.
	treeScreen.Key(term.Rune('/'))
	if treeScreen.filter == nil || !treeScreen.filter.Search {
		t.Errorf("/ opened %+v, want the search bar", treeScreen.filter)
	}
	treeScreen.Key(term.Key{Code: term.KeyEsc})
	// F opens it; Shift+l and Shift+h remain text while it is being
	// edited, and Tab switches between all entries and diffs only.
	treeScreen.Key(term.Rune('F'))
	treeScreen.Key(term.Rune('L'))
	treeScreen.Key(term.Rune('H'))
	if got := string(treeScreen.filter.Query); got != "aLH" {
		t.Errorf("filter query %q, want aLH", got)
	}
	for _, want := range []bool{true, false, true} {
		treeScreen.Key(term.Key{Code: term.KeyTab})
		if treeScreen.filter == nil || treeScreen.filter.DiffsOnly != want {
			t.Fatalf("Tab in the filter bar: %+v, want diffs only %v", treeScreen.filter, want)
		}
	}
	treeScreen.Key(term.Key{Code: term.KeyEnter})
	if m := treeScreen.model; !m.Filtered || m.Query != "aLH" || !m.DiffsOnly {
		t.Errorf("applied filter %q, diffs only %v, filtered %v", m.Query, m.DiffsOnly, m.Filtered)
	}
	// An empty filter is applied too, matching every entry.
	treeScreen.Key(term.Key{Code: term.KeyEsc})
	treeScreen.Key(term.Rune('F'))
	if treeScreen.filter == nil || len(treeScreen.filter.Query) != 0 {
		t.Fatalf("F after Esc opened %+v, want an empty filter bar", treeScreen.filter)
	}
	treeScreen.Key(term.Key{Code: term.KeyEnter})
	if m := treeScreen.model; !m.Filtered || m.Query != "" || m.DiffsOnly {
		t.Errorf("empty filter: query %q, diffs only %v, filtered %v", m.Query, m.DiffsOnly, m.Filtered)
	}
	// Queries are regular expressions; an invalid one keeps the bar open.
	treeScreen.Key(term.Key{Code: term.KeyEsc})
	for _, bar := range []rune{'F', '/'} {
		treeScreen.Key(term.Rune(bar))
		treeScreen.Key(term.Rune('('))
		treeScreen.Key(term.Key{Code: term.KeyEnter})
		if f := treeScreen.filter; f == nil || f.Literal {
			t.Fatalf("%c: Enter on an invalid expression left %+v", bar, f)
		}
		// Ctrl+r switches to literal text, which is valid.
		treeScreen.Key(term.Ctrl('r'))
		treeScreen.Key(term.Key{Code: term.KeyEnter})
		if treeScreen.filter != nil {
			t.Fatalf("%c: Enter on literal text left the bar open", bar)
		}
		// The bar opens in the mode last applied.
		treeScreen.Key(term.Rune(bar))
		if f := treeScreen.filter; f == nil || !f.Literal {
			t.Errorf("%c reopened %+v, want literal", bar, f)
		}
		treeScreen.Key(term.Ctrl('r'))
		if f := treeScreen.filter; f.Literal {
			t.Errorf("%c: Ctrl+r did not switch back to regex", bar)
		}
		treeScreen.Key(term.Key{Code: term.KeyEsc})
	}
	if m := treeScreen.model; m.Query != "(" || !m.Literal || m.Search != "(" || !m.SearchLiteral {
		t.Errorf("applied filter %q literal %v, search %q literal %v", m.Query, m.Literal, m.Search, m.SearchLiteral)
	}
}

// TestHistory checks recalling applied queries in the filter and search
// bars with Up and Down, as in bash.
func TestHistory(t *testing.T) {
	ts := &TreeScreen{model: tree.New(&scan.Node{})}
	apply := func(bar rune, query string, literal bool) {
		ts.treeKey(term.Rune(bar))
		ts.filter.Query, ts.filter.Cursor, ts.filter.Literal = []rune(query), len(query), literal
		ts.filterKey(term.Key{Code: term.KeyEnter})
		if ts.filter != nil {
			t.Fatalf("%c did not apply %q", bar, query)
		}
	}
	up, down := term.Key{Code: term.KeyUp}, term.Key{Code: term.KeyDown}
	// shows reports the bar's query, with literal text marked, and checks
	// the cursor is at its end.
	shows := func() string {
		f := ts.filter
		if f.Cursor != len(f.Query) {
			t.Errorf("cursor at %d in %q", f.Cursor, string(f.Query))
		}
		if f.Literal {
			return "=" + string(f.Query)
		}
		return string(f.Query)
	}
	apply('/', "a", false)
	apply('/', "b", true)
	apply('/', "", false)
	apply('/', "c", false)
	apply('/', "a", false)
	apply('F', "f", false)
	// The search bar recalls its own queries, newest first, each in its
	// mode, without repeats or empty ones; past the oldest, Up stays.
	ts.treeKey(term.Rune('/'))
	ts.filterKey(term.Rune('x'))
	var got []string
	for _, k := range []term.Key{up, up, term.Ctrl('p'), up, down, term.Ctrl('n'), down, down} {
		ts.filterKey(k)
		got = append(got, shows())
	}
	if want := "a c =b =b c a x x"; strings.Join(got, " ") != want {
		t.Errorf("search history %q, want %q", strings.Join(got, " "), want)
	}
	ts.filterKey(term.Key{Code: term.KeyEsc})
	// The filter bar opens with the applied filter, which Up skips.
	ts.treeKey(term.Rune('F'))
	ts.filterKey(up)
	if got := shows(); got != "f" {
		t.Errorf("filter history recalled %q, want f", got)
	}
	apply('F', "g", false)
	ts.treeKey(term.Rune('F'))
	ts.filterKey(up)
	if got := shows(); got != "f" {
		t.Errorf("filter history recalled %q after g, want f", got)
	}
	ts.filterKey(down)
	if got := shows(); got != "g" {
		t.Errorf("Down restored %q, want g", got)
	}
	// The history is kept to its limit.
	for i := range historyLimit + 5 {
		ts.filterHistory.add(fmt.Sprint(i), false)
	}
	if h := ts.filterHistory.entries; len(h) != historyLimit || h[0].query != "5" {
		t.Errorf("history of %d starting with %q", len(h), h[0].query)
	}
}

// TestFilterEditing checks the filter bar's line editing keys, which work
// as in bash. A | in the text marks the cursor.
func TestFilterEditing(t *testing.T) {
	ctrl := term.Ctrl
	alt := func(r rune) term.Key { return term.Key{Code: term.KeyRune, Rune: r, Alt: true} }
	altBS := term.Key{Code: term.KeyBackspace, Alt: true}
	for _, c := range []struct {
		before string
		key    term.Key
		after  string
	}{
		{"foo bar|baz", ctrl('u'), "|baz"},
		{"|foo", ctrl('u'), "|foo"},
		{"foo bar|baz", ctrl('k'), "foo bar|"},
		{"foo|", ctrl('k'), "foo|"},
		// Alt+Backspace deletes back to the start of a word, past the
		// punctuation and spaces before the cursor.
		{"src/main.go|", altBS, "src/main.|"},
		{"src/main.|", altBS, "src/|"},
		{"foo bar  |x", altBS, "foo |x"},
		{"fo|o", altBS, "|o"},
		{"|foo", altBS, "|foo"},
		{"--|", altBS, "|"},
		{"åäö ÿ|", altBS, "åäö |"},
		// Alt+d deletes to the end of a word, and Alt+f moves there.
		{"src|/main.go", alt('d'), "src|.go"},
		{"s|rc/main", alt('d'), "s|/main"},
		{"foo|  ", alt('d'), "foo|"},
		{"foo|", alt('d'), "foo|"},
		{"|src/main.go", alt('f'), "src|/main.go"},
		{"src|/main.go", alt('f'), "src/main|.go"},
		{"src/main|.go", alt('f'), "src/main.go|"},
		{"go|", alt('f'), "go|"},
		// Alt+b moves to the start of a word.
		{"src/main.go|", alt('b'), "src/main.|go"},
		{"src/main.|go", alt('b'), "src/|main.go"},
		{"src/ma|in.go", alt('b'), "src/|main.go"},
		{"--|x", alt('b'), "|--x"},
		{"|go", alt('b'), "|go"},
		// Ctrl+b, Ctrl+f and Ctrl+d move and delete by a character.
		{"ab|c", ctrl('b'), "a|bc"},
		{"|abc", ctrl('b'), "|abc"},
		{"a|bc", ctrl('f'), "ab|c"},
		{"abc|", ctrl('f'), "abc|"},
		{"a|bc", ctrl('d'), "a|c"},
		{"abc|", ctrl('d'), "abc|"},
		{"|", ctrl('d'), "|"},
		{"a|bc", term.Key{Code: term.KeyHome}, "|abc"},
		{"a|bc", ctrl('a'), "|abc"},
		{"a|bc", term.Key{Code: term.KeyEnd}, "abc|"},
		{"a|bc", ctrl('e'), "abc|"},
	} {
		before := []rune(c.before)
		cursor := slices.Index(before, '|')
		f := &view.FilterBar{Query: slices.Delete(before, cursor, cursor+1), Cursor: cursor}
		ts := &TreeScreen{filter: f}
		if !ts.filterKey(c.key) {
			t.Errorf("%q: %v not handled", c.before, c.key)
			continue
		}
		got := string(slices.Insert(slices.Clone(f.Query), f.Cursor, '|'))
		if got != c.after || ts.filter == nil {
			t.Errorf("%q, %v: %q, want %q", c.before, c.key, got, c.after)
		}
	}
}

// Enter on a directory found by the filter shows it in the tree as it was,
// without collapsing it, though every directory is expanded after the scan.
func TestEnterRevealsFilteredDirectory(t *testing.T) {
	left, right := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(left, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(left, "sub/x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := scan.Start(source.Disk(left), source.Disk(right), scan.Options{}, nil)
	s.Wait()
	ts := &TreeScreen{
		app: &App{}, scan: s, model: tree.New(s.Root), expandAfterScan: true,
		view:  &view.TreeView{Styles: colors.Default(), Left: left, Right: right},
		roots: [2]source.Root{source.Disk(left), source.Disk(right)},
	}
	ts.Draw(term.NewScreen(80, 30))
	for _, k := range []term.Key{term.Rune('F'), term.Rune('s'), term.Rune('u'), term.Rune('b'),
		{Code: term.KeyEnter}, {Code: term.KeyEnter}} {
		ts.Key(k)
	}
	s.Lock()
	defer s.Unlock()
	sub := s.Root.Find("sub")
	if ts.model.Filtered || !ts.model.Expanded(sub) || ts.model.SelectedNode() != sub {
		t.Errorf("filtered %v, expanded %v, selected %v; want the tree with sub expanded and selected",
			ts.model.Filtered, ts.model.Expanded(sub), ts.model.SelectedNode().Path())
	}
}
