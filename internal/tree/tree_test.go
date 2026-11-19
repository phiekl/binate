// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package tree

import (
	"slices"
	"strings"
	"testing"

	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
)

// build makes a tree from lines of "mark path", in tree order, where a path
// ending in "/" is a directory on the sides the mark says it's on.
func build(spec string) *scan.Node {
	root := &scan.Node{L: scan.Side{Kind: scan.Dir}, R: scan.Side{Kind: scan.Dir}}
	for _, line := range strings.Split(strings.TrimSpace(spec), "\n") {
		mark, p, _ := strings.Cut(strings.TrimSpace(line), " ")
		dir := strings.HasSuffix(p, "/")
		p = strings.TrimSuffix(p, "/")
		parent := root
		if i := strings.LastIndex(p, "/"); i >= 0 {
			parent = root.Find(p[:i])
			p = p[i+1:]
		}
		kind := scan.File
		if dir {
			kind = scan.Dir
		}
		n := &scan.Node{Name: p, Parent: parent, L: scan.Side{Kind: kind}, R: scan.Side{Kind: kind}}
		switch mark {
		case "=":
			n.State = scan.Same
		case "|":
			n.State = scan.Differ
		case "?":
			n.State = scan.Unverified
		case "<":
			n.State, n.R.Kind = scan.LeftOnly, scan.Missing
		case ">":
			n.State, n.L.Kind = scan.RightOnly, scan.Missing
		case "!":
			n.State, n.R.Kind = scan.TypeDiff, scan.File
		}
		parent.Children = append(parent.Children, n)
	}
	return root
}

const sample = `
| a/
| a/b/
| a/b/x
= a/b/y
= a/same
< a/gone/
< a/gone/one
< a/gone/two
= same/
= same/z
! typed/
? unread
| changed
> new
= top
`

func paths(rows []Row) string {
	var out []string
	for _, r := range rows {
		out = append(out, strings.Repeat(" ", r.Depth)+r.Node.Path())
	}
	return strings.Join(out, "\n")
}

func selectedPath(m *Model) string {
	if n := m.SelectedNode(); n != nil {
		return n.Path()
	}
	return ""
}

func TestHidesIdenticalAtStart(t *testing.T) {
	m := New(build(sample))
	m.ExpandAll()
	if got, want := paths(m.Rows()), "a\n a/b\n  a/b/x\n a/gone\n  a/gone/one\n  a/gone/two\ntyped\nunread\nchanged\nnew"; got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	m.ShowSame = true
	if got, want := paths(m.Rows()), "a\n a/b\n  a/b/x\n  a/b/y\n a/same\n a/gone\n  a/gone/one\n  a/gone/two\nsame\n same/z\ntyped\nunread\nchanged\nnew\ntop"; got != want {
		t.Errorf("rows with identical:\n%s\nwant:\n%s", got, want)
	}
}

func TestExpansionAfterLoading(t *testing.T) {
	root := build("| a/")
	m := New(root)
	m.Rows()
	// The scan discovers a nested directory after the first draw.
	a := root.Find("a")
	b := &scan.Node{Name: "b", Parent: a, L: scan.Side{Kind: scan.Dir}, State: scan.LeftOnly}
	b.Children = []*scan.Node{{Name: "x", Parent: b, L: scan.Side{Kind: scan.File}, State: scan.LeftOnly}}
	a.Children = append(a.Children, b)
	if got := paths(m.Rows()); got != "a" {
		t.Errorf("rows while scanning: %q, want a", got)
	}
	m.ExpandAll()
	if got, want := paths(m.Rows()), "a\n a/b\n  a/b/x"; got != want {
		t.Errorf("rows after scan progress:\n%s\nwant:\n%s", got, want)
	}
	// A user's collapse survives a rescan, while other directories stay expanded.
	m.Collapse()
	m.SetRoot(build("| a/\n< a/b/\n< a/b/x"))
	if got := paths(m.Rows()); got != "a" {
		t.Errorf("rows after rescan: %q, want a", got)
	}
	m.Toggle()
	if got, want := paths(m.Rows()), "a\n a/b\n  a/b/x"; got != want {
		t.Errorf("rows after reopening:\n%s\nwant:\n%s", got, want)
	}
}

func TestExpandCollapse(t *testing.T) {
	m := New(build(sample))
	m.CollapseAll()
	m.Expand()
	if got, want := paths(m.Rows()), "a\n a/b\n a/gone\ntyped\nunread\nchanged\nnew"; got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	m.Move(1)
	m.Toggle()
	m.Move(1)
	if selectedPath(m) != "a/b/x" {
		t.Errorf("selected %q", selectedPath(m))
	}
	// h on a file selects the parent; again collapses it; again moves up.
	m.Collapse()
	if selectedPath(m) != "a/b" {
		t.Errorf("selected %q", selectedPath(m))
	}
	m.Collapse()
	if m.Expanded(m.Root.Find("a/b")) {
		t.Error("a/b still expanded")
	}
	m.Collapse()
	if selectedPath(m) != "a" {
		t.Errorf("selected %q", selectedPath(m))
	}
	// No parent above the top level.
	m.Collapse()
	m.Collapse()
	if selectedPath(m) != "a" || m.Expanded(m.Root.Find("a")) {
		t.Errorf("selected %q", selectedPath(m))
	}
}

func TestExpandAllCollapseAll(t *testing.T) {
	m := New(build(sample))
	m.CollapseAll()
	m.ExpandAll()
	want := "a\n a/b\n  a/b/x\n a/gone\n  a/gone/one\n  a/gone/two\ntyped\nunread\nchanged\nnew"
	if got := paths(m.Rows()); got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	m.Select(m.Root.Find("a/gone/two"))
	m.CollapseAll()
	if got := paths(m.Rows()); got != "a\ntyped\nunread\nchanged\nnew" {
		t.Errorf("rows:\n%s", got)
	}
	if selectedPath(m) != "a" {
		t.Errorf("selected %q, want the nearest shown parent", selectedPath(m))
	}
}

func TestExpandSubtree(t *testing.T) {
	for _, alreadyExpanded := range []bool{false, true} {
		m := New(build(sample))
		if alreadyExpanded {
			m.Expand()
		}
		m.ExpandSubtree()
		if got, want := paths(m.Rows()), "a\n a/b\n  a/b/x\n a/gone\n  a/gone/one\n  a/gone/two\ntyped\nunread\nchanged\nnew"; got != want {
			t.Errorf("already expanded %v, rows:\n%s\nwant:\n%s", alreadyExpanded, got, want)
		}
		if selectedPath(m) != "a" || m.ShowSame || m.Expanded(m.Root.Find("same")) || m.Expanded(m.Root.Find("typed")) {
			t.Error("expanding the subtree changed selection, identical visibility or siblings")
		}
		// Files and filtered results do not change directory expansion.
		m.Select(m.Root.Find("a/b/x"))
		m.ExpandSubtree()
		if selectedPath(m) != "a/b/x" {
			t.Error("expanding a file changed the selection")
		}
		m.CollapseAll()
		m.Query, m.Filtered = "a", true
		m.ExpandSubtree()
		if m.Expanded(m.Root.Find("a")) {
			t.Error("expanded a filtered result")
		}
	}
}

func TestCollapseSubtree(t *testing.T) {
	m := New(build(sample))
	m.ExpandAll()
	m.Select(m.Root.Find("a"))
	m.CollapseSubtree()
	if got, want := paths(m.Rows()), "a\ntyped\nunread\nchanged\nnew"; got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	// Expanding again shows one level: the directories below were collapsed too.
	m.Expand()
	if got, want := paths(m.Rows()), "a\n a/b\n a/gone\ntyped\nunread\nchanged\nnew"; got != want {
		t.Errorf("rows after expanding again:\n%s\nwant:\n%s", got, want)
	}
	if !m.Expanded(m.Root.Find("same")) || !m.Expanded(m.Root.Find("typed")) {
		t.Error("collapsing the subtree changed unrelated directories")
	}
	// On a file, the directory holding it is collapsed and selected, and
	// an already collapsed directory has its hidden expansion cleared.
	m.ExpandSubtree()
	m.Select(m.Root.Find("a/b/x"))
	m.CollapseSubtree()
	if selectedPath(m) != "a/b" || m.Expanded(m.Root.Find("a/b")) || !m.Expanded(m.Root.Find("a")) {
		t.Errorf("on a file: selected %q, a/b expanded %v", selectedPath(m), m.Expanded(m.Root.Find("a/b")))
	}
	m.ExpandSubtree()
	m.Select(m.Root.Find("a"))
	m.Collapse()
	m.CollapseSubtree()
	m.Expand()
	if m.Expanded(m.Root.Find("a/gone")) || selectedPath(m) != "a" {
		t.Error("collapsing a collapsed directory left the directories below it expanded")
	}
	// A file at the top has no directory to collapse.
	m.Select(m.Root.Find("changed"))
	m.CollapseSubtree()
	if selectedPath(m) != "changed" || !m.Expanded(m.Root.Find("a")) {
		t.Error("collapsing from a top-level file changed the tree")
	}
	// Filtered results are left alone.
	m.Select(m.Root.Find("a"))
	m.Query, m.Filtered = "a", true
	m.CollapseSubtree()
	if !m.Expanded(m.Root.Find("a")) {
		t.Error("collapsed a filtered result")
	}
}

func TestHiddenSelectionReturns(t *testing.T) {
	m := New(build(sample))
	m.ShowSame = true
	m.Select(m.Root.Find("same"))
	m.ShowSame = false
	if got := selectedPath(m); got != "a" {
		t.Errorf("selected %q while hidden, want its nearest shown row", got)
	}
	m.ShowSame = true
	if got := selectedPath(m); got != "same" {
		t.Errorf("selected %q once shown again", got)
	}
	m.ShowSame = false
	m.Move(1)
	m.ShowSame = true
	if got := selectedPath(m); got == "same" {
		t.Errorf("selection returned after the cursor moved")
	}
}

func TestMovePaging(t *testing.T) {
	m := New(build(sample))
	m.Move(100)
	if selectedPath(m) != "new" {
		t.Errorf("selected %q", selectedPath(m))
	}
	m.Move(-2)
	if selectedPath(m) != "unread" {
		t.Errorf("selected %q", selectedPath(m))
	}
	m.MoveTo(true)
	if selectedPath(m) != "a" {
		t.Errorf("selected %q", selectedPath(m))
	}
}

func TestJumps(t *testing.T) {
	m := New(build(sample))
	m.CollapseAll()
	var got []string
	for range 7 {
		if !m.Jump(true) {
			t.Fatal("no stop")
		}
		got = append(got, selectedPath(m))
	}
	// a and a/b are on both sides: not stops. a/gone is one stop. The
	// unverified row is not a stop. The jump wraps.
	want := "a/b/x a/gone typed changed new a/b/x a/gone"
	if strings.Join(got, " ") != want {
		t.Errorf("forward stops %q, want %q", strings.Join(got, " "), want)
	}
	if !m.Expanded(m.Root.Find("a/b")) || m.Expanded(m.Root.Find("a/gone")) {
		t.Error("jumping didn't expand just the parents")
	}
	got = nil
	for range 3 {
		m.Jump(false)
		got = append(got, selectedPath(m))
	}
	if want := "a/b/x new changed"; strings.Join(got, " ") != want {
		t.Errorf("backward stops %q, want %q", strings.Join(got, " "), want)
	}

	// A directory whose own attributes differ is a stop, before what is
	// below it.
	m = New(build(sample))
	m.Root.Find("a/b").Own = scan.Mode
	m.CollapseAll()
	got = nil
	for range 3 {
		m.Jump(true)
		got = append(got, selectedPath(m))
	}
	if want := "a/b a/b/x a/gone"; strings.Join(got, " ") != want {
		t.Errorf("stops with a directory's mode %q, want %q", strings.Join(got, " "), want)
	}

	same := New(build("= a\n= b"))
	if same.Jump(true) {
		t.Error("jumped in a tree without differences")
	}
}

func TestFilter(t *testing.T) {
	m := New(build(sample))
	m.Query, m.Filtered = "O", true
	if got, want := paths(m.Rows()), "a/gone\na/gone/one\na/gone/two\ntop"; got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	for _, r := range m.Rows() {
		if !r.Flat {
			t.Errorf("filter row %q not flat", r.Node.Path())
		}
	}
	m.DiffsOnly = true
	if got, want := paths(m.Rows()), "a/gone\na/gone/one\na/gone/two"; got != want {
		t.Errorf("diffs-only rows:\n%s\nwant:\n%s", got, want)
	}
	m.Query = ""
	if got, want := paths(m.Rows()), "a\na/b\na/b/x\na/gone\na/gone/one\na/gone/two\ntyped\nunread\nchanged\nnew"; got != want {
		t.Errorf("all differences:\n%s\nwant:\n%s", got, want)
	}
	// Expanding is unavailable; jumps stay within the rows.
	m.ExpandAll()
	m.Expand()
	m.Select(m.Root.Find("a/gone/one"))
	m.Jump(true)
	if selectedPath(m) != "a/gone/two" {
		t.Errorf("jumped to %q within the filter", selectedPath(m))
	}
}

func TestFilterPatterns(t *testing.T) {
	m := New(build(sample))
	m.Filtered = true
	for _, c := range []struct {
		query   string
		literal bool
		want    string
	}{
		// A regular expression, ignoring case.
		{"^T", false, "a/gone/two\ntyped\ntop"},
		{"o$", false, "a/gone/two"},
		{"^(one|two)$", false, "a/gone/one\na/gone/two"},
		// Literal text, with no special characters.
		{"^T", true, ""},
		{"gone", true, "a/gone"},
		// An invalid expression matches nothing.
		{"(", false, ""},
	} {
		m.Query, m.Literal = c.query, c.literal
		if got := paths(m.Rows()); got != c.want {
			t.Errorf("%q (literal %v) rows:\n%s\nwant:\n%s", c.query, c.literal, got, c.want)
		}
	}
	// Searching too.
	m.Filtered = false
	m.Search = "^c.*d$"
	if found, _ := m.SearchNext(true); !found || selectedPath(m) != "changed" {
		t.Errorf("regex search selected %q", selectedPath(m))
	}
	m.SearchLiteral = true
	if found, _ := m.SearchNext(true); found {
		t.Errorf("literal search matched %q", selectedPath(m))
	}
	m.Search = "."
	if found, _ := m.SearchNext(true); found {
		t.Errorf("literal search for . matched %q", selectedPath(m))
	}
}

func TestReveal(t *testing.T) {
	m := New(build(sample))
	m.Query, m.Filtered = "z", true
	m.Reveal(m.Root.Find("same/z"))
	if m.Filtered || !m.ShowSame || !m.Expanded(m.Root.Find("same")) || selectedPath(m) != "same/z" {
		t.Errorf("reveal: filtered %v, show same %v, selected %q", m.Filtered, m.ShowSame, selectedPath(m))
	}
}

func TestSelectionSurvivesRescan(t *testing.T) {
	m := New(build(sample))
	m.ExpandAll()
	m.Select(m.Root.Find("a/gone/two"))
	m.SetRoot(build(sample))
	if selectedPath(m) != "a/gone/two" {
		t.Errorf("selected %q after a rescan", selectedPath(m))
	}
	// Gone after a rescan: its parent.
	m.SetRoot(build("| a/\n< a/gone/\n< a/gone/one"))
	if selectedPath(m) != "a/gone" {
		t.Errorf("selected %q after the entry went", selectedPath(m))
	}
}

func TestCount(t *testing.T) {
	c := Count(build(sample))
	want := Counts{Total: 11, Same: 4, Data: 2, Type: 1, LeftOnly: 2, RightOnly: 1, Unverified: 1}
	if c != want {
		t.Errorf("Count = %+v, want %+v", c, want)
	}
}

func TestCountKinds(t *testing.T) {
	// A pair that differs in more than one kind counts under each, and a
	// directory on both sides under its own attributes.
	root := build("| d/\n| d/f\n| g")
	root.Find("d").Own = scan.Mode
	root.Find("d/f").Own = scan.Content | scan.Mtime
	root.Find("g").Own = scan.Owner
	want := Counts{Total: 2, Data: 1, Meta: 2, Mtime: 1}
	if c := Count(root); c != want {
		t.Errorf("Count = %+v, want %+v", c, want)
	}
}

func TestSearch(t *testing.T) {
	m := New(build(sample))
	m.ExpandAll()
	m.Search = "NE"
	// Forward from the first row, ignoring case and wrapping at the end.
	var got []string
	var wraps []bool
	for range 4 {
		found, wrapped := m.SearchNext(true)
		if !found {
			t.Fatal("no match")
		}
		got, wraps = append(got, selectedPath(m)), append(wraps, wrapped)
	}
	if want := "a/gone a/gone/one new a/gone"; strings.Join(got, " ") != want || !slices.Equal(wraps, []bool{false, false, false, true}) {
		t.Errorf("forward matches %q, wrapped %v; want %q, wrapping last", strings.Join(got, " "), wraps, want)
	}
	if _, wrapped := m.SearchNext(false); selectedPath(m) != "new" || !wrapped {
		t.Errorf("backward from the first match: %q, wrapped %v", selectedPath(m), wrapped)
	}
	// Entries in collapsed directories match, and are shown; hidden
	// identical entries don't, until shown.
	m.CollapseAll()
	m.Search = "x"
	if found, _ := m.SearchNext(true); !found || selectedPath(m) != "a/b/x" || !m.Expanded(m.Root.Find("a/b")) || !m.Expanded(m.Root.Find("a")) {
		t.Errorf("search in collapsed directories selected %q", selectedPath(m))
	}
	m.Search = "z"
	if found, _ := m.SearchNext(true); found {
		t.Errorf("found hidden same/z at %q", selectedPath(m))
	}
	m.ShowSame = true
	if found, _ := m.SearchNext(true); !found || selectedPath(m) != "same/z" || !m.Expanded(m.Root.Find("same")) {
		t.Errorf("search for a shown identical entry selected %q", selectedPath(m))
	}
	m.ShowSame = false
	// Within a filter, only its results match.
	m.Query, m.Filtered = "o", true
	m.Search = "t"
	m.MoveTo(true)
	got = nil
	for range 3 {
		m.SearchNext(true)
		got = append(got, selectedPath(m))
	}
	if want := "a/gone/two top a/gone/two"; strings.Join(got, " ") != want {
		t.Errorf("matches in the filter %q, want %q", strings.Join(got, " "), want)
	}
	m.Search = ""
	if found, _ := m.SearchNext(true); found {
		t.Error("an empty search matched")
	}
}

func TestIgnored(t *testing.T) {
	root := build(sample)
	// As when entries on one side only, and of different types, aren't
	// compared: what is below them too.
	for _, p := range []string{"a/gone", "a/gone/one", "a/gone/two", "typed", "new"} {
		root.Find(p).State = scan.Ignored
	}
	m := New(root)
	m.ExpandAll()
	m.ShowSame = true
	if got, want := paths(m.Rows()), "a\n a/b\n  a/b/x\n  a/b/y\n a/same\nsame\n same/z\nunread\nchanged\ntop"; got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	m.Query, m.Filtered = "e", true
	if got, want := paths(m.Rows()), "a/same\nsame\nunread\nchanged"; got != want {
		t.Errorf("filter rows:\n%s\nwant:\n%s", got, want)
	}
	m.Filtered, m.Query = false, ""
	m.CollapseAll()
	var got []string
	for range 3 {
		m.Jump(true)
		got = append(got, selectedPath(m))
	}
	if want := "a/b/x changed a/b/x"; strings.Join(got, " ") != want {
		t.Errorf("stops %q, want %q", strings.Join(got, " "), want)
	}
	m.Search = "new"
	if found, _ := m.SearchNext(true); found {
		t.Errorf("found an ignored entry: %q", selectedPath(m))
	}
	if c, want := Count(root), (Counts{Total: 7, Same: 4, Data: 2, Unverified: 1}); c != want {
		t.Errorf("Count = %+v, want %+v", c, want)
	}
}

func TestStatuses(t *testing.T) {
	root := build(`
= kept/
= kept/f
= plain/
= plain/g
| changed
> new
> untracked/
> untracked/a
> untracked/b
< gone
= top
`)
	set := func(p, st string) { root.Find(p).Status = source.Status{Staged: st[0], Unstaged: st[1]} }
	set("kept/f", "D ")
	set("changed", "MM")
	set("new", "A ")
	set("untracked", "??")
	set("untracked/a", "??")
	set("untracked/b", "??")
	set("gone", " D")
	root.Find("top").Status = source.Status{Staged: ' ', Unstaged: ' '}
	scan.Aggregate(root)
	m := New(root)
	m.ExpandAll()
	// Identical entries with a status are shown, and their directories.
	if got, want := paths(m.Rows()), "kept\n kept/f\nchanged\nnew\nuntracked\n untracked/a\n untracked/b\ngone"; got != want {
		t.Errorf("rows:\n%s\nwant:\n%s", got, want)
	}
	m.Filtered, m.DiffsOnly = true, true
	if got, want := paths(m.Rows()), "kept/f\nchanged\nnew\nuntracked\nuntracked/a\nuntracked/b\ngone"; got != want {
		t.Errorf("diffs only:\n%s\nwant:\n%s", got, want)
	}
	gone := &scan.Node{Name: "gone", Parent: root, State: scan.Same, Gone: scan.File, Status: source.Status{Staged: 'D', Unstaged: ' '}}
	root.Children = append(root.Children, gone)
	c := Count(root)
	// What is gone from both sides counts by its status alone.
	if c.Staged != 4 || c.Unstaged != 2 || c.Untracked != 2 || c.Unmerged != 0 || c.Same != 3 || c.Total != 8 {
		t.Errorf("Count = %+v", c)
	}
	set("changed", "UU")
	if c := Count(root); c.Unmerged != 1 || c.Staged != 3 || c.Unstaged != 1 {
		t.Errorf("Count with a conflict = %+v", c)
	}
}

func TestCountFileWithEntriesBelow(t *testing.T) {
	// A file on one side, with entries gone from both below it, counts as
	// a file, and they by their statuses.
	root := build("> repl")
	repl := root.Find("repl")
	repl.Children = []*scan.Node{{Name: "old", Parent: repl, State: scan.Same, Gone: scan.File, Status: source.Status{Staged: 'D', Unstaged: ' '}}}
	if c := Count(root); c.Total != 1 || c.RightOnly != 1 || c.Staged != 1 {
		t.Errorf("Count = %+v", c)
	}
}
