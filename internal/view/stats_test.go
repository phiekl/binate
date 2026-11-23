// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"errors"
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

func TestPaneRows(t *testing.T) {
	for _, tt := range []struct{ h, rows, panel int }{
		{5, 2, 0},
		{9, 6, 0},
		// A panel once the list keeps 5 rows, growing to 11 rows.
		{10, 5, 1},
		{19, 5, 10},
		{20, 5, 11},
		{30, 15, 11},
	} {
		if rows, panel := paneRows(tt.h); rows != tt.rows || panel != tt.panel {
			t.Errorf("paneRows(%d) = %d, %d, want %d, %d", tt.h, rows, panel, tt.rows, tt.panel)
		}
	}
}

// panelRowsText returns the rows of the panels below the dividers,
// without the borders, as panes does.
func panelRowsText(scr *term.Screen) []string {
	var out []string
	in := false
	for _, line := range strings.Split(panes2(scr), "\n") {
		if in {
			out = append(out, line)
		}
		in = in || strings.HasPrefix(line, "├")
	}
	return out
}

// panes2 is panes, from the top, through the divider rows.
func panes2(scr *term.Screen) string {
	p := Layout(scr.W)
	border := map[int]bool{
		p.LeftBox: true, p.LeftBox + p.LeftBoxW - 1: true,
		p.RightBox: true, p.RightBox + p.RightBoxW - 1: true,
	}
	var b strings.Builder
	for y := 1; y < scr.H-1; y++ {
		if scr.Cell(0, y).R == '├' {
			b.WriteString("├\n")
			continue
		}
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

// pair joins a left and right pane's row as panes does, on a screen 80
// columns wide.
func pair(l, r string) string {
	if r == "" {
		return l
	}
	return l + strings.Repeat(" ", 39-len([]rune(l))) + r
}

// marked is pair with mark between the panes.
func marked(l string, mark rune, r string) string {
	line := []rune(pair(l, r))
	line[37] = mark
	return string(line)
}

func local(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func file(t *testing.T, size int64, perm uint32, mtime string) *stat.Stat {
	tm := local(t, mtime)
	return &stat.Stat{
		Mode: unix.S_IFREG | perm, Size: size, Uid: 1000, Gid: 50, Nlink: 1, Ino: 7,
		Mtime: tm, Ctime: tm, Atime: tm,
	}
}

// drawPanelsOn draws the panels in boxes on a screen of height h, and
// returns it, highlighting differences in every attribute.
func drawPanelsOn(h, rows int, sides [2]StatSide) *term.Screen {
	return drawPanelsComparing(h, rows, sides, scan.DefaultAttrs|scan.Mtime)
}

func drawPanelsComparing(h, rows int, sides [2]StatSide, compare scan.Attrs) *term.Screen {
	scr := term.NewScreen(80, h)
	p := Layout(80)
	drawBoxes(scr, p, h, nil, [2][]display.Glyph{}, [2]string{}, colors.Default())
	drawPanels(scr, p, 1, rows, sides, compare, "", term.Style{}, colors.Default())
	return scr
}

func TestPanels(t *testing.T) {
	st := colors.Default()
	l := StatSide{Stat: file(t, 10, 0o644, "2026-11-02 14:10:00"), User: "al", Group: "st", Entries: -1}
	r := StatSide{Stat: file(t, 4300, 0o4755, "2026-11-03 09:00:00"), Entries: -1}
	r.Stat.Ino, r.Stat.Gid = 8, 51
	scr := drawPanelsOn(13, 10, [2]StatSide{l, r})
	want := []string{
		pair(" type │ file", " type │ file"),
		marked(" size │ 10", '|', " size │ 4300 (4.2 KiB)"),
		marked(" mode │ 0644", '~', " mode │ 4755"),
		marked("owner │ al:st (1000:50)", '~', "owner │ 1000:51"),
		pair("links │ 1", "links │ 1"),
		pair("inode │ 7", "inode │ 8"),
		marked("mtime │ 2026-11-02 14:10:00", '#', "mtime │ 2026-11-03 09:00:00 (newer)"),
		pair("ctime │ 2026-11-02 14:10:00", "ctime │ 2026-11-03 09:00:00"),
		pair("atime │ 2026-11-02 14:10:00", "atime │ 2026-11-03 09:00:00"),
		pair("btime │ —", "btime │ —"),
		"",
	}
	if got := panelRowsText(scr); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("panels:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if line := strings.Split(scr.String(), "\n")[1]; !strings.HasPrefix(line, "├──") || !strings.HasSuffix(line, "──┤") {
		t.Errorf("divider %q", line)
	}
	// Differences that matter are highlighted on both sides, and marked
	// between them, in their mark's colour, bright for a file; the inode
	// and the other times differ between any two trees, and are not.
	p := Layout(80)
	for y, wantStyle := range map[int]term.Style{
		2: st.Plain, 3: brighten(st.Changed), 4: brighten(st.Attrs), 5: brighten(st.Attrs),
		7: st.Plain, 8: st.Mtime, 9: st.Plain,
	} {
		for _, x := range []int{9, p.Mark, 50} {
			if x == p.Mark && wantStyle == st.Plain {
				continue
			}
			if c := scr.Cell(x, y); c.Style != wantStyle {
				t.Errorf("row %d column %d %q %+v, want %+v", y, x, c.R, c.Style, wantStyle)
			}
		}
	}

	// Only the attributes compared are highlighted; the type always is.
	scr = drawPanelsComparing(13, 10, [2]StatSide{l, r}, scan.Owner)
	for y, wantStyle := range map[int]term.Style{3: st.Plain, 4: st.Plain, 5: brighten(st.Attrs), 8: st.Plain} {
		if c := scr.Cell(9, y); c.Style != wantStyle {
			t.Errorf("comparing the owner only: row %d %q %+v, want %+v", y, c.R, c.Style, wantStyle)
		}
	}

	// Times within a second of each other, which the tree tells apart to
	// the nanosecond, show as much of the fraction as differs.
	r.Stat.Mtime = l.Stat.Mtime.Add(500 * time.Millisecond)
	if got, want := panelRowsText(drawPanelsOn(13, 10, [2]StatSide{l, r}))[6],
		marked("mtime │ 2026-11-02 14:10:00.0", '#', "mtime │ 2026-11-02 14:10:00.5 (newer)"); got != want {
		t.Errorf("mtime %q, want %q", got, want)
	}
	r.Stat.Mtime = l.Stat.Mtime
	if got := panelRowsText(drawPanelsOn(13, 10, [2]StatSide{l, r}))[6]; strings.Contains(got, "newer") || strings.Contains(got, "#") {
		t.Errorf("equal mtime %q", got)
	}
	// One recorded only to the second, as in an archive, is compared to
	// it.
	l.Stat.MtimePrecision, r.Stat.Mtime = time.Second, l.Stat.Mtime.Add(500*time.Millisecond)
	if got := panelRowsText(drawPanelsOn(13, 10, [2]StatSide{l, r}))[6]; strings.Contains(got, "newer") || strings.Contains(got, "#") || strings.Contains(got, ".5") {
		t.Errorf("mtime within the second of one to it %q", got)
	}
	r.Stat.Mtime = l.Stat.Mtime.Add(time.Second)
	if got, want := panelRowsText(drawPanelsOn(13, 10, [2]StatSide{l, r}))[6],
		marked("mtime │ 2026-11-02 14:10:00", '#', "mtime │ 2026-11-02 14:10:01 (newer)"); got != want {
		t.Errorf("mtime a second after one to it %q, want %q", got, want)
	}
	// One recorded to two seconds, as a zip archive's DOS time, rounded
	// up or down, is the same a second apart, but not two.
	l.Stat.MtimePrecision, r.Stat.Mtime = 2*time.Second, l.Stat.Mtime.Add(time.Second)
	if got := panelRowsText(drawPanelsOn(13, 10, [2]StatSide{l, r}))[6]; strings.Contains(got, "newer") || strings.Contains(got, "#") {
		t.Errorf("mtime a second from one to two %q", got)
	}
	r.Stat.Mtime = l.Stat.Mtime.Add(2 * time.Second)
	if got := panelRowsText(drawPanelsOn(13, 10, [2]StatSide{l, r}))[6]; !strings.Contains(got, "newer") || !strings.Contains(got, "#") {
		t.Errorf("mtime two seconds from one to two %q", got)
	}
	// Both recorded so, as in two archives, they differ a second apart,
	// as extracting them makes them, and below the second, the fraction
	// shown.
	r.Stat.MtimePrecision, r.Stat.Mtime = time.Second, l.Stat.Mtime.Add(time.Second)
	if got := panelRowsText(drawPanelsOn(13, 10, [2]StatSide{l, r}))[6]; !strings.Contains(got, "newer") || !strings.Contains(got, "#") {
		t.Errorf("mtimes of two archives a second apart %q", got)
	}
	l.Stat.MtimePrecision, r.Stat.MtimePrecision, r.Stat.Mtime = time.Microsecond, time.Microsecond, l.Stat.Mtime.Add(500*time.Nanosecond)
	if got := panelRowsText(drawPanelsOn(13, 10, [2]StatSide{l, r}))[6]; !strings.Contains(got, "newer") || !strings.Contains(got, "#") {
		t.Errorf("mtimes of two archives below a microsecond apart %q", got)
	}
	l.Stat.MtimePrecision, r.Stat.MtimePrecision, r.Stat.Mtime = 0, 0, l.Stat.Mtime

	// A short panel keeps the first rows.
	got := panelRowsText(drawPanelsOn(13, 3, [2]StatSide{l, r}))
	if !strings.HasPrefix(got[2], " mode │") || got[3] != "" || len(got) != 11 {
		t.Errorf("short panels:\n%s", strings.Join(got, "\n"))
	}
}

func TestPanelNotes(t *testing.T) {
	st := colors.Default()
	dir := &stat.Stat{Mode: unix.S_IFDIR | 0o755}
	link := &stat.Stat{Mode: unix.S_IFLNK | 0o777, Target: "evil\x1b[31m"}
	for _, tt := range []struct {
		name  string
		sides [2]StatSide
		want  [2]string
	}{
		{"missing and reading", [2]StatSide{{Missing: true}, {Entries: -1}}, [2]string{"missing", "…"}},
		{"read error", [2]StatSide{{Err: errors.New("permission denied")}, {Missing: true}}, [2]string{"failed reading (permission denied)", "missing"}},
		{"scan error first", [2]StatSide{{Stat: dir, ScanErr: errors.New("permission denied"), Entries: 0}, {Stat: dir, Entries: 1}}, [2]string{"error │ permission denied", ""}},
		{"entries", [2]StatSide{{Stat: dir, Entries: 12}, {Stat: dir, Entries: 1}}, [2]string{" type │ dir", " type │ dir"}},
		{"link target escaped", [2]StatSide{{Stat: link, Entries: -1}, {Missing: true}}, [2]string{` type │ link → evil\x1b[31m`, "missing"}},
	} {
		scr := drawPanelsOn(13, 10, tt.sides)
		if got, want := panelRowsText(scr)[0], pair(tt.want[0], tt.want[1]); got != want {
			t.Errorf("%s: first row %q, want %q", tt.name, got, want)
		}
	}
	scr := drawPanelsOn(13, 10, [2]StatSide{{Stat: dir, Entries: 12}, {Stat: dir, Entries: 1}})
	// A directory's number of entries is not marked.
	if rows := panelRowsText(scr); rows[1] != pair(" size │ 12 entries", " size │ 1 entry") {
		t.Errorf("entries %q", rows[1])
	}
	// The other side's rows are pushed down past an error, so that the
	// fields and their marks line up.
	scr = drawPanelsOn(13, 10, [2]StatSide{{Stat: dir, Entries: 1}, {Stat: dir, ScanErr: errors.New("permission denied"), Entries: 0}})
	if rows := panelRowsText(scr); rows[1] != pair(" type │ dir", " type │ dir") || rows[2] != pair(" size │ 1 entry", " size │ 0 entries") {
		t.Errorf("aligned rows:\n%s", strings.Join(rows, "\n"))
	}
	// A type opposite another is marked as in the tree.
	file := &stat.Stat{Mode: unix.S_IFREG | 0o755}
	scr = drawPanelsOn(13, 10, [2]StatSide{{Stat: dir, Entries: 0}, {Stat: file, Entries: -1}})
	if rows := panelRowsText(scr); rows[0] != marked(" type │ dir", '!', " type │ file") {
		t.Errorf("types %q", rows[0])
	}
	if c := scr.Cell(Layout(80).Mark, 2); c.Style != brighten(st.Type) {
		t.Errorf("type mark style %+v", c.Style)
	}
	// Symlinks' targets are marked where the targets are compared.
	other := &stat.Stat{Mode: unix.S_IFLNK | 0o777, Target: "elsewhere"}
	links := [2]StatSide{{Stat: link, Entries: -1}, {Stat: other, Entries: -1}}
	if rows := panelRowsText(drawPanelsOn(13, 10, links)); []rune(rows[0])[37] != '|' {
		t.Errorf("targets %q", rows[0])
	}
	if rows := panelRowsText(drawPanelsComparing(13, 10, links, scan.Mode)); []rune(rows[0])[37] != ' ' {
		t.Errorf("targets not compared %q", rows[0])
	}
	// A symlink's size, its target's length, is not compared as a file's:
	// with the targets left out, neither is marked.
	sized := [2]StatSide{{Stat: &stat.Stat{Mode: unix.S_IFLNK | 0o777, Target: "old", Size: 3}, Entries: -1},
		{Stat: &stat.Stat{Mode: unix.S_IFLNK | 0o777, Target: "long-target", Size: 11}, Entries: -1}}
	for _, row := range panelRowsText(drawPanelsComparing(13, 10, sized, scan.DefaultAttrs&^scan.Target)) {
		if r := []rune(row); len(r) > 37 && r[37] != ' ' {
			t.Errorf("symlinks, targets not compared: %q", row)
		}
	}
	// Nor is a file's size opposite another type's: the type is marked.
	sized[1] = StatSide{Stat: &stat.Stat{Mode: unix.S_IFREG | 0o777, Size: 11}, Entries: -1}
	if rows := panelRowsText(drawPanelsOn(13, 10, sized)); []rune(rows[1])[37] != ' ' {
		t.Errorf("size opposite another type %q", rows[1])
	}
	scr = drawPanelsOn(13, 10, [2]StatSide{{Err: errors.New("permission denied")}, {Missing: true}})
	if c := scr.Cell(1, 2); c.Style != st.Error {
		t.Errorf("error style %+v", c.Style)
	}
	scr = drawPanelsOn(13, 10, [2]StatSide{{Stat: link, Entries: -1}, {Missing: true}})
	x := strings.Index(panelRowsText(scr)[0], `\x1b`)
	if c := scr.Cell(1+len([]rune(panelRowsText(scr)[0][:x])), 2); c.Style != st.Escape {
		t.Errorf("escape style %+v", c)
	}
}

// TestTreePanels draws the panels of the selected entry, and without room
// for them, shows what could not be read in the status bar.
func TestTreePanels(t *testing.T) {
	left := writeTree(t, map[string]string{"f": "x"})
	right := writeTree(t, map[string]string{"f": "y"})
	s := scanned(t, left, right)
	m := tree.New(s.Root)
	m.Root.Find("f").L.Err = errors.New("permission denied")
	v := &TreeView{Styles: colors.Default(), Left: "/l", Right: "/r"}
	var asked *scan.Node
	stats := func(n *scan.Node) [2]StatSide {
		asked = n
		return [2]StatSide{{Missing: true}, {Missing: true}}
	}
	scr := term.NewScreen(80, 24)
	v.Draw(scr, m, TreeStatus{Stats: stats})
	if asked != m.Root.Find("f") || !strings.Contains(scr.String(), "missing") {
		t.Errorf("asked about %v:\n%s", asked, scr.String())
	}
	if status := lastLine(scr); strings.Contains(status, "permission denied") {
		t.Errorf("status %q", status)
	}

	asked = nil
	scr = term.NewScreen(80, 8)
	v.Draw(scr, m, TreeStatus{Stats: stats})
	if asked != nil || strings.Contains(scr.String(), "├") {
		t.Errorf("panels without room:\n%s", scr.String())
	}
	if status := lastLine(scr); status != " failed reading the left side (permission denied)" {
		t.Errorf("status %q", status)
	}
}

func TestPanelPath(t *testing.T) {
	st := colors.Default()
	long := "/home/u/a/" + strings.Repeat("x", 40) + "/end"
	sides := [2]StatSide{
		{Stat: file(t, 1, 0o644, "2026-11-02 14:10:00"), Entries: -1, Path: long},
		{Missing: true, Path: "/r/only\x1bhere"},
	}
	scr := term.NewScreen(80, 15)
	p := Layout(80)
	drawBoxes(scr, p, 15, nil, [2][]display.Glyph{}, [2]string{}, st)
	drawPanels(scr, p, 1, 11, sides, scan.DefaultAttrs|scan.Mtime, "/home/u", term.Style{}, st)
	got := panelRowsText(scr)
	// The path comes first, with ~ for home and shortened in the middle,
	// and the side's other rows follow.
	l, r := []rune(got[0])[:38], strings.TrimSpace(string([]rune(got[0])[39:]))
	if s := strings.TrimSpace(string(l)); !strings.HasPrefix(s, "path │ ~/a/xx") || !strings.HasSuffix(s, "xx/end") || !strings.Contains(s, "…") || scr.Cell(36, 2).R != 'd' {
		t.Errorf("left path %q", s)
	}
	if r != `path │ /r/only\x1bhere` {
		t.Errorf("right path %q", r)
	}
	if !strings.HasPrefix(got[1], " type │ file") || !strings.Contains(got[1], "missing") {
		t.Errorf("rows after the path %q", got[1])
	}
	// It links to the full path, not to its label; a missing side's path
	// is dimmed and not linked, and its escapes are highlighted.
	if c := scr.Cell(9, 2); c.R != '~' || c.Style.Link != display.ShellQuote(long) || c.Style.SGR != st.Plain.SGR {
		t.Errorf("left path start %+v", c)
	}
	if c := scr.Cell(2, 2); c.Style.Link != "" {
		t.Errorf("label linked %+v", c)
	}
	if c := scr.Cell(50, 2); c.R != '/' || c.Style.Link != "" || c.Style.Attr&term.Dim == 0 {
		t.Errorf("missing side's path %+v", c)
	}
	if c := scr.Cell(57, 2); c.R != '\\' || c.Style == st.Plain.With(term.Dim) {
		t.Errorf("escape in the path %+v", c)
	}
	// Without a path, the panel starts with the type, as before.
	sides[0].Path, sides[1].Path = "", ""
	if got := panelRowsText(drawPanelsOn(15, 11, sides)); !strings.HasPrefix(got[0], " type │") {
		t.Errorf("panel without a path starts %q", got[0])
	}
}

// A side in a git revision shows only what git records, and its path,
// which names no file, unlinked but not dimmed.
func TestPanelInRevision(t *testing.T) {
	st := colors.Default()
	sides := [2]StatSide{
		{Stat: &stat.Stat{Mode: unix.S_IFREG | 0o755, Size: 3}, Entries: -1, Path: "main:dir/f", InRevision: true},
		{Stat: &stat.Stat{Mode: unix.S_IFLNK | 0o777, Size: 1, Target: "f"}, Entries: -1, Path: "HEAD~:dir/f", InRevision: true},
	}
	scr := term.NewScreen(80, 15)
	p := Layout(80)
	drawBoxes(scr, p, 15, nil, [2][]display.Glyph{}, [2]string{}, st)
	drawPanels(scr, p, 1, 11, sides, scan.DefaultAttrs, "", term.Style{}, st)
	got := panelRowsText(scr)
	var labels []string
	for _, row := range got {
		if f := strings.Fields(row); len(f) > 1 && f[1] == "│" {
			labels = append(labels, f[0])
		}
	}
	if strings.Join(labels, " ") != "path type size mode" {
		t.Errorf("rows %q", got)
	}
	if !strings.Contains(got[1], "link → f") {
		t.Errorf("no target: %q", got[1])
	}
	if c := scr.Cell(9, 2); c.R != 'm' || c.Style.Link != "" || c.Style.Attr&term.Dim != 0 {
		t.Errorf("path %+v", c)
	}
}

func TestPanelChange(t *testing.T) {
	st := colors.Default()
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("+0130", 90*60))
	file := &stat.Stat{Mode: unix.S_IFREG | 0o644, Size: 3}
	for _, tc := range []struct {
		name  string
		right *Change
		want  string
	}{
		{"reading", &Change{}, "commit │ …"},
		{"failed", &Change{Err: errors.New("not changed in the last 9 commits")}, "commit │ not changed in the last 9 commits"},
		{"none", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sides := [2]StatSide{
				{Stat: file, Entries: -1, Path: "main:f", InRevision: true, Change: &Change{ID: "0123456789abcdef", AuthorName: "A\x1bB", AuthorEmail: "a@b", Authored: at, Committed: at.Add(time.Hour)}},
				{Stat: file, Entries: -1, Path: "HEAD~:f", InRevision: true, Change: tc.right},
			}
			scr := term.NewScreen(100, 15)
			p := Layout(100)
			drawBoxes(scr, p, 15, nil, [2][]display.Glyph{}, [2]string{}, st)
			drawPanels(scr, p, 1, 11, sides, scan.DefaultAttrs, "", term.Style{}, st)
			got := panelRowsText(scr)
			local := func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") }
			for i, want := range []string{
				"commit │ 0123456789abcdef",
				`author │ A\x1bB <a@b>`,
				"authored │ " + local(at),
				"committed │ " + local(at.Add(time.Hour)),
			} {
				if !strings.Contains(got[4+i], want) {
					t.Errorf("row %d: %q, want %q", 4+i, got[4+i], want)
				}
			}
			// The labels of both panels are as wide as the widest.
			if !strings.Contains(got[0], "     path │") {
				t.Errorf("labels not widened: %q", got[0])
			}
			right := strings.Join(got[4:], "\n")
			if tc.want == "" {
				if strings.Count(right, "commit │") != 1 {
					t.Errorf("rows %q", got)
				}
			} else if !strings.Contains(got[4], tc.want) {
				t.Errorf("right %q, want %q", got[4], tc.want)
			}
		})
	}
}

func TestPanelBesideGit(t *testing.T) {
	file := &stat.Stat{Mode: unix.S_IFREG | 0o644, Size: 3, Nlink: 1, Ino: 7, Mtime: time.Unix(1, 0)}
	for _, beside := range []bool{false, true} {
		sides := [2]StatSide{
			{Stat: &stat.Stat{Mode: unix.S_IFREG | 0o644, Size: 3}, Entries: -1, Path: "HEAD:f", InRevision: true},
			{Stat: file, Entries: -1, Path: "/repo/f", BesideGit: beside},
		}
		scr := term.NewScreen(100, 15)
		p := Layout(100)
		drawPanels(scr, p, 1, 11, sides, scan.DefaultAttrs, "", term.Style{}, colors.Default())
		var labels []string
		// The label before the right panel's bar, the row's last.
		for _, row := range panelRowsText(scr) {
			if f := strings.Fields(row[:max(strings.LastIndex(row, "│"), 0)]); len(f) > 0 {
				labels = append(labels, f[len(f)-1])
			}
		}
		want := "path type size mode owner links inode mtime ctime atime btime"
		if beside {
			want = "path type size mode mtime"
		}
		if got := strings.Join(labels, " "); got != want {
			t.Errorf("beside revision %v: labels %q, want %q", beside, got, want)
		}
		// The revision has no mtime for it to be newer than.
		if text := strings.Join(panelRowsText(scr), "\n"); strings.Contains(text, "newer") {
			t.Errorf("beside revision %v: mtime marked newer:\n%s", beside, text)
		}
	}
}

// TestPanelNotNewerBesideGit checks that two sides on disk compared as
// git, as a work tree with a directory, mark neither mtime newer, as the
// tree compares none: a sparse entry the index supplies has none.
func TestPanelNotNewerBesideGit(t *testing.T) {
	l := StatSide{Stat: file(t, 10, 0o644, "2026-11-02 14:10:00"), Entries: -1, BesideGit: true}
	r := StatSide{Stat: &stat.Stat{Mode: unix.S_IFREG | 0o644, Size: 10}, Entries: -1, BesideGit: true}
	text := strings.Join(panelRowsText(drawPanelsOn(13, 10, [2]StatSide{l, r})), "\n")
	if strings.Contains(text, "newer") {
		t.Errorf("mtime marked newer beside git:\n%s", text)
	}
}

// TestPanelOwnerNames checks that owners are compared by their ids, as
// the tree compares them, not by the names an archive records for them.
func TestPanelOwnerNames(t *testing.T) {
	l := StatSide{Stat: file(t, 10, 0o644, "2026-11-02 14:10:00"), Entries: -1, User: "alice", Group: "staff"}
	for _, tc := range []struct {
		uid  uint32
		want bool
	}{{1000, false}, {1001, true}} {
		st := file(t, 10, 0o644, "2026-11-02 14:10:00")
		st.Uid, st.User, st.Group = tc.uid, "build", "build"
		r := StatSide{Stat: st, Entries: -1, InArchive: true, User: "build", Group: "build"}
		text := strings.Join(panelRowsText(drawPanelsOn(14, 11, [2]StatSide{l, r})), "\n")
		if marked := strings.Contains(text, "~ owner"); marked != tc.want {
			t.Errorf("uid %d: owner marked %v:\n%s", tc.uid, marked, text)
		}
	}
}

// TestPanelImplied checks that a directory an archive doesn't list shows
// no mode, owner or mtime, and none differs from the other side's.
func TestPanelImplied(t *testing.T) {
	dir := file(t, 0, 0o700, "2026-11-02 14:10:00")
	dir.Mode = unix.S_IFDIR | 0o700
	l := StatSide{Stat: dir, Entries: 1}
	r := StatSide{Stat: &stat.Stat{Mode: unix.S_IFDIR | 0o755, Implied: true}, Entries: 1, InArchive: true}
	scr := drawPanelsOn(14, 11, [2]StatSide{l, r})
	text := strings.Join(panelRowsText(scr), "\n")
	for _, label := range []string{"mode", "owner", "mtime"} {
		var row string
		for _, line := range panelRowsText(scr) {
			if i := strings.LastIndex(line, " "+label+" │"); i >= 0 {
				row = line[i:]
			}
		}
		if !strings.Contains(row, "│ —") {
			t.Errorf("%s: %q", label, row)
		}
	}
	if strings.Contains(text, "newer") || strings.ContainsAny(text, "~#") {
		t.Errorf("marked as differing:\n%s", text)
	}
}

func TestPanelLabelsOnDisk(t *testing.T) {
	sides := [2]StatSide{{Stat: &stat.Stat{Mode: unix.S_IFREG}, Entries: -1, Path: "/f"}, {Missing: true, Entries: -1}}
	scr := term.NewScreen(80, 15)
	p := Layout(80)
	drawPanels(scr, p, 1, 11, sides, scan.DefaultAttrs, "", term.Style{}, colors.Default())
	if got := panelRowsText(scr); !strings.Contains(got[0], " path │") || strings.Contains(got[0], "  path │") {
		t.Errorf("labels %q", got[0])
	}
}

func TestPanelChangeStyle(t *testing.T) {
	st := colors.Default()
	item := brighten(st.Changed)
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	file := &stat.Stat{Mode: unix.S_IFREG | 0o644, Size: 3}
	left := &Change{ID: "aaaa", AuthorName: "A", AuthorEmail: "a@b", Authored: at, Committed: at}
	for _, tc := range []struct {
		name  string
		right *Change
		// styled is whether the commit, authored and committed rows take
		// the item's style.
		styled [4]bool
	}{
		{"same", &Change{ID: "aaaa", AuthorName: "A", AuthorEmail: "a@b", Authored: at, Committed: at}, [4]bool{}},
		{"other commit", &Change{ID: "bbbb", AuthorName: "A", AuthorEmail: "a@b", Authored: at, Committed: at.Add(time.Hour)}, [4]bool{true, false, false, true}},
		{"other author", &Change{ID: "bbbb", AuthorName: "A", AuthorEmail: "a@c", Authored: at, Committed: at}, [4]bool{true, true, false, false}},
		// The same time in another zone is the same.
		{"zone", &Change{ID: "bbbb", AuthorName: "A", AuthorEmail: "a@b", Authored: at.In(time.FixedZone("x", 3600)), Committed: at}, [4]bool{true, false, false, false}},
		{"looking", &Change{}, [4]bool{}},
		{"none", nil, [4]bool{true, true, true, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sides := [2]StatSide{
				{Stat: file, Entries: -1, Path: "a:f", InRevision: true, Change: left},
				{Stat: file, Entries: -1, Path: "b:f", InRevision: true, Change: tc.right},
			}
			scr := term.NewScreen(100, 15)
			p := Layout(100)
			drawPanels(scr, p, 1, 11, sides, scan.DefaultAttrs, "", item, st)
			for i, styled := range tc.styled {
				// The value starts after "committed │ ".
				c := scr.Cell(p.LeftX+12, 1+5+i)
				if got := c.Style == item; got != styled {
					t.Errorf("row %d: %q styled %v, want %v", i, c.R, got, styled)
				}
				if m := scr.Cell(p.Mark, 1+5+i).R; m != ' ' && m != 0 {
					t.Errorf("row %d marked %q", i, m)
				}
			}
		})
	}
}

// The path of a side in a revision links the part after the colon to the
// entry's path in the repository, keeping the revision whole when
// shortened.
func TestPanelRepoPath(t *testing.T) {
	st := colors.Default()
	file := &stat.Stat{Mode: unix.S_IFREG | 0o644, Size: 3}
	for _, tc := range []struct {
		name, path string
		w          int
		// rev is the part shown whole and not linked, and linked whether
		// the rest is linked.
		rev    string
		linked bool
	}{
		{"fits", "main:dir/f", 80, "main:", true},
		{"shortened", "main:" + strings.Repeat("d/", 30) + "f", 60, "main:", true},
		{"no room", strings.Repeat("r", 40) + ":f", 50, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sides := [2]StatSide{
				{Stat: file, Entries: -1, Path: tc.path, InRevision: true, RepoPath: "d/it's"},
				{Missing: true, Entries: -1},
			}
			scr := term.NewScreen(tc.w, 15)
			p := Layout(tc.w)
			drawPanels(scr, p, 1, 11, sides, scan.DefaultAttrs, "", term.Style{}, st)
			x0 := p.LeftX + len(" path │ ") - len("│") + 1
			var plain, linked strings.Builder
			for x := x0; x < p.LeftX+p.LeftW; x++ {
				c := scr.Cell(x, 2)
				switch c.Style.Link {
				case "":
					if linked.Len() == 0 {
						plain.WriteRune(c.R)
					}
				case `'d/it'\''s'`:
					linked.WriteRune(c.R)
				default:
					t.Errorf("link %q", c.Style.Link)
				}
			}
			got, rest := strings.TrimRight(plain.String(), " "), linked.String()
			shortened := len(tc.path) > p.LeftW-len(" path │ ")
			switch {
			case tc.linked && (got != tc.rev || rest == "" || shortened != strings.Contains(rest, "…") || !strings.HasSuffix(tc.path, rest[strings.LastIndex(rest, "…")+len("…"):])):
				t.Errorf("not linked %q, linked %q", got, rest)
			case !tc.linked && (rest != "" || !strings.Contains(got, "…")):
				t.Errorf("not linked %q, linked %q", got, rest)
			}
		})
	}
}

// A submodule's panel shows its commit as its type, marked where the
// commits differ, and has no size or mode.
func TestPanelSubmodule(t *testing.T) {
	sub := func(commit string) *stat.Stat { return &stat.Stat{Mode: source.ModeGitlink, Target: commit} }
	sides := [2]StatSide{
		{Stat: sub("aaaa"), Entries: -1, InRevision: true},
		{Stat: sub("bbbb"), Entries: -1, InRevision: true},
	}
	scr := term.NewScreen(80, 15)
	p := Layout(80)
	drawPanels(scr, p, 1, 11, sides, scan.DefaultAttrs, "", term.Style{}, colors.Default())
	got := panelRowsText(scr)
	for i, want := range []string{"type │ submodule aaaa", "size │ —", "mode │ —"} {
		if !strings.Contains(got[i], want) {
			t.Errorf("row %d: %q, want %q", i, got[i], want)
		}
	}
	if m := scr.Cell(p.Mark, 2).R; m != '|' {
		t.Errorf("commits differing marked %q", m)
	}
	// A repository in the work tree with no commits has none to show.
	sides[1].Stat = sub("")
	scr = term.NewScreen(80, 15)
	drawPanels(scr, p, 1, 11, sides, scan.DefaultAttrs, "", term.Style{}, colors.Default())
	if got := panelRowsText(scr); !strings.Contains(got[0], "submodule (no commits)") {
		t.Errorf("row 0: %q, want submodule (no commits)", got[0])
	}
}

func TestStatRowsGitStatus(t *testing.T) {
	for _, sd := range []StatSide{
		{Path: "/r/f", Status: "deleted, not staged", Missing: true},
		{Path: "/r/f", Status: "modified, not staged", Stat: &stat.Stat{Mode: 0o100644}, BesideGit: true},
	} {
		rows := statRows(&sd, scan.DefaultAttrs, "")
		if len(rows) < 3 || rows[1].label != "git" || display.String(rows[1].value) != sd.Status {
			t.Errorf("rows of %+v: %+v", sd, rows)
		}
	}
	if rows := statRows(&StatSide{Path: "/r/f", Missing: true}, scan.DefaultAttrs, ""); len(rows) != 2 {
		t.Errorf("rows without a status: %+v", rows)
	}
}
