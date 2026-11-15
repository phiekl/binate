// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/diff"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/tree"
)

func TestPreviewRows(t *testing.T) {
	for _, tt := range []struct{ rows, tree, preview int }{
		{5, 5, 0},
		{15, 15, 0},
		// From 3 rows, once the tree keeps 10, up to 8.
		{16, 10, 5},
		{26, 17, 8},
		{60, 51, 8},
	} {
		if tree, preview := previewRows(tt.rows); tree != tt.tree || preview != tt.preview {
			t.Errorf("previewRows(%d) = %d, %d, want %d, %d", tt.rows, tree, preview, tt.tree, tt.preview)
		}
	}
}

// numbered returns lines 1 to n, with the lines in changed replaced.
func numbered(n int, changed ...int) diff.Text {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if slices.Contains(changed, i) {
			fmt.Fprintf(&b, "x%d\n", i)
		} else {
			fmt.Fprintf(&b, "%d\n", i)
		}
	}
	return diff.Split([]byte(b.String()))
}

// previewSection returns the rows between the first two dividers.
func previewSection(scr *term.Screen) []string {
	parts := strings.Split(panes2(scr), "├\n")
	if len(parts) < 3 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(parts[1], "\n"), "\n")
}

func TestTreePreview(t *testing.T) {
	left := writeTree(t, map[string]string{"f": "x", "same": "s"})
	right := writeTree(t, map[string]string{"f": "y", "same": "s"})
	s := scanned(t, left, right)
	m := tree.New(s.Root)
	// The entry selected, which the preview is asked about.
	sel := m.Root.Find("f")
	v := &TreeView{Styles: colors.Default(), Left: "/l", Right: "/r"}
	stats := func(*scan.Node) [2]StatSide { return [2]StatSide{{Missing: true}, {Missing: true}} }
	draw := func(h int, pv *DiffView, ok bool) *term.Screen {
		t.Helper()
		var asked *scan.Node
		preview := func(n *scan.Node) (*DiffView, bool) {
			asked = n
			return pv, ok
		}
		scr := term.NewScreen(80, h)
		v.Draw(scr, m, TreeStatus{Stats: stats, Preview: preview})
		if asked != nil && asked != sel {
			t.Errorf("preview of %v", asked)
		}
		return scr
	}

	// The first changes, without the unchanged lines before them, and a
	// note for those left out.
	pv := NewPreview(numbered(60), numbered(60, 20, 40, 50))
	pv.Styles = colors.Default()
	scr := draw(40, pv, true)
	want := []string{
		pair("19 19", "19 19"),
		marked("20 20", '|', "20 x20"),
		pair("21 21", "21 21"),
		pair("··· 17 unchanged lines ···", "··· 17 unchanged lines ···"),
		pair("39 39", "39 39"),
		marked("40 40", '|', "40 x40"),
		pair("41 41", "41 41"),
		pair("··· Enter shows all ···", "··· Enter shows all ···"),
	}
	got := previewSection(scr)
	trim := func(lines []string) []string {
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = strings.Join(strings.Fields(l), " ")
		}
		return out
	}
	if strings.Join(trim(got), "\n") != strings.Join(trim(want), "\n") {
		t.Errorf("preview:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if v.Rows() != 16 {
		t.Errorf("tree rows %d with a preview, want 16", v.Rows())
	}

	// Unchanged lines left out after the last change say nothing.
	pv = NewPreview(numbered(60), numbered(60, 20))
	pv.Styles = colors.Default()
	got = previewSection(draw(40, pv, true))
	if text := strings.Join(got, "\n"); strings.Contains(text, "Enter") || !strings.Contains(text, "x20") {
		t.Errorf("preview of one change:\n%s", text)
	}

	// While it is read, and why it can't be shown.
	if got := previewSection(draw(40, nil, true)); len(got) != 8 || got[0] != "…" {
		t.Errorf("preview being read: %q", got)
	}
	got = previewSection(draw(40, &DiffView{SideErrors: [2]string{"", "Unable to show files larger than 10 MiB."}}, true))
	if got[0] != pair("Unable to compare files due to an", "Unable to show files larger than 10") {
		t.Errorf("preview of a binary file: %q", got)
	}

	// An entry without a preview keeps its rows, saying why, so the tree
	// keeps its height.
	v.Compare = scan.DefaultAttrs
	m.ShowSame = true
	sel = m.Root.Find("same")
	m.Select(sel)
	got = previewSection(draw(40, pv, false))
	if len(got) != 8 || strings.Join(strings.Fields(got[0]), " ") != "··· contents identical ··· ··· contents identical ···" || strings.TrimSpace(strings.Join(got[1:], "")) != "" || v.Rows() != 16 {
		t.Errorf("no preview: %d tree rows, %q", v.Rows(), got)
	}

	// Without room for one, the tree keeps all its rows.
	scr = draw(30, pv, true)
	if n := strings.Count(panes2(scr), "├"); n != 1 || v.Rows() != TreeRows(30) {
		t.Errorf("no room: %d dividers, %d tree rows:\n%s", n, v.Rows(), scr.String())
	}
}

func TestNoPreview(t *testing.T) {
	file := scan.Side{Kind: scan.File}
	for _, tt := range []struct {
		n       scan.Node
		compare scan.Attrs
		want    string
	}{
		{scan.Node{L: scan.Side{Kind: scan.Dir}, R: scan.Side{Kind: scan.Dir}}, scan.DefaultAttrs, "directories have no preview"},
		{scan.Node{L: scan.Side{Kind: scan.Dir}}, scan.DefaultAttrs, "directories have no preview"},
		{scan.Node{L: scan.Side{Kind: scan.Symlink}, R: scan.Side{Kind: scan.Symlink}}, scan.DefaultAttrs, "only files have a preview"},
		{scan.Node{L: file, R: scan.Side{Kind: scan.Symlink}, State: scan.TypeDiff}, scan.DefaultAttrs, "only files have a preview"},
		{scan.Node{L: file, R: file, State: scan.Differ}, scan.Mode, "contents not compared"},
		{scan.Node{L: file, R: file, State: scan.Pending, Data: scan.Pending}, scan.DefaultAttrs, "contents not compared yet"},
		{scan.Node{L: file, R: file, State: scan.Unverified, Data: scan.Unverified}, scan.DefaultAttrs, "contents not verified"},
		// Differing in mode, with contents quick mode couldn't tell apart.
		{scan.Node{L: file, R: file, State: scan.Differ, Own: scan.Mode, Data: scan.Unverified}, scan.DefaultAttrs, "contents not verified"},
		{scan.Node{L: file, R: file, State: scan.Differ, Own: scan.Mode, Data: scan.Same}, scan.DefaultAttrs, "contents identical"},
		{scan.Node{L: file, R: file, State: scan.Same, Data: scan.Same}, scan.DefaultAttrs, "contents identical"},
	} {
		if got := noPreview(&tt.n, tt.compare); got != tt.want {
			t.Errorf("%+v, %v: %q, want %q", tt.n, tt.compare, got, tt.want)
		}
	}
}

func TestDotted(t *testing.T) {
	if got := dotted("empty", 13); got != "··· empty ···" {
		t.Errorf("dotted in 13 columns: %q", got)
	}
	if got := dotted("empty", 12); got != "empty" {
		t.Errorf("dotted in 12 columns: %q", got)
	}
}

func TestMoreChanges(t *testing.T) {
	for _, tt := range []struct {
		w    int
		want string
	}{{40, "··· more changes, Enter shows all ···"}, {36, "··· Enter shows all ···"}, {10, "···"}} {
		if got := moreChanges(tt.w); got != tt.want {
			t.Errorf("moreChanges(%d) = %q, want %q", tt.w, got, tt.want)
		}
	}
}

func TestPreviewWithoutChanges(t *testing.T) {
	for _, tt := range []struct{ l, r, want string }{
		{"", "", "empty"},
		{"a\n", "a\r\n", "only line ends differ"},
		{"a\n", "a\n", "no differences"},
	} {
		pv := NewPreview(diff.Split([]byte(tt.l)), diff.Split([]byte(tt.r)))
		pv.Styles = colors.Default()
		scr := term.NewScreen(80, 10)
		pv.drawPreview(scr, Layout(80), 1, 5)
		got := strings.Join(strings.Fields(strings.Split(scr.String(), "\n")[1]), " ")
		if want := "··· " + tt.want + " ··· ··· " + tt.want + " ···"; got != want {
			t.Errorf("%q and %q: %q, want %q", tt.l, tt.r, got, want)
		}
	}
}
