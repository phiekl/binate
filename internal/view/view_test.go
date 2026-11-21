// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/stat"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/tree"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	stamp := time.Date(2026, 11, 2, 14, 10, 0, 0, time.Local)
	for p, contents := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(contents, "->") {
			if err := os.Symlink(contents[2:], full); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(full, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// panes returns the rows inside the boxes, without the borders, as the
// left pane, the mark column with a space either side, and the right pane,
// with trailing spaces removed: one line each, ending in a line feed.
func panes(scr *term.Screen) string {
	p := Layout(scr.W)
	border := map[int]bool{
		p.LeftBox: true, p.LeftBox + p.LeftBoxW - 1: true,
		p.RightBox: true, p.RightBox + p.RightBoxW - 1: true,
	}
	var b strings.Builder
	for y := 1; y < scr.H && scr.Cell(0, y).R == '│'; y++ {
		var line strings.Builder
		for x := 0; x < scr.W; x++ {
			if c := scr.Cell(x, y); c.W > 0 && !border[x] {
				line.WriteRune(c.R)
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " ") + "\n")
	}
	return b.String()
}

// lastLine returns the screen's last line, the status bar.
func lastLine(scr *term.Screen) string {
	lines := strings.Split(scr.String(), "\n")
	return lines[scr.H-1]
}

func scanned(t *testing.T, left, right string) *scan.Scan {
	t.Helper()
	s := scan.Start(source.Disk(left), source.Disk(right), scan.Options{}, nil)
	s.Wait()
	return s
}

func TestTreeScreen(t *testing.T) {
	left := writeTree(t, map[string]string{
		"src/main.go": "package main\n",
		"src/old.go":  "x",
		"README":      "hello",
		"same":        "x",
		"link":        "->target",
	})
	right := writeTree(t, map[string]string{
		"src/main.go": "package main // changed\n",
		"README":      "hello",
		"same":        "x",
		"new.txt":     strings.Repeat("x", 4300),
		"link":        "->other",
	})
	s := scanned(t, left, right)
	m := tree.New(s.Root)
	m.ExpandAll()
	v := &TreeView{Styles: colors.Default(), Left: "/l", Right: "/r"}
	// Short enough for no panels.
	scr := term.NewScreen(64, 8)
	v.Draw(scr, m, TreeStatus{})
	want := `  dir │ ▾ src/               =   dir │ ▾ src/
 file │     main.go          |  file │     main.go
 file │     old.go           <
 link │   link -> target     |  link │   link -> other
                             >  file │   new.txt
`
	if got := panes(scr); got != want {
		t.Errorf("panes:\n%s\nwant:\n%s", got, want)
	}
	lines := strings.Split(scr.String(), "\n")
	if !strings.HasPrefix(lines[0], "┌ /l ──") || !strings.Contains(lines[0], "┐   ┌ /r ──") {
		t.Errorf("titles %q", lines[0])
	}
	if !strings.HasSuffix(lines[6], "─ 1/5 ┘   └──────────────────────── 1/5 ┘") {
		t.Errorf("bottom borders %q", lines[6])
	}
	if !strings.HasPrefix(lines[7], " [6 items total] [2 identical] [1 left] [1 right] [2 contents]") {
		t.Errorf("status line %q", lines[7])
	}
	// Each count is in its mark's colour, between dim brackets.
	st := v.Styles
	for _, c := range []struct {
		text  string
		style term.Style
	}{{"[", st.Border}, {"6 items", st.Plain}, {"1 left", st.LeftOnly}, {"1 right", st.RightOnly}, {"2 contents", st.Changed}} {
		x := strings.Index(lines[7], c.text)
		if cell := scr.Cell(len([]rune(lines[7][:x])), 7); cell.Style != c.style {
			t.Errorf("%q in the status bar: style %+v, want %+v", c.text, cell.Style, c.style)
		}
	}

	// The selection's background spans the row, inside the borders.
	for _, x := range []int{1, 30, 32, 34, 62} {
		if c := scr.Cell(x, 1); !strings.HasSuffix(c.Style.SGR, v.Styles.Select.SGR) {
			t.Errorf("selected row without its background at %d: %+v", x, c)
		}
	}
	if c := scr.Cell(0, 1); c.R != '│' || c.Style != v.Styles.Border {
		t.Errorf("border cell %+v", c)
	}
	// Each state's mark keeps its colour; unselected rows have no background.
	for _, tt := range []struct {
		y    int
		mark rune
		fg   term.Style
	}{
		{2, '|', term.Style{SGR: "93"}},
		{3, '<', term.Style{SGR: "91"}},
		{4, '|', term.Style{SGR: "93"}},
		{5, '>', term.Style{SGR: "92"}},
	} {
		if c := scr.Cell(31, tt.y); c.R != tt.mark || c.Style != tt.fg {
			t.Errorf("mark cell %+v, want %c in %+v", c, tt.mark, tt.fg)
		}
		for _, x := range []int{1, 30, 32, 62} {
			if c := scr.Cell(x, tt.y); c.Style != st.Plain {
				t.Errorf("row %d column %d %+v, want plain background", tt.y, x, c)
			}
		}
	}
	// Types keep ls's colours, the rule the borders', and names take the
	// mark's colour without bold. The selection keeps its background.
	if c := scr.Cell(3, 1); c.R != 'd' || c.Style != (term.Style{SGR: "1;34;22"}).On(st.Select) {
		t.Errorf("dir type in the selected row %+v", c)
	}
	// A directory is marked by its own attributes alone, not its entries.
	if c := scr.Cell(31, 1); c.R != '=' || c.Style != st.Plain.On(st.Select) {
		t.Errorf("directory mark in the selected row %+v", c)
	}
	if c := scr.Cell(2, 4); c.R != 'l' || c.Style != st.Link {
		t.Errorf("link type %+v", c)
	}
	if c := scr.Cell(2, 2); c.R != 'f' || c.Style != st.Plain {
		t.Errorf("file type %+v", c)
	}
	if c := scr.Cell(7, 2); c.R != '│' || c.Style != st.Border {
		t.Errorf("rule %+v", c)
	}
	if c := scr.Cell(13, 2); c.R != 'm' || c.Style != (term.Style{SGR: "93;22"}) {
		t.Errorf("name %+v", c)
	}
	if c := scr.Cell(11, 4); c.R != 'l' || c.Style != (term.Style{SGR: "93;22"}) {
		t.Errorf("symlink name %+v", c)
	}
	// Identical and pending filenames are plain; the other states follow
	// their marks, with bold disabled even for !. A directory on both sides
	// follows its own attributes alone.
	m.ShowSame = true
	for _, tt := range []struct {
		state           scan.State
		own             scan.Attrs
		dirSGR, fileSGR string
	}{
		{scan.Pending, 0, "", ""},
		{scan.Same, 0, "", ""},
		{scan.Differ, scan.Content, "", "93;22"},
		{scan.Differ, scan.Mode, "36;22", "96;22"},
		{scan.LeftOnly, 0, "", "91;22"},
		{scan.RightOnly, 0, "", "92;22"},
		{scan.Unverified, 0, "", "90;22"},
		{scan.TypeDiff, 0, "", "95;22"},
	} {
		m.Root.Find("src").State, m.Root.Find("src").Own = tt.state, tt.own&^scan.Content
		m.Root.Find("src/main.go").State, m.Root.Find("src/main.go").Own = tt.state, tt.own
		v.Draw(scr, m, TreeStatus{})
		for _, name := range []struct {
			x, y int
			bg   term.Style
			sgr  string
		}{{11, 1, st.Select, tt.dirSGR}, {13, 2, st.Plain, tt.fileSGR}} {
			if c := scr.Cell(name.x, name.y); c.Style != (term.Style{SGR: name.sgr}).On(name.bg) {
				t.Errorf("state %v name at %d,%d: %+v", tt.state, name.x, name.y, c)
			}
		}
	}
}

func TestTreeEscapesNames(t *testing.T) {
	left := writeTree(t, map[string]string{"evil\x1b[31mname": "x", "rtl\u202etxt.exe": "x"})
	right := writeTree(t, map[string]string{})
	s := scanned(t, left, right)
	m := tree.New(s.Root)
	v := &TreeView{Styles: colors.Default(), Left: "/l", Right: "/r"}
	scr := term.NewScreen(64, 7)
	v.Draw(scr, m, TreeStatus{})
	got := panes(scr)
	if !strings.Contains(got, `evil\x1b[31mname`) || !strings.Contains(got, `rtl\u202etxt.exe`) {
		t.Errorf("screen:\n%s", got)
	}
	// The escape is marked, over the row's background.
	st := v.Styles
	x := 1 + strings.Index(strings.Split(got, "\n")[0], `\x1b`)
	if c := scr.Cell(x, 1); c.Style != st.Escape.On(st.Select) {
		t.Errorf("escape in the selected row %+v", c)
	}
	x = 1 + strings.Index(strings.Split(got, "\n")[1], `\u202e`)
	if c := scr.Cell(x, 2); c.Style != st.Escape {
		t.Errorf("escape cell %+v", c)
	}
	// Without colour, the selection is in reverse video, the escape in it
	// underlined instead, and the rows untinted.
	v.Styles = colors.NoColor()
	v.Draw(scr, m, TreeStatus{})
	x = 1 + strings.Index(strings.Split(got, "\n")[0], `\x1b`)
	if c := scr.Cell(x, 1); c.Style.Attr&term.Underline == 0 || !strings.HasSuffix(c.Style.SGR, "27") {
		t.Errorf("escape in the selected row without colour %+v", c)
	}
	if c := scr.Cell(1, 1); c.Style.Attr&term.Reverse == 0 {
		t.Errorf("selected row without colour %+v", c)
	}
	if c := scr.Cell(1, 2); c.Style != (term.Style{}) {
		t.Errorf("row without colour %+v", c)
	}
	if c := scr.Cell(11, 1); c.R != 'e' || c.Style.Attr&term.Bold != 0 {
		t.Errorf("filename without colour is bold: %+v", c)
	}
	for y := 0; y < scr.H; y++ {
		for x := 0; x < scr.W; x++ {
			if r := scr.Cell(x, y).R; r == 0x1b || r == 0x202e {
				t.Fatalf("raw %U on the screen", r)
			}
		}
	}
}

func TestFilterResultsAndBar(t *testing.T) {
	left := writeTree(t, map[string]string{"deep/er/match.c": "x", "match.h": "x"})
	right := writeTree(t, map[string]string{"deep/er/match.c": "y"})
	s := scanned(t, left, right)
	m := tree.New(s.Root)
	m.Query, m.Filtered = "match", true
	v := &TreeView{Styles: colors.Default(), Left: "/l", Right: "/r"}
	scr := term.NewScreen(64, 7)
	v.Draw(scr, m, TreeStatus{Filter: &FilterBar{Query: []rune("ma"), Cursor: 1}})
	lines := strings.Split(panes(scr), "\n")
	if !strings.HasPrefix(lines[0], " file │ deep/er › match.c") || !strings.HasPrefix(lines[1], " file │ match.h") {
		t.Errorf("screen:\n%s", scr.String())
	}
	// The label says which entries are filtered; nothing is on the right.
	if bar := lastLine(scr); strings.TrimRight(bar, " ") != " Filter (all, regex): ma" || !scr.CursorOn || scr.CursorX != 23 || scr.CursorY != 6 {
		t.Errorf("filter bar %q, cursor %v at %d,%d", bar, scr.CursorOn, scr.CursorX, scr.CursorY)
	}
	if label, query := scr.Cell(1, 6).Style, scr.Cell(22, 6).Style; label != v.Styles.Accent.With(term.Bold) || query != v.Styles.Accent {
		t.Errorf("filter bar styles: label %+v, query %+v", label, query)
	}
	v.Draw(scr, m, TreeStatus{Filter: &FilterBar{Query: []rune("ma"), Cursor: 2, DiffsOnly: true}})
	if bar := lastLine(scr); strings.TrimRight(bar, " ") != " Filter (diffs, regex): ma" || scr.CursorX != 26 {
		t.Errorf("diffs-only filter bar %q, cursor at %d", bar, scr.CursorX)
	}
	v.Draw(scr, m, TreeStatus{Filter: &FilterBar{Query: []rune("ma"), Cursor: 2, Search: true}})
	if bar := lastLine(scr); strings.TrimRight(bar, " ") != " Search (regex): ma" || scr.CursorX != 19 {
		t.Errorf("search bar %q, cursor at %d", bar, scr.CursorX)
	}
	// The label says how the query matches, and whether it is valid.
	for _, c := range []struct {
		bar  FilterBar
		want string
	}{
		{FilterBar{Query: []rune("m("), Literal: true}, " Filter (all, literal): m("},
		{FilterBar{Query: []rune("m(")}, " Filter (all, invalid regex): m("},
		{FilterBar{Query: []rune("m("), Search: true}, " Search (invalid regex): m("},
		{FilterBar{Query: []rune("m("), Search: true, Literal: true}, " Search (literal): m("},
	} {
		v.Draw(scr, m, TreeStatus{Filter: &c.bar})
		if bar := strings.TrimRight(lastLine(scr), " "); bar != c.want {
			t.Errorf("bar %q, want %q", bar, c.want)
		}
	}
	v.Draw(scr, m, TreeStatus{})
	if c := scr.Cell(0, 6); c.Style != v.Styles.Status {
		t.Errorf("status bar style %+v, want %+v", c.Style, v.Styles.Status)
	}
	// The status bar names the applied filter the same way.
	for _, c := range []struct {
		query string
		diffs bool
		want  string
	}{{"match", false, "filter (all): match"}, {"match", true, "filter (diffs): match"}, {"", true, "[filter (diffs)]"}} {
		m.Query, m.DiffsOnly = c.query, c.diffs
		v.Draw(scr, m, TreeStatus{})
		if bar := lastLine(scr); !strings.Contains(bar, c.want) {
			t.Errorf("status bar %q lacks %q", bar, c.want)
		}
	}
	// An applied search shows after the filter.
	m.Search = "tch"
	v.Draw(scr, m, TreeStatus{})
	if bar := lastLine(scr); !strings.Contains(bar, "[filter (diffs)] [search") {
		t.Errorf("status bar %q lacks the search", bar)
	}
	// Literal text, unlike a regular expression, is marked.
	m.Literal, m.SearchLiteral, m.Search = true, true, ""
	v.Draw(scr, m, TreeStatus{})
	if bar := lastLine(scr); !strings.Contains(bar, "[filter (diffs, literal") {
		t.Errorf("status bar %q lacks the literal filter", bar)
	}
	m.Filtered, m.Search = false, "tch"
	v.Draw(scr, m, TreeStatus{})
	if bar := lastLine(scr); !strings.Contains(bar, "[search (literal): tch]") {
		t.Errorf("status bar %q lacks the literal search", bar)
	}
}

func TestLoadingBoxAndStatus(t *testing.T) {
	root := writeTree(t, map[string]string{"a": "x"})
	s := scanned(t, root, root)
	m := tree.New(s.Root)
	m.ShowSame = true
	v := &TreeView{Styles: colors.Default(), Left: "/home/u/src/a", Right: "/home/u/src/b", Home: "/home/u"}
	for _, size := range []struct {
		width, height, x, y, w, h, top int
		path                           string // the path line, "" if left out
	}{
		{40, 5, 2, 1, 36, 3, 2, ""},
		{40, 6, 2, 1, 36, 4, 2, "src/very/deep…irectory/here"},
		{41, 9, 2, 1, 37, 6, 3, "src/very/deep…directory/here"},
		{80, 24, 14, 9, 52, 6, 11, "src/very/deep/path/to/some/directory/here"},
	} {
		scr := term.NewScreen(size.width, size.height)
		v.Draw(scr, m, TreeStatus{
			Scanning: true, Scanned: 1234, Elapsed: 3 * time.Second,
			Current: "src/very/deep/path/to/some/directory/here",
			Stats: func(*scan.Node) [2]StatSide {
				t.Error("entry metadata requested while scanning")
				return [2]StatSide{}
			},
		})
		for _, corner := range []struct {
			x, y int
			r    rune
		}{{size.x, size.y, '┌'}, {size.x + size.w - 1, size.y, '┐'}, {size.x, size.y + size.h - 1, '└'}, {size.x + size.w - 1, size.y + size.h - 1, '┘'}} {
			if c := scr.Cell(corner.x, corner.y); c.R != corner.r {
				t.Errorf("%dx%d loading corner %d,%d: %c, want %c", scr.W, scr.H, corner.x, corner.y, c.R, corner.r)
			}
		}
		for _, xy := range [][2]int{{size.x, size.y}, {size.x + size.w - 1, size.y + 1}, {size.x, size.y + size.h - 1}, {size.x + size.w/2, size.y + size.h - 1}} {
			if c := scr.Cell(xy[0], xy[1]); c.Style != v.Styles.Accent {
				t.Errorf("%dx%d loading border at %d,%d: %+v", scr.W, scr.H, xy[0], xy[1], c.Style)
			}
		}
		// A spinner, then the count and elapsed time, and the path being
		// read, shortened in the middle, centered vertically.
		if c := scr.Cell(size.x+3, size.top); !strings.ContainsRune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏", c.R) || c.Style != v.Styles.Accent.With(term.Bold) {
			t.Errorf("%dx%d spinner %+v", scr.W, scr.H, c)
		}
		// The inside is in the accent colour too, as the filter bar.
		for _, xy := range [][2]int{{size.x + 1, size.y + 1}, {size.x + 6, size.top}} {
			if c := scr.Cell(xy[0], xy[1]); c.Style != v.Styles.Accent {
				t.Errorf("%dx%d loading inside at %d,%d: %+v", scr.W, scr.H, xy[0], xy[1], c.Style)
			}
		}
		lines := strings.Split(scr.String(), "\n")
		text := func(row int) string {
			return strings.TrimSpace(string([]rune(lines[row])[size.x+6 : size.x+size.w-1]))
		}
		want := []string{"1,234 entries" + strings.Repeat(" ", size.w-9-len("1,234 entries3.0s")) + "3.0s"}
		if size.path != "" {
			want = append(want, size.path)
		}
		for i, line := range want {
			if got := text(size.top + i); got != line {
				t.Errorf("%dx%d loading line %d: %q, want %q", scr.W, scr.H, i, got, line)
			}
		}
		if c := scr.Cell(size.x+size.w-4, size.top); c.R != 's' || c.Style != v.Styles.Border.On(v.Styles.Accent) {
			t.Errorf("%dx%d elapsed time %+v", scr.W, scr.H, c)
		}
		if strings.Contains(scr.String(), "per second") {
			t.Errorf("%dx%d loading box shows a rate", scr.W, scr.H)
		}
		for _, row := range []int{size.top - 1, size.top + len(want)} {
			if row > size.y && row < size.y+size.h-1 && text(row) != "" {
				t.Errorf("%dx%d loading row %d: %q, want padding", scr.W, scr.H, row, text(row))
			}
		}
		if strings.Contains(scr.String(), "Loading") || strings.ContainsAny(scr.String(), "░█") {
			t.Errorf("%dx%d loading box has a title or a bar:\n%s", scr.W, scr.H, scr.String())
		}
		if status := lastLine(scr); strings.TrimSpace(status) != "" {
			t.Errorf("status while loading: %q, want blank", status)
		}
		if strings.Contains(scr.String(), "no differences") || strings.Contains(scr.String(), "scanning…") {
			t.Errorf("extra loading message:\n%s", scr.String())
		}
		if strings.Contains(scr.String(), "file │") {
			t.Errorf("tree rows drawn while scanning:\n%s", scr.String())
		}
		// Progress updates in place; finishing removes the box and restores the bar.
		scr.Clear()
		v.Draw(scr, m, TreeStatus{Scanning: true, Scanned: 2345, Elapsed: 4 * time.Second})
		if v.loaded <= 1234 || v.loaded > 2345 || !strings.Contains(scr.String(), "entries") || !strings.Contains(scr.String(), "4.0s") {
			t.Errorf("progress did not update:\n%s", scr.String())
		}
		scr.Clear()
		v.Draw(scr, m, TreeStatus{})
		if strings.Contains(scr.String(), "entries") {
			t.Errorf("loading box after scanning:\n%s", scr.String())
		}
		for _, want := range []string{"[1 items total]", "[1 identical]"} {
			if status := lastLine(scr); !strings.Contains(status, want) {
				t.Errorf("status after loading %q lacks %q", status, want)
			}
		}
	}
	scr := term.NewScreen(80, 5)
	v.Draw(scr, m, TreeStatus{})
	status := lastLine(scr)
	for _, want := range []string{"[1 items total]", "[1 identical]"} {
		if !strings.Contains(status, want) {
			t.Errorf("status %q lacks %q", status, want)
		}
	}
	// The roots are the boxes' titles, home shown as ~.
	if top := strings.Split(scr.String(), "\n")[0]; !strings.HasPrefix(top, "┌ ~/src/a ─") || !strings.Contains(top, "┌ ~/src/b ─") {
		t.Errorf("titles %q", top)
	}
}

func TestLoadingCountAndAnimation(t *testing.T) {
	m := tree.New(&scan.Node{})
	v := &TreeView{Styles: colors.Default()}
	scr := term.NewScreen(80, 10)
	draw := func(count int64, elapsed time.Duration) string {
		scr.Clear()
		v.Draw(scr, m, TreeStatus{Scanning: true, Scanned: count, Elapsed: elapsed})
		for _, line := range strings.Split(scr.String(), "\n") {
			if strings.Contains(line, "entries") {
				return line
			}
		}
		t.Fatal("loading count is missing")
		return ""
	}
	line := draw(1000, 0)
	if next := draw(1000, 80*time.Millisecond); next == line || v.loaded != 1000 {
		t.Error("the spinner must keep turning when the count is unchanged")
	}
	draw(10000, 100*time.Millisecond)
	if v.loaded <= 1000 || v.loaded >= 10000 {
		t.Errorf("count jumped from 1000 to %d instead of easing toward 10000", v.loaded)
	}
	previous := v.loaded
	for frame := 3; frame <= 40; frame++ {
		draw(10000, time.Duration(frame)*50*time.Millisecond)
		if v.loaded < previous || v.loaded > 10000 {
			t.Errorf("count %d after %d: must increase without overshooting", v.loaded, previous)
		}
		previous = v.loaded
	}
	if v.loaded != 10000 {
		t.Errorf("count did not catch up: %d", v.loaded)
	}
	if !strings.Contains(draw(10000, 2050*time.Millisecond), "10,000 entries") {
		t.Error("the count must group digits")
	}
	// A rescan starts from its own count, including when restarted mid-scan.
	draw(4, 0)
	if v.loaded != 4 {
		t.Errorf("count after restarting: %d, want 4", v.loaded)
	}
	v.Draw(scr, m, TreeStatus{})
	draw(25, time.Second)
	if v.loaded != 25 {
		t.Errorf("count after a completed scan: %d, want 25", v.loaded)
	}
}

func TestTypeLabels(t *testing.T) {
	root := t.TempDir()
	for _, f := range []struct {
		name string
		perm os.FileMode
	}{{"run", 0o755}, {"text", 0o644}} {
		if err := os.WriteFile(filepath.Join(root, f.name), nil, f.perm); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := scanned(t, root, t.TempDir())
	m := tree.New(s.Root)
	v := &TreeView{Styles: colors.Default(), Left: "/l", Right: "/r"}
	for _, tt := range []struct {
		name, label string
		style       term.Style
	}{
		{"run", "file", v.Styles.Exec},
		{"text", "file", v.Styles.Plain},
		{"fifo", "pipe", v.Styles.Pipe},
	} {
		label, style := v.typeLabel(&m.Root.Find(tt.name).L)
		if label != tt.label || style != tt.style {
			t.Errorf("%s: %q %+v, want %q %+v", tt.name, label, style, tt.label, tt.style)
		}
	}
	for _, tt := range []struct {
		kind  scan.Kind
		label string
	}{{scan.CharDev, "char"}, {scan.BlockDev, "bdev"}, {scan.Socket, "sock"}, {scan.Unknown, "?"}} {
		label, _ := v.typeLabel(&scan.Side{Kind: tt.kind})
		if label != tt.label || len(label) > typeLabelWidth {
			t.Errorf("%v: %q, want %q", tt.kind, label, tt.label)
		}
	}
}

func TestShortenMiddle(t *testing.T) {
	for _, tt := range []struct {
		in    string
		width int
		want  string
	}{
		{"short", 10, "short"},
		{"~/src/project/lib/util.go", 12, "~/src…til.go"},
		{"abcdef", 1, "…"},
		{"日本語日本語", 7, "日…語"},
	} {
		got := display.String(ShortenMiddle(display.Name(tt.in), tt.width))
		if got != tt.want {
			t.Errorf("ShortenMiddle(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 1023: "1023", 1024: "1.0K", 4300: "4.2K", 10 << 20: "10M", 5 << 30: "5.0G"} {
		if got := HumanSize(n); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestSizeValue(t *testing.T) {
	for size, want := range map[int64]string{
		1023: "1023", 1024: "1024 (1.0 KiB)", 10 << 20: "10485760 (10 MiB)", 5 << 30: "5368709120 (5.0 GiB)",
	} {
		if got := sizeValue(&stat.Stat{Mode: unix.S_IFREG, Size: size}, -1); got != want {
			t.Errorf("sizeValue(%d) = %q, want %q", size, got, want)
		}
	}
}

func TestHelpScrolls(t *testing.T) {
	scr := term.NewScreen(60, 10)
	h := NewHelp(TreeHelp)
	st := colors.Default()
	h.Draw(scr, st)
	got := scr.String()
	if !strings.Contains(got, "move down") || !strings.Contains(got, "▼") {
		t.Errorf("help:\n%s", got)
	}
	// Like the loading box: a sharp-cornered border and text in the accent
	// colour, two columns in.
	x, y, w, bh, _ := h.box(scr)
	for _, c := range []struct {
		x, y int
		r    rune
	}{{x, y, '┌'}, {x + w - 1, y, '┐'}, {x, y + bh - 1, '└'}, {x + w - 1, y + bh - 1, '┘'}, {x + w - 1, y + 3, '│'}} {
		if cell := scr.Cell(c.x, c.y); cell.R != c.r || cell.Style != st.Accent {
			t.Errorf("help border at %d,%d: %+v, want %c", c.x, c.y, cell, c.r)
		}
	}
	// The section's title between lines across the box, then columns: the
	// vim-like key, its alternative, and the text, the keys in bold and dim
	// rules between them, as in the tree's file stats, which the lines join.
	lines := strings.Split(got, "\n")
	rule := func(left, mid, right string) string {
		return left + strings.Repeat("─", 9) + mid + strings.Repeat("─", 13) + mid + strings.Repeat("─", w-26) + right
	}
	for row, want := range map[int]string{
		y:     "┌" + strings.Repeat("─", w-2) + "┐",
		y + 1: "│  Keyboard shortcuts",
		y + 2: rule("├", "┬", "┤"),
		y + 3: "│  j      │ Down        │ move down",
		y + 5: "│  Ctrl+f │ PgDn        │ move down a screen",
		y + 6: "│  Ctrl+b │ PgUp        │ move up a screen",
	} {
		if got := strings.TrimRight(string([]rune(lines[row])[x:x+w]), " │"); got != strings.TrimRight(want, " │") {
			t.Errorf("help row %d:\n%q, want\n%q", row-y, got, want)
		}
	}
	if c := scr.Cell(x, y+2); c.R != '├' || c.Style != st.Accent {
		t.Errorf("help line %+v", c)
	}
	if c := scr.Cell(x+3, y+1); c.R != 'K' || c.Style != st.Accent.With(term.Bold) {
		t.Errorf("help title %+v", c)
	}
	// The first row with keys is selected: a bar across the box.
	sel, bold := st.HelpSelect, st.Accent.With(term.Bold)
	for _, c := range []struct {
		x, y  int
		r     rune
		style term.Style
	}{
		{x + 1, y + 3, ' ', sel}, {x + 3, y + 3, 'j', sel.With(term.Bold)}, {x + 10, y + 3, '│', st.Border.On(sel)},
		{x + 12, y + 3, 'D', sel.With(term.Bold)}, {x + 26, y + 3, 'm', sel}, {x + w - 2, y + 3, ' ', sel},
		{x + 3, y + 4, 'k', bold}, {x + 10, y + 4, '│', st.Border.On(st.Accent)}, {x + 12, y + 4, 'U', bold},
		{x + 24, y + 4, '│', st.Border.On(st.Accent)}, {x + 26, y + 4, 'm', st.Accent},
		{x + 10, y + bh - 1, '┴', st.Accent}, {x + 24, y + bh - 1, '┴', st.Accent},
	} {
		if cell := scr.Cell(c.x, c.y); cell.R != c.r || cell.Style != c.style {
			t.Errorf("help cell at %d,%d: %+v, want %c", c.x-x, c.y-y, cell, c.r)
		}
	}
	h.MoveTo(scr, false)
	scr.Clear()
	h.Draw(scr, st)
	if got := scr.String(); !strings.Contains(got, "different entry types") || !strings.Contains(got, "▲") {
		t.Errorf("help moved to the end:\n%s", got)
	}
	if l := h.Lines[h.Selected]; l.Text != "different entry types" {
		t.Errorf("selected %q at the end", l.Text)
	}
	// Between the sections, a line, the title and a line.
	big := term.NewScreen(80, 40)
	NewHelp(TreeHelp).Draw(big, st)
	if got := big.String(); !regexp.MustCompile(`│  Ctrl\+c +│ +│ exit binate +│\n *├─────────┴─────────────┴─+┤\n *│  Comparison marks +│\n *├─────────┬─────────────┬─+┤\n *│  = `).MatchString(got) {
		t.Errorf("help's Marks section:\n%s", got)
	}
	h.MoveTo(scr, true)
	if h.Offset != 0 || h.Selected != 2 {
		t.Errorf("offset %d, selected %d after moving to the start", h.Offset, h.Selected)
	}
}

func TestHelpSelection(t *testing.T) {
	scr := term.NewScreen(60, 10)
	h := NewHelp(TreeHelp)
	at := func(text string) int {
		for i, l := range h.Lines {
			if l.Text == text {
				return i
			}
		}
		t.Fatalf("no help row %q", text)
		return 0
	}
	// Down and up a row at a time, skipping the lines and the title, and
	// scrolling to keep the selection shown.
	h.Selected = at("exit binate")
	h.Move(scr, 1)
	if h.Selected != at("no difference found") {
		t.Errorf("down from exit binate selects %q", h.Lines[h.Selected].Text)
	}
	_, _, _, _, inner := h.box(scr)
	if h.Selected < h.Offset || h.Selected >= h.Offset+inner {
		t.Errorf("selected %d not shown from offset %d", h.Selected, h.Offset)
	}
	h.Move(scr, -1)
	if h.Selected != at("exit binate") {
		t.Errorf("up selects %q", h.Lines[h.Selected].Text)
	}
	// The ends stop the selection.
	h.MoveTo(scr, false)
	h.Move(scr, 1)
	if h.Selected != len(h.Lines)-1 {
		t.Errorf("down past the end selects %d", h.Selected)
	}
	h.MoveTo(scr, true)
	h.Move(scr, -1)
	if h.Selected != 2 || h.Offset != 0 {
		t.Errorf("up past the start: selected %d, offset %d", h.Selected, h.Offset)
	}
	// Up to the first row of a section shows its title too.
	h.Selected, h.Offset = at("move up"), at("move up")
	h.Move(scr, -1)
	if h.Selected != 2 || h.Offset != 0 {
		t.Errorf("up to the first row: selected %d, offset %d", h.Selected, h.Offset)
	}
	// A page.
	h.Move(scr, 5)
	if h.Selected != 7 {
		t.Errorf("a page down selects %d", h.Selected)
	}
	// Under NO_COLOR, the selected row is not reversed like the box.
	h.Draw(scr, colors.NoColor())
	x, y, _, _, _ := h.box(scr)
	if c := scr.Cell(x+3, y+1+h.Selected-h.Offset); c.Style.Attr&term.Reverse != 0 {
		t.Errorf("selected row under NO_COLOR %+v", c)
	}
}

func TestHelpKeys(t *testing.T) {
	join := func(lines []HelpLine) string {
		var rows []string
		for _, l := range lines {
			rows = append(rows, strings.Join(append(append([]string{}, l.Keys...), l.Text), " "))
		}
		return strings.Join(rows, "\n")
	}
	tree, diff := join(TreeHelp), join(DiffHelp())
	// Ctrl+c exits from both; Esc goes back to the tree from the diff view,
	// before Ctrl+c, and ends a search or a filter in the tree.
	for _, text := range []string{tree, diff} {
		if !strings.Contains(text, "Ctrl+c exit binate") {
			t.Errorf("help lacks Ctrl+c:\n%s", text)
		}
	}
	if !strings.Contains(diff, "Esc return to the tree\nCtrl+c exit binate") {
		t.Errorf("The diff view's help from a tree lacks Esc:\n%s", diff)
	}
	if strings.Count(tree, "Esc") != 1 || !strings.Contains(tree, "Esc close an action's result, end the search, or clear the filter") {
		t.Error("Esc listed where it does nothing, or not where it does")
	}
	if strings.Contains(tree+diff, "diff view") || strings.Contains(tree+diff, "tree view") {
		t.Error("help names a screen")
	}
	text := tree + "\n" + diff
	for _, gone := range []string{"Ctrl+z", "Space", "Backspace", "close this help", "filter bar", "Alt+"} {
		if strings.Contains(text, gone) {
			t.Errorf("help lists %q", gone)
		}
	}
	for _, want := range []string{"Ctrl+f PgDn", "Ctrl+b PgUp", "gg Home", "G End", "n Shift+Down", "N Shift+Up", "/ search", "N previous match", "Esc close an action's result, end the search", "a run an action"} {
		if !strings.Contains(text, want) {
			t.Errorf("help lacks %q", want)
		}
	}
}

func TestQuitPrompt(t *testing.T) {
	st := colors.Default()
	for _, exit := range []bool{true, false} {
		scr := term.NewScreen(80, 24)
		DrawQuit(scr, st, exit)
		lines := strings.Split(scr.String(), "\n")
		// As dialog(1): the question padded by blank rows, a rule, and the
		// buttons, centered.
		want := []string{
			"┌──────────────────────────────────────┐",
			"│                                      │",
			"│    Are you sure you want to exit?    │",
			"│                                      │",
			"├──────────────────────────────────────┤",
			"│         < Exit >   <Cancel>          │",
			"└──────────────────────────────────────┘",
		}
		for i, line := range want {
			if got := strings.TrimSpace(lines[8+i]); got != line {
				t.Errorf("quit prompt row %d: %q, want %q", i, got, line)
			}
		}
		if c := scr.Cell(20, 8); c.Style != st.Quit {
			t.Errorf("quit prompt border %+v", c)
		}
		// The focused button stands out, and Exit's x in both states.
		exitStyle, cancelStyle := st.QuitFocus, st.Quit
		if !exit {
			exitStyle, cancelStyle = st.Quit, st.QuitFocus
		}
		if c := scr.Cell(30, 13); c.R != '<' || c.Style != exitStyle {
			t.Errorf("exit %v: exit button %+v", exit, c)
		}
		if c := scr.Cell(33, 13); c.R != 'x' || c.Style != exitStyle.On(st.QuitKey) {
			t.Errorf("exit %v: exit key %+v", exit, c)
		}
		if c := scr.Cell(41, 13); c.R != '<' || c.Style != cancelStyle {
			t.Errorf("exit %v: cancel button %+v", exit, c)
		}
	}
	// Five rows leave out the padding.
	scr := term.NewScreen(40, 5)
	DrawQuit(scr, st, true)
	lines := strings.Split(scr.String(), "\n")
	if !strings.Contains(lines[1], QuitQuestion) || !strings.HasPrefix(lines[2], "├") || !strings.Contains(lines[3], ExitButton+"   "+CancelButton) {
		t.Errorf("compact quit prompt:\n%s", scr.String())
	}
}

func TestUnlistableRoots(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	left, right := t.TempDir(), t.TempDir()
	for _, d := range []string{left, right} {
		if err := os.Chmod(d, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(d, 0o755) })
	}
	s := scanned(t, left, right)
	v := &TreeView{Styles: colors.Default(), Left: "/l", Right: "/r"}
	scr := term.NewScreen(120, 5)
	v.Draw(scr, tree.New(s.Root), TreeStatus{})
	first := strings.Split(panes(scr), "\n")[0]
	want := "failed listing this directory (permission denied)"
	if first != want+strings.Repeat(" ", 56-len(want)+3)+want {
		t.Errorf("first line %q, want %q in each pane", first, want)
	}
}

func TestBreakLines(t *testing.T) {
	for _, tt := range []struct {
		in    string
		width int
		want  []string
	}{
		{"short", 10, []string{"short"}},
		{"failed listing this directory (permission denied)", 20, []string{"failed listing this", "directory", "(permission denied)"}},
		{"abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"日本語", 3, []string{"日", "本", "語"}},
		{"a\x1b b", 3, []string{`a\x`, `1b`, "b"}},
	} {
		var got []string
		for _, line := range breakLines(display.Name(tt.in), tt.width) {
			got = append(got, display.String(line))
		}
		if strings.Join(got, "|") != strings.Join(tt.want, "|") {
			t.Errorf("breakLines(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
	}
}

func TestBoxTitle(t *testing.T) {
	scr := term.NewScreen(24, 3)
	drawBox(scr, 0, 24, 3, nil, display.Name("/a/very/long/path/to/compare"), "3/9", colors.Default())
	want := "┌ /a/very/l…o/compare ─┐\n└───────────────── 3/9 ┘\n"
	lines := strings.Split(scr.String(), "\n")
	if got := lines[0] + "\n" + lines[2] + "\n"; got != want {
		t.Errorf("box:\n%s\nwant:\n%s", got, want)
	}

	// A lead, dimmed, before the title, the short one where the long one
	// leaves too little room for the whole title, and kept over the title
	// where there is little room.
	st := colors.Default()
	for _, tt := range []struct {
		w    int
		want string
	}{
		{44, "┌ (viewing) /a/very/long/path/to/compare ──┐"},
		{43, "┌ (viewing) /a/very/long/path/to/compare ─┐"},
		{42, "┌ (v) /a/very/long/path/to/compare ──────┐"},
		{37, "┌ (v) /a/very/long/path/to/compare ─┐"},
		{36, "┌ (v) /a/very/long/…th/to/compare ─┐"},
		{12, "┌ (v) /…e ─┐"},
		{9, "┌ (v) ──┐"},
		{7, "┌ (v ─┐"},
	} {
		scr := term.NewScreen(tt.w, 3)
		drawBox(scr, 0, tt.w, 3, viewingLeads, display.Name("/a/very/long/path/to/compare"), "", st)
		if got := strings.Split(scr.String(), "\n")[0]; got != tt.want {
			t.Errorf("width %d: %q, want %q", tt.w, got, tt.want)
		}
		if c := scr.Cell(2, 0); c.R != '(' || c.Style != st.Plain.With(term.Dim) {
			t.Errorf("width %d: lead %+v", tt.w, c)
		}
	}
}

func TestEntriesAndDigits(t *testing.T) {
	for n, want := range map[int64]string{0: "0 entries", 1: "1 entry", 999: "999 entries", 1000: "1,000 entries", 1234567: "1,234,567 entries"} {
		if got := entries(n); got != want {
			t.Errorf("entries(%d) = %q, want %q", n, got, want)
		}
	}
	if got := groupDigits(-12345); got != "-12,345" {
		t.Errorf("groupDigits(-12345) = %q", got)
	}
}

func TestEditPopup(t *testing.T) {
	st := colors.Default()
	scr := term.NewScreen(80, 24)
	e := NewEditPopup("/home/u", "/home/u/a/f.txt", "/b/new\nline.txt")
	e.Draw(scr, st)
	got := scr.String()
	// Like Help: the title above a line, then the keys and the paths, set
	// apart by spaces rather than rules, home as ~ and escapes shown.
	w := len([]rune("  l →  /b/new\\nline.txt")) + 2
	for _, want := range []string{
		"┌" + strings.Repeat("─", w) + "┐",
		"│  Select side to edit",
		"├" + strings.Repeat("─", w) + "┤",
		"│  h ←  ~/a/f.txt",
		"│  l →  /b/new\\nline.txt",
		"└" + strings.Repeat("─", w) + "┘",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("popup lacks %q:\n%s", want, got)
		}
	}
	x, y, _, _, _ := e.box(scr)
	if c := scr.Cell(x, y); c.R != '┌' || c.Style != st.Accent {
		t.Errorf("popup corner %+v", c)
	}
	// The left side is selected: a bar over its path, from a column
	// before it to the border, but not over its keys.
	for _, c := range []struct {
		x, y  int
		r     rune
		style term.Style
	}{
		{x + 2, y + 3, ' ', st.Accent}, {x + 3, y + 3, 'h', st.Accent.With(term.Bold)}, {x + 5, y + 3, '←', st.Accent.With(term.Bold)},
		{x + 6, y + 3, ' ', st.Accent}, {x + 7, y + 3, ' ', st.HelpSelect}, {x + 8, y + 3, '~', st.HelpSelect},
		{x + w, y + 3, ' ', st.HelpSelect}, {x + w + 1, y + 3, '│', st.Accent},
		{x + 3, y + 4, 'l', st.Accent.With(term.Bold)}, {x + 7, y + 4, ' ', st.Accent}, {x + 8, y + 4, '/', st.Accent},
	} {
		if cell := scr.Cell(c.x, c.y); cell.R != c.r || cell.Style != c.style {
			t.Errorf("popup cell at %d,%d: %+v, want %c", c.x-x, c.y-y, cell, c.r)
		}
	}
	// The escape stands out on either background.
	col := strings.Index(strings.Split(got, "\n")[y+4], "\\n")
	col = len([]rune(strings.Split(got, "\n")[y+4][:col]))
	if c := scr.Cell(col, y+4); c.Style != rowEscape(st.Escape, st.Accent) {
		t.Errorf("escape %+v", c)
	}
	// A long path is shortened in the middle to fit.
	small := term.NewScreen(40, 10)
	NewEditPopup("", "/a/"+strings.Repeat("x", 60)+"/end.txt", "/b").Draw(small, st)
	if s := small.String(); !strings.Contains(s, "…") || !strings.Contains(s, "end.txt") {
		t.Errorf("long path:\n%s", s)
	}
	if e.Side() != 0 {
		t.Errorf("side %d first", e.Side())
	}
}

func TestErrorPopup(t *testing.T) {
	st := colors.Default()
	scr := term.NewScreen(80, 24)
	DrawError(scr, st, "Unable to edit", "Neither side is a regular file. The left is a directory and the right a symlink.")
	lines := strings.Split(scr.String(), "\n")
	// The title above a line, then the message wrapped at spaces, in red,
	// narrower than 80 columns on a screen too narrow for that.
	want := []string{
		"┌───────────────────────────────────────────────────────────────────────────┐",
		"│  Unable to edit                                                           │",
		"├───────────────────────────────────────────────────────────────────────────┤",
		"│  Neither side is a regular file. The left is a directory and the right a  │",
		"│  symlink.                                                                 │",
		"└───────────────────────────────────────────────────────────────────────────┘",
	}
	for i, line := range want {
		if got := strings.TrimSpace(lines[9+i]); got != line {
			t.Errorf("row %d: %q, want %q", i, got, line)
		}
	}
	x := strings.Index(lines[9], "┌")
	if c := scr.Cell(x, 9); c.Style != st.Quit {
		t.Errorf("border %+v", c)
	}
	if c := scr.Cell(x+3, 10); c.R != 'U' || c.Style != st.Quit.With(term.Bold) {
		t.Errorf("title %+v", c)
	}
	if c := scr.Cell(x+3, 12); c.R != 'N' || c.Style != st.Quit {
		t.Errorf("text %+v", c)
	}
}

// More than one text is a list, each wrapped with its lines set in under
// its text.
func TestErrorPopupList(t *testing.T) {
	scr := term.NewScreen(80, 24)
	DrawError(scr, colors.Default(), "Unable to open",
		"Failed reading the left side (permission denied).",
		"Only UTF-8 encoded text files can be opened (the right side is not), which is a longer text than fits.")
	var got []string
	for _, line := range strings.Split(scr.String(), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "│ ") {
			got = append(got, strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(line, "│  "), "│"), " "))
		}
	}
	want := []string{
		"Unable to open",
		"- Failed reading the left side (permission denied).",
		"- Only UTF-8 encoded text files can be opened (the right side is not),",
		"  which is a longer text than fits.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("popup:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A first text ending in a colon leads the list.
func TestErrorPopupListLead(t *testing.T) {
	scr := term.NewScreen(80, 24)
	DrawError(scr, colors.Default(), "Unable to open", "Only regular files can be opened:", "The left side is a symlink.", "The right side is a socket.")
	var got []string
	for _, line := range strings.Split(scr.String(), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "│ ") {
			got = append(got, strings.TrimRight(strings.TrimSuffix(strings.TrimPrefix(line, "│  "), "│"), " "))
		}
	}
	want := []string{
		"Unable to open",
		"Only regular files can be opened:",
		"- The left side is a symlink.",
		"- The right side is a socket.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("popup:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// On a screen wide enough, the error popup's text is wrapped at 80
// columns.
func TestErrorPopupWraps(t *testing.T) {
	scr := term.NewScreen(160, 24)
	text := strings.Repeat("word ", 40)
	DrawError(scr, colors.Default(), "Unable to open", text)
	widest := 0
	for _, line := range strings.Split(scr.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "│  word") {
			widest = max(widest, len(strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "│"))))
		}
	}
	// 16 words of 4 letters and the spaces between them.
	if widest != 79 {
		t.Errorf("text wrapped at %d columns:\n%s", widest, scr.String())
	}
}

func TestErrorTextBreaks(t *testing.T) {
	for _, tt := range []struct {
		in    string
		width int
		want  []string
	}{
		{"one two three", 7, []string{"one two", "three"}},
		{"abcdefghij x", 4, []string{"abcd", "efgh", "ij x"}},
	} {
		var got []string
		for _, l := range breakLines(display.Name(tt.in), tt.width) {
			got = append(got, display.String(l))
		}
		if strings.Join(got, "|") != strings.Join(tt.want, "|") {
			t.Errorf("breakLines(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
	}
}

// An error may quote any text: it is escaped, not a panic.
func TestDrawErrorEscapes(t *testing.T) {
	scr := term.NewScreen(60, 12)
	DrawError(scr, colors.Default(), "Failed", "bad name \"e\u0301\x1b\".")
	if out := scr.String(); !strings.Contains(out, `\u0301\x1b`) {
		t.Errorf("not escaped:\n%s", out)
	}
}

func TestDiffMarkStyles(t *testing.T) {
	v := &TreeView{Styles: colors.Default()}
	st := v.Styles
	for _, tt := range []struct {
		diffs scan.Attrs
		mark  rune
		style term.Style
	}{
		{scan.Content, '|', st.Changed},
		{scan.Owner, '~', st.Attrs},
		{scan.Mtime, '#', st.Mtime},
		{scan.Content | scan.Mtime, '*', st.Changed},
		{scan.Mode | scan.Mtime, '*', st.Attrs},
	} {
		n := &scan.Node{L: scan.Side{Kind: scan.Dir}, R: scan.Side{Kind: scan.Dir}, State: scan.Differ, Own: tt.diffs}
		if mark, style := n.Mark(), v.markStyle(n); mark != tt.mark || style != tt.style {
			t.Errorf("%v: %c %+v, want %c %+v", tt.diffs, mark, style, tt.mark, tt.style)
		}
	}
	// A file's is in the bright palette, as for the other marks.
	n := &scan.Node{L: scan.Side{Kind: scan.File}, R: scan.Side{Kind: scan.File}, State: scan.Differ, Own: scan.Mode}
	if style := v.rowStyle(n); style.SGR != "96" {
		t.Errorf("file with another mode: %+v", style)
	}
}

func TestComparePopup(t *testing.T) {
	c := NewComparePopup(scan.DefaultAttrs, false, 0)
	scr := term.NewScreen(80, 24)
	c.Draw(scr, colors.Default())
	text := scr.String()
	for _, want := range []string{
		"Compare",
		"[x]  left      entries on the left only (<)",
		"[x]  right     entries on the right only (>)",
		"[x]  type      entries of different types (!)",
		"[x]  content   files' size and contents",
		"[x]  target    symlinks' targets",
		"[x]  owner     user and group",
		"[ ]  mtime     modification time",
		"Space or a letter toggles, * toggles all, Esc closes",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("popup without %q:\n%s", want, text)
		}
	}
	if c.Attr() != scan.Left {
		t.Errorf("selected %v", c.Attr())
	}
	// Attributes the sides don't record are shown as such.
	git := NewComparePopup(scan.DefaultAttrs&^scan.Owner, false, scan.Owner|scan.Mtime)
	gitScr := term.NewScreen(80, 24)
	git.Draw(gitScr, colors.Default())
	for _, want := range []string{
		"[x]  mode      permission bits",
		"[-]  owner     not recorded in git revisions",
		"[-]  mtime     not recorded in git revisions",
	} {
		if !strings.Contains(gitScr.String(), want) {
			t.Errorf("popup without %q:\n%s", want, gitScr.String())
		}
	}
	// Each row's letter is highlighted, as the quit prompt's x: l in
	// left, y in type, a in target, t in mtime.
	st := colors.Default()
	for _, tt := range []struct {
		row string
		hot rune
		at  int
		on  term.Style
	}{
		{"left", 'l', 0, st.HelpSelect},
		{"target", 'a', 1, st.Accent},
		{"type", 'y', 1, st.Accent},
		{"mtime", 't', 1, st.Accent},
	} {
		lines := strings.Split(scr.String(), "\n")
		for y, line := range lines {
			x := strings.Index(line, "]  "+tt.row+" ")
			if x < 0 {
				continue
			}
			col := len([]rune(line[:x])) + 3 + tt.at
			if cell := scr.Cell(col, y); cell.R != tt.hot || cell.Style != tt.on.On(st.QuitKey) {
				t.Errorf("%s: cell %+v, want %c in %+v", tt.row, cell, tt.hot, tt.on.On(st.QuitKey))
			}
			if cell := scr.Cell(col+1, y); cell.Style != tt.on {
				t.Errorf("%s: the next letter is highlighted too: %+v", tt.row, cell)
			}
		}
	}
	if !c.SelectKey('o') || c.Attr() != scan.Owner || c.SelectKey('z') {
		t.Errorf("SelectKey: selected %v", c.Attr())
	}

	c.Selected = firstCompareRow
	c.Move(scr, 8)
	c.Set(scan.Mode|scan.Content, true)
	if c.Attr() != scan.Mtime || c.Lines[c.Selected].Keys[0] != "[ ]" || c.Lines[5].Keys[0] != "[q]" ||
		!strings.Contains(c.Lines[5].Text, "quick") || c.Lines[2].Keys[0] != "[ ]" {
		t.Errorf("after Set: selected %v, lines %+v", c.Attr(), c.Lines)
	}
}

// A submodule shows as "sub", with its commit abbreviated after its name.
func TestTreeSubmodule(t *testing.T) {
	root := &scan.Node{L: scan.Side{Kind: scan.Dir}, R: scan.Side{Kind: scan.Dir}, State: scan.Differ}
	sub := &scan.Node{
		Name: "lib", Parent: root, State: scan.Differ, Own: scan.Content, Data: scan.Differ,
		L: scan.Side{Kind: scan.Submodule, Target: strings.Repeat("a", 40)},
		R: scan.Side{Kind: scan.Submodule, Target: strings.Repeat("b", 40)},
	}
	root.Children = []*scan.Node{sub}
	m := tree.New(root)
	v := &TreeView{Styles: colors.Default(), Left: "a", Right: "b"}
	scr := term.NewScreen(70, 8)
	v.Draw(scr, m, TreeStatus{})
	want := "  sub │   lib @ aaaaaaaaaaaa    |   sub │   lib @ bbbbbbbbbbbb\n"
	if got := strings.SplitAfter(panes(scr), "\n")[0]; got != want {
		t.Errorf("panes:\n%q\nwant:\n%q", got, want)
	}
	sub.R.Target = ""
	scr = term.NewScreen(70, 8)
	v.Draw(scr, m, TreeStatus{})
	if got := strings.SplitAfter(panes(scr), "\n")[0]; !strings.Contains(got, "lib @ (no commits)") {
		t.Errorf("panes:\n%q\nwant lib @ (no commits)", got)
	}
}

func TestTreeStatuses(t *testing.T) {
	root := &scan.Node{L: scan.Side{Kind: scan.Dir}, R: scan.Side{Kind: scan.Dir}, State: scan.Differ}
	add := func(name, status string, state scan.State, l, r scan.Kind) *scan.Node {
		n := &scan.Node{Name: name, Parent: root, State: state, L: scan.Side{Kind: l}, R: scan.Side{Kind: r}}
		if status != "" {
			n.Status = source.Status{Staged: status[0], Unstaged: status[1]}
		}
		root.Children = append(root.Children, n)
		return n
	}
	add("added", "A ", scan.RightOnly, scan.Missing, scan.File)
	add("changed", " M", scan.Differ, scan.File, scan.File)
	add("new", "??", scan.RightOnly, scan.Missing, scan.File)
	add("same", "", scan.Same, scan.File, scan.File)
	add("staged", "D ", scan.LeftOnly, scan.File, scan.Missing)
	add("unstaged", " D", scan.LeftOnly, scan.File, scan.Missing)
	scan.Aggregate(root)
	m := tree.New(root)
	m.ShowSame = true
	v := &TreeView{Styles: colors.Default(), Left: "HEAD", Right: "/r", Statuses: true, StatusRight: true}
	scr := term.NewScreen(80, 9)
	v.Draw(scr, m, TreeStatus{})
	want := ` added, staged                       >  A  file │   added
 file │   changed                    |   M file │   changed
 untracked                           >  ?? file │   new
 file │   same                       =     file │   same
 file │   staged                     <  D  deleted, staged
 file │   unstaged                   <   D deleted, not staged
`
	if got := panes(scr); got != want {
		t.Errorf("panes:\n%s\nwant:\n%s", got, want)
	}
	wide := term.NewScreen(120, 9)
	v.Draw(wide, m, TreeStatus{})
	if got := lastLine(wide); !strings.Contains(got, "[2 staged] [2 unstaged] [1 untracked]") {
		t.Errorf("status bar: %q", got)
	}
	// The letters in git's colours: staged in green, the rest in red.
	p := Layout(scr.W)
	for y, want := range map[int][2]string{1: {"32", ""}, 2: {"", "31"}, 3: {"31", "31"}} {
		for i, w := range want {
			// The first row is selected, over a background.
			if c := scr.Cell(p.RightX+1+i, y); w != "" && strings.Split(c.Style.SGR, ";")[0] != w {
				t.Errorf("row %d letter %d style %q, want %q", y, i, c.Style.SGR, w)
			}
		}
	}

	// Below an untracked directory, the entries are said to be at the
	// directory alone.
	untracked := source.Status{Staged: '?', Unstaged: '?'}
	other := &scan.Node{L: scan.Side{Kind: scan.Dir}, R: scan.Side{Kind: scan.Dir}, State: scan.Differ}
	dir := &scan.Node{Name: "untr", Parent: other, State: scan.RightOnly, R: scan.Side{Kind: scan.Dir}, Status: untracked}
	dir.Children = []*scan.Node{{Name: "f", Parent: dir, State: scan.RightOnly, R: scan.Side{Kind: scan.File}, Status: untracked}}
	other.Children = []*scan.Node{dir}
	scan.Aggregate(other)
	um := tree.New(other)
	um.ExpandAll()
	scr = term.NewScreen(80, 9)
	v.Draw(scr, um, TreeStatus{})
	want = ` untracked                           >  ??  dir │ ▾ untr/
                                     >  ?? file │     f
`
	if got := strings.TrimRight(panes(scr), "\n") + "\n"; got != want {
		t.Errorf("untracked directory:\n%s\nwant:\n%s", got, want)
	}

	// Gone from both sides: shown dimmed on the work tree's, its status
	// in words on the other, with no mark.
	goneRoot := &scan.Node{L: scan.Side{Kind: scan.Dir}, R: scan.Side{Kind: scan.Dir}, State: scan.Same}
	goneDir := &scan.Node{Name: "dir", Parent: goneRoot, State: scan.Same, Gone: scan.Dir}
	goneDir.Children = []*scan.Node{{Name: "f", Parent: goneDir, State: scan.Same, Gone: scan.File, Status: source.Status{Staged: 'D', Unstaged: ' '}}}
	goneRoot.Children = []*scan.Node{goneDir}
	scan.Aggregate(goneRoot)
	gm := tree.New(goneRoot)
	gm.ExpandAll()
	scr = term.NewScreen(80, 9)
	v.Draw(scr, gm, TreeStatus{})
	want = `                                            dir │ ▾ dir/
 deleted, staged                        D  file │     f
`
	if got := strings.TrimRight(panes(scr), "\n") + "\n"; got != want {
		t.Errorf("gone entries:\n%s\nwant:\n%s", got, want)
	}
	for x := Layout(80).RightX + statusWidth + typeColumn; x < 80; x++ {
		if c := scr.Cell(x, 2); c.R == 'f' {
			if c.Style.SGR != colors.Default().Border.SGR {
				t.Errorf("gone name in %q, want dimmed", c.Style.SGR)
			}
			break
		}
	}

	// On the left, as for the work tree compared with a revision.
	v.StatusRight = false
	scr = term.NewScreen(80, 9)
	v.Draw(scr, m, TreeStatus{})
	if got := strings.SplitAfter(panes(scr), "\n")[1]; got != "  M file │   changed                 |  file │   changed\n" {
		t.Errorf("left statuses: %q", got)
	}
}

func TestStatusText(t *testing.T) {
	for _, tc := range []struct {
		status  string
		present bool
		want    string
	}{
		{"", true, ""},
		{"  ", true, ""},
		{"??", true, "untracked"},
		{"UU", true, "unmerged, both modified"},
		{"DU", false, "unmerged, deleted by us"},
		{"A ", true, "added, staged"},
		{" M", true, "modified, not staged"},
		{"MM", true, "modified, partly staged"},
		{"AD", false, "added, staged, then deleted, not staged"},
		{"D ", false, "deleted, staged"},
		{"D ", true, "deleted, staged, kept untracked"},
		{" T", true, "type changed, not staged"},
		{" A", true, "added, not staged"},
	} {
		var st source.Status
		if tc.status != "" {
			st = source.Status{Staged: tc.status[0], Unstaged: tc.status[1]}
		}
		if got := StatusText(st, tc.present); got != tc.want {
			t.Errorf("StatusText(%q, %v) = %q, want %q", tc.status, tc.present, got, tc.want)
		}
	}
}

func TestNodeStatusText(t *testing.T) {
	deleted := source.Status{Staged: 'D', Unstaged: ' '}
	for kind, want := range map[scan.Kind]string{
		scan.File:    "deleted, staged, kept untracked",
		scan.Missing: "deleted, staged",
		// A directory staged in place of the file doesn't keep it.
		scan.Dir: "deleted, staged",
	} {
		n := &scan.Node{L: scan.Side{Kind: scan.File}, R: scan.Side{Kind: kind}, Status: deleted}
		if got := NodeStatusText(n, true); got != want {
			t.Errorf("%v on disk: %q, want %q", kind, got, want)
		}
	}
}
