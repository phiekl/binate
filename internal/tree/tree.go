// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package tree is the tree view's model: which rows are shown, which
// is selected, expansion, the filter and difference jumps. Nodes are
// identified by path, so expansion and the selection survive a rescan.
//
// The caller must hold the scan's lock while calling any method, since the
// scan writes the nodes.
package tree

import (
	"regexp"
	"strings"

	"pxy.se/go/binate/internal/scan"
)

// Row is one shown row.
type Row struct {
	Node  *scan.Node
	Depth int
	// Flat is set for a filter result, shown with its parent's path.
	Flat bool
}

// Model is the tree view's state.
type Model struct {
	Root     *scan.Node
	expanded map[string]bool
	// ShowSame shows identical entries, hidden at start.
	ShowSame bool
	// selected is the path of the selected node. It may be hidden, as by
	// collapsing a parent; the nearest shown parent is selected then.
	selected string

	// Query and DiffsOnly are the applied filter; Filtered is set while
	// it applies. Literal matches Query as text, not a regular expression.
	Query     string
	DiffsOnly bool
	Filtered  bool
	Literal   bool
	// Search is the applied search, among the shown rows; "" when there
	// is none. SearchLiteral matches it as text.
	Search        string
	SearchLiteral bool

	rows              []Row
	queryRe, searchRe pattern
}

// Compile returns the pattern matching names by query, ignoring case: a
// regular expression, or with literal the text itself.
func Compile(query string, literal bool) (*regexp.Regexp, error) {
	if literal {
		query = regexp.QuoteMeta(query)
	}
	return regexp.Compile("(?i)" + query)
}

// pattern is a compiled query, kept while the query is unchanged.
type pattern struct {
	query   string
	literal bool
	done    bool
	re      *regexp.Regexp
}

// match reports whether name matches query; an invalid one matches
// nothing.
func (p *pattern) match(query string, literal bool, name string) bool {
	if !p.done || p.query != query || p.literal != literal {
		p.re, _ = Compile(query, literal)
		p.query, p.literal, p.done = query, literal, true
	}
	return p.re != nil && p.re.MatchString(name)
}

// New returns a model of the tree at root, with directories collapsed.
func New(root *scan.Node) *Model {
	return &Model{Root: root, expanded: map[string]bool{}}
}

// SetRoot replaces the tree, as after a rescan, keeping expansion and the
// selection by path.
func (m *Model) SetRoot(root *scan.Node) {
	m.Root = root
}

// Rows returns the shown rows.
func (m *Model) Rows() []Row {
	m.rows = nil
	if m.Filtered {
		m.walkFiltered(m.Root)
	} else {
		m.walk(m.Root, 0)
	}
	return m.rows
}

// shown reports whether n is shown, unless in a collapsed directory: an
// ignored entry never is, nor what is below it.
func (m *Model) shown(n *scan.Node) bool {
	return n.State != scan.Ignored && (m.ShowSame || n.State != scan.Same)
}

func (m *Model) walk(n *scan.Node, depth int) {
	for _, c := range n.Children {
		if !m.shown(c) {
			continue
		}
		m.rows = append(m.rows, Row{Node: c, Depth: depth})
		if c.IsDir() && m.Expanded(c) {
			m.walk(c, depth+1)
		}
	}
}

// Matches reports whether n is a filter result.
func (m *Model) Matches(n *scan.Node) bool {
	if m.DiffsOnly && n.State == scan.Same {
		return false
	}
	return m.queryRe.match(m.Query, m.Literal, n.Name)
}

func (m *Model) walkFiltered(n *scan.Node) {
	for _, c := range n.Children {
		if c.State == scan.Ignored {
			continue
		}
		if m.Matches(c) {
			m.rows = append(m.rows, Row{Node: c, Flat: true})
		}
		m.walkFiltered(c)
	}
}

// Expanded reports whether the directory at n is expanded.
func (m *Model) Expanded(n *scan.Node) bool {
	return m.expanded[n.Path()]
}

// Selected returns the index of the selected row in rows, which Rows
// returned: the selected node's, or its nearest shown parent's, or 0.
func (m *Model) Selected(rows []Row) int {
	n := m.Root.Find(m.selected)
	p := m.selected
	for n == nil && p != "" {
		// Gone after a rescan: try its parents.
		if i := strings.LastIndex(p, "/"); i >= 0 {
			p = p[:i]
		} else {
			p = ""
		}
		n = m.Root.Find(p)
	}
	for ; n != nil && n.Parent != nil; n = n.Parent {
		for i, r := range rows {
			if r.Node == n {
				return i
			}
		}
	}
	return 0
}

// SelectionShown reports whether the selected node itself is among rows,
// rather than a parent of it.
func (m *Model) SelectionShown(rows []Row) bool {
	n := m.Root.Find(m.selected)
	for _, r := range rows {
		if r.Node == n {
			return true
		}
	}
	return false
}

// SelectedNode returns the selected row's node, or nil if no rows are
// shown.
func (m *Model) SelectedNode() *scan.Node {
	rows := m.Rows()
	if len(rows) == 0 {
		return nil
	}
	return rows[m.Selected(rows)].Node
}

// Select selects n.
func (m *Model) Select(n *scan.Node) {
	m.selected = n.Path()
}

// Move moves the selection by delta rows, within the rows shown.
func (m *Model) Move(delta int) {
	rows := m.Rows()
	if len(rows) == 0 {
		return
	}
	i := min(max(m.Selected(rows)+delta, 0), len(rows)-1)
	m.Select(rows[i].Node)
}

// MoveTo selects the first (top true) or last row.
func (m *Model) MoveTo(top bool) {
	rows := m.Rows()
	if len(rows) == 0 {
		return
	}
	if top {
		m.Select(rows[0].Node)
	} else {
		m.Select(rows[len(rows)-1].Node)
	}
}

// Expand expands the selected directory.
func (m *Model) Expand() {
	if n := m.SelectedNode(); n != nil && n.IsDir() && !m.Filtered {
		m.expanded[n.Path()] = true
	}
}

// ExpandSubtree expands the selected directory and all directories below
// it, including one-sided directories. Unrelated directories stay as they are.
func (m *Model) ExpandSubtree() {
	if n := m.SelectedNode(); n != nil && n.IsDir() && !m.Filtered {
		m.expanded[n.Path()] = true
		m.expandChildren(n)
	}
}

// Collapse collapses the selected directory, or if it is collapsed or not
// a directory, selects its parent.
func (m *Model) Collapse() {
	n := m.SelectedNode()
	if n == nil || m.Filtered {
		return
	}
	if n.IsDir() && m.Expanded(n) {
		delete(m.expanded, n.Path())
		return
	}
	if n.Parent != nil && n.Parent.Parent != nil {
		m.Select(n.Parent)
	}
}

// CollapseSubtree collapses the selected directory and every directory
// below it, so expanding it again shows one level. For a file, it does so
// for the directory holding it, which it selects. Unrelated directories
// stay as they are.
func (m *Model) CollapseSubtree() {
	n := m.SelectedNode()
	if n == nil || m.Filtered {
		return
	}
	if !n.IsDir() {
		if n.Parent == nil || n.Parent.Parent == nil {
			return
		}
		n = n.Parent
		m.Select(n)
	}
	p := n.Path()
	for q := range m.expanded {
		if q == p || strings.HasPrefix(q, p+"/") {
			delete(m.expanded, q)
		}
	}
}

// Toggle expands or collapses the selected directory.
func (m *Model) Toggle() {
	n := m.SelectedNode()
	if n == nil || !n.IsDir() || m.Filtered {
		return
	}
	m.expanded[n.Path()] = !m.Expanded(n)
}

// ExpandAll expands every directory, one-sided ones included.
func (m *Model) ExpandAll() {
	m.expandChildren(m.Root)
}

func (m *Model) expandChildren(n *scan.Node) {
	for _, c := range n.Children {
		if c.IsDir() {
			m.expanded[c.Path()] = true
			m.expandChildren(c)
		}
	}
}

// CollapseAll collapses every directory. A selection that is hidden then
// shows as its nearest shown parent.
func (m *Model) CollapseAll() {
	clear(m.expanded)
	// Select what is shown, so moving starts from there.
	if n := m.SelectedNode(); n != nil {
		m.Select(n)
	}
}

// Reveal shows n: it clears the filter and expands n's parents, and
// selects n.
func (m *Model) Reveal(n *scan.Node) {
	m.Filtered = false
	for p := n.Parent; p != nil && p.Parent != nil; p = p.Parent {
		m.expanded[p.Path()] = true
	}
	if n.State == scan.Same {
		m.ShowSame = true
	}
	m.Select(n)
}

// isStop reports whether a difference jump stops at n: a difference that
// isn't a directory present on both sides, or one whose own attributes
// differ, as its mark shows.
func isStop(n *scan.Node) bool {
	if n.BothDirs() {
		return n.Own != 0
	}
	return n.State.IsDiff()
}

// stops lists jump destinations in tree order. Outside a filtered list,
// a directory without a counterpart, or paired with a different type,
// represents its whole subtree, so the traversal skips its descendants.
func (m *Model) stops() []*scan.Node {
	var out []*scan.Node
	if m.Filtered {
		for _, r := range m.Rows() {
			if isStop(r.Node) {
				out = append(out, r.Node)
			}
		}
		return out
	}
	var walk func(*scan.Node)
	walk = func(n *scan.Node) {
		for _, c := range n.Children {
			if c.State == scan.Ignored {
				continue
			}
			if isStop(c) {
				out = append(out, c)
				if !c.BothDirs() {
					continue
				}
			}
			walk(c)
		}
	}
	walk(m.Root)
	return out
}

// Jump moves forward or backward through the tree's differences, cycling
// at either end. It opens the destination's ancestors so the selection is
// visible. With no destination available, it returns false.
func (m *Model) Jump(forward bool) bool {
	target, _ := m.next(m.stops(), forward)
	if target == nil {
		return false
	}
	m.show(target)
	return true
}

// SearchNext selects the next (forward) or previous entry after the
// selection whose name matches Search, ignoring case, in tree order and
// wrapping around at the end as vim does, and expands the directories
// above it. Entries inside collapsed directories are searched, but not
// hidden identical ones; while a filter is applied, only its results. It
// returns whether there was a match, and whether the search wrapped to
// reach it.
func (m *Model) SearchNext(forward bool) (found, wrapped bool) {
	if m.Search == "" {
		return false, false
	}
	var matches []*scan.Node
	match := func(n *scan.Node) {
		if m.searchRe.match(m.Search, m.SearchLiteral, n.Name) {
			matches = append(matches, n)
		}
	}
	if m.Filtered {
		for _, r := range m.Rows() {
			match(r.Node)
		}
	} else {
		var walk func(*scan.Node)
		walk = func(n *scan.Node) {
			for _, c := range n.Children {
				if m.shown(c) {
					match(c)
					walk(c)
				}
			}
		}
		walk(m.Root)
	}
	target, wrapped := m.next(matches, forward)
	if target == nil {
		return false, false
	}
	m.show(target)
	return true, wrapped
}

// next returns the first of nodes, which are in tree order, after the
// selection (forward) or before it, wrapping around at the end, and
// whether it wrapped. It returns nil if nodes is empty.
func (m *Model) next(nodes []*scan.Node, forward bool) (*scan.Node, bool) {
	if len(nodes) == 0 {
		return nil, false
	}
	// Order the selection among the nodes by its position in tree order.
	order := map[*scan.Node]int{}
	i := 0
	var walk func(*scan.Node)
	walk = func(n *scan.Node) {
		for _, c := range n.Children {
			order[c] = i
			i++
			walk(c)
		}
	}
	walk(m.Root)
	cur := -1
	if n := m.Root.Find(m.selected); n != nil {
		cur = order[n]
	} else if sel := m.SelectedNode(); sel != nil {
		cur = order[sel]
	}
	if forward {
		for _, n := range nodes {
			if order[n] > cur {
				return n, false
			}
		}
		return nodes[0], true
	}
	for j := len(nodes) - 1; j >= 0; j-- {
		if order[nodes[j]] < cur {
			return nodes[j], false
		}
	}
	return nodes[len(nodes)-1], true
}

// show selects n, expanding the directories above it unless a filter,
// which lists n flat, is applied.
func (m *Model) show(n *scan.Node) {
	if !m.Filtered {
		for p := n.Parent; p != nil && p.Parent != nil; p = p.Parent {
			m.expanded[p.Path()] = true
		}
	}
	m.Select(n)
}

// Counts are the pairs of the whole tree by state. Total counts each pair
// once: directories present on both sides are not counted, only what is
// in them. Each kind of difference counts the pairs that differ in it, so
// a pair that differs in more than one kind counts under each, and a
// directory on both sides counts under its own attributes' differences.
type Counts struct {
	Total                     int
	Same, LeftOnly, RightOnly int
	Type, Data, Meta, Mtime   int
	Unverified, Pending       int
}

// Count counts the pairs below root.
func Count(root *scan.Node) Counts {
	var c Counts
	own := func(a scan.Attrs) {
		if a&scan.Data != 0 {
			c.Data++
		}
		if a&scan.Meta != 0 {
			c.Meta++
		}
		if a&scan.Mtime != 0 {
			c.Mtime++
		}
	}
	var walk func(*scan.Node)
	walk = func(n *scan.Node) {
		for _, ch := range n.Children {
			if ch.State == scan.Ignored {
				continue
			}
			oneSidedDir := (ch.State == scan.LeftOnly || ch.State == scan.RightOnly) && len(ch.Children) > 0
			if ch.BothDirs() {
				own(ch.Own)
				walk(ch)
				continue
			}
			if oneSidedDir {
				walk(ch)
				continue
			}
			c.Total++
			switch ch.State {
			case scan.Same:
				c.Same++
			case scan.Differ:
				if ch.Own == 0 {
					c.Data++
				}
				own(ch.Own)
			case scan.TypeDiff:
				c.Type++
			case scan.LeftOnly:
				c.LeftOnly++
			case scan.RightOnly:
				c.RightOnly++
			case scan.Unverified:
				c.Unverified++
			case scan.Pending:
				c.Pending++
			}
		}
	}
	walk(root)
	return c
}
