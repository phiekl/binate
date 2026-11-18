// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/stat"
	"pxy.se/go/binate/internal/term"
)

// minListRows is how many rows a pane keeps for its list or text before
// its panel gives way.
const minListRows = 5

// panelRows is the most rows a panel shows: one field each.
const panelRows = 11

// paneRows splits the rows inside the boxes on a screen of height h, which
// is all but the borders and the status bar: the rows of the list or text,
// and of the panel below them and its divider, 0 if none fits.
func paneRows(h int) (rows, panel int) {
	inner := boxHeight(h) - 2
	panel = min(inner-minListRows-1, panelRows)
	if panel <= 0 {
		return inner, 0
	}
	return inner - panel - 1, panel
}

// The preview of the selected pair's differences takes from 3 to 8 of the
// tree's rows, a third of them, once 10 are left to the tree.
const (
	minPreviewRows     = 3
	maxPreviewRows     = 8
	minPreviewTreeRows = 10
)

// previewRows splits rows, the rows of the tree from paneRows, between
// the tree, and the preview below it and its divider, 0 if none fits.
func previewRows(rows int) (tree, preview int) {
	preview = min(rows/3, maxPreviewRows)
	if preview < minPreviewRows || rows-1-preview < minPreviewTreeRows {
		return rows, 0
	}
	return rows - 1 - preview, preview
}

// drawDividers draws the divider across both boxes at row y.
func drawDividers(s *term.Screen, p Panes, y int, st colors.Styles) {
	for i, x := range []int{p.LeftBox, p.RightBox} {
		if w := []int{p.LeftBoxW, p.RightBoxW}[i]; w >= 2 {
			s.Label(x, y, w, "├"+strings.Repeat("─", w-2)+"┤", st.Border)
		}
	}
}

// boxHeight is the height of the boxes on a screen of height h: all but
// the status bar.
func boxHeight(h int) int {
	return max(h-3, 1) + 2
}

// StatSide is what a side's panel shows.
type StatSide struct {
	// Missing is set if the side has no entry.
	Missing bool
	// Stat, or Err, is what was read; neither is set while reading.
	Stat *stat.Stat
	// User and Group name the owner, where known.
	User, Group string
	// Err is why Stat could not be read.
	Err error
	// ScanErr is what the scan could not read of the entry.
	ScanErr error
	// Entries is a directory's number of entries found by the scan, or -1.
	Entries int
	// Path, if set, is the side's full path, shown first, and linked to
	// unless the side is missing or in a revision.
	Path string
	// InRevision is set for a side in a git revision, which records only
	// an entry's type, target, size and mode: the rest is left out, and
	// the path, which names no file, is linked only by RepoPath.
	InRevision bool
	// BesideRevision is set for a side on disk compared with one in a
	// git revision, as a work tree is. Its owner, links, inode and times
	// but the modification time, which Git neither records nor compares,
	// are left out.
	BesideRevision bool
	// RepoPath, if set, of a side in a revision, is the entry's path from
	// the top of the repository, as git commands take it, linked to from
	// the part of Path after the revision and the colon.
	RepoPath string
	// Change, if set, is the commit that last changed the entry, of a
	// side with a history.
	Change *Change
}

// Change is the commit that last changed an entry, or why it isn't known.
type Change struct {
	// ID names the commit; none is known yet if empty and Err is nil.
	ID string
	// AuthorName and AuthorEmail are who made the change.
	AuthorName, AuthorEmail string
	// Authored and Committed are its author's and committer's times.
	Authored, Committed time.Time
	Err                 error
}

// statRow is a row of a panel: a field, or, without a label, a note.
type statRow struct {
	label string
	value []display.Glyph
	// compared is set for a field whose difference is highlighted, and
	// marked with mark between the panels.
	compared bool
	mark     rune
	// key, if set, is what is compared instead of the value shown.
	key string
	// failed is set for an error.
	failed bool
	// path is set for the path row, shortened in the middle, linked to
	// link if set, and dimmed if dim is.
	path bool
	link string
	dim  bool
	// linkFrom is the first glyph of the path linked, as after a
	// revision and its colon.
	linkFrom int
	// item is set for a row of the commit that last changed the entry,
	// whose value takes the item's style in the tree where it differs from
	// the other side's; pending, for one still looked for.
	item, pending bool
}

// statLabelWidth is the width of the widest label but a commit's, which
// widen the labels of the panels showing one.
const statLabelWidth = 5

// statRows returns the rows of a side's panel, most important first,
// with home shown as "~" in the path.
func statRows(sd *StatSide, compare scan.Attrs, home string) []statRow {
	var rows []statRow
	if sd.Path != "" {
		row := statRow{label: "path", value: display.Name(HomePath(sd.Path, home)), path: true, dim: sd.Missing}
		switch {
		case sd.Missing:
		case !sd.InRevision:
			// Quoted, to paste into a command line, which also keeps
			// controls out of the link.
			row.link = display.ShellQuote(sd.Path)
		case sd.RepoPath != "":
			if rev, rel, ok := strings.Cut(sd.Path, ":"); ok && rel != "" {
				pre := display.Name(rev + ":")
				row.value, row.linkFrom = append(pre, display.Name(rel)...), len(pre)
				row.link = display.ShellQuote(sd.RepoPath)
			}
		}
		rows = append(rows, row)
	}
	if sd.ScanErr != nil {
		rows = append(rows, statRow{label: "error", value: display.Name(sd.ScanErr.Error()), failed: true})
	}
	note := func(text string, failed bool) []statRow {
		return append(rows, statRow{value: display.Name(text), failed: failed})
	}
	switch {
	case sd.Missing:
		return note("missing", false)
	case sd.Err != nil:
		return note("failed reading ("+sd.Err.Error()+")", true)
	case sd.Stat == nil:
		return note("…", false)
	}
	st := sd.Stat
	field := func(label string, value []display.Glyph, attr scan.Attrs, mark rune) {
		rows = append(rows, statRow{label: label, value: value, compared: compare&attr != 0, mark: mark})
	}
	plain := func(label, value string, attr scan.Attrs, mark rune) {
		field(label, display.Plain(value), attr, mark)
	}
	// The type shows a symlink's target and a device's number, data; one
	// type opposite another is marked as in the tree (see drawPanels).
	typeAttr := ^scan.Attrs(0)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		typeAttr = scan.Target
	case unix.S_IFCHR, unix.S_IFBLK:
		typeAttr = scan.Device
	case source.ModeGitlink:
		// A submodule's commit is its contents.
		typeAttr = scan.Content
	}
	field("type", typeValue(st), typeAttr, '|')
	// Only a regular file's size is compared, with its contents: a
	// directory's entries are compared, and marked, in the tree, and a
	// symlink's size is its target's length, compared in the type.
	var sizeAttr scan.Attrs
	if st.Mode&unix.S_IFMT == unix.S_IFREG {
		sizeAttr = scan.Content
	}
	if st.Mode&unix.S_IFMT == source.ModeGitlink {
		// A submodule has neither in the tree: only its commit.
		plain("size", "—", 0, 0)
		plain("mode", "—", 0, 0)
	} else {
		plain("size", sizeValue(st, sd.Entries), sizeAttr, '|')
		plain("mode", fmt.Sprintf("%04o", st.Mode&0o7777), scan.Mode, '~')
	}
	if sd.InRevision {
		return append(rows, changeRows(sd.Change)...)
	}
	if !sd.BesideRevision {
		field("owner", ownerValue(st, sd.User, sd.Group), scan.Owner, '~')
		plain("links", fmt.Sprint(st.Nlink), 0, 0)
		plain("inode", fmt.Sprint(st.Ino), 0, 0)
	}
	plain("mtime", formatStatTime(st.Mtime, 0), scan.Mtime, '#')
	// Compared to the nanosecond, as the tree compares it.
	rows[len(rows)-1].key = fmt.Sprint(st.Mtime.UnixNano())
	if sd.BesideRevision {
		return rows
	}
	plain("ctime", formatStatTime(st.Ctime, 0), 0, 0)
	plain("atime", formatStatTime(st.Atime, 0), 0, 0)
	plain("btime", formatStatTime(st.Btime, 0), 0, 0)
	return rows
}

// changeRows returns the rows of the commit that last changed an entry:
// none without one, "…" while it is found.
func changeRows(c *Change) []statRow {
	plain := func(label, value string) statRow {
		return statRow{label: label, value: display.Plain(value)}
	}
	switch {
	case c == nil:
		return nil
	case c.Err != nil:
		return []statRow{{label: "commit", value: display.Name(c.Err.Error()), failed: true}}
	case c.ID == "":
		return []statRow{{label: "commit", value: display.Plain("…"), pending: true}}
	}
	rows := []statRow{
		plain("commit", c.ID),
		// As git shows it; both are any text the commit holds, shown
		// escaped as names are.
		{label: "author", value: display.Name(c.AuthorName + " <" + c.AuthorEmail + ">")},
		plain("authored", formatStatTime(c.Authored, 0)),
		plain("committed", formatStatTime(c.Committed, 0)),
	}
	// Compared to the second, as shown, and in any zone.
	rows[2].key = fmt.Sprint(c.Authored.Unix())
	rows[3].key = fmt.Sprint(c.Committed.Unix())
	for i := range rows {
		rows[i].item = true
	}
	return rows
}

// itemDiffers reports whether a row of the last change has another value,
// or none, on the other side's rows, unless that change is still looked
// for.
func itemDiffers(r statRow, other []statRow) bool {
	if !r.item || slices.ContainsFunc(other, func(o statRow) bool { return o.pending }) {
		return false
	}
	for _, o := range other {
		if o.label == r.label {
			return o.compareKey() != r.compareKey()
		}
	}
	return true
}

// labelWidth returns the width of the labels of both panels' rows: that
// of the widest, at least statLabelWidth.
func labelWidth(all [2][]statRow) int {
	w := statLabelWidth
	for _, rows := range all {
		for _, row := range rows {
			w = max(w, display.Width(display.Plain(row.label)))
		}
	}
	return w
}

// typeValue names the type, with a symlink's target or a device's number.
func typeValue(st *stat.Stat) []display.Glyph {
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return display.Plain("file")
	case unix.S_IFDIR:
		return display.Plain("dir")
	case unix.S_IFLNK:
		return append(display.Plain("link → "), display.Name(st.Target)...)
	case unix.S_IFIFO:
		return display.Plain("pipe")
	case unix.S_IFSOCK:
		return display.Plain("sock")
	case unix.S_IFCHR:
		return display.Plain(fmt.Sprintf("char %d,%d", unix.Major(st.Rdev), unix.Minor(st.Rdev)))
	case unix.S_IFBLK:
		return display.Plain(fmt.Sprintf("bdev %d,%d", unix.Major(st.Rdev), unix.Minor(st.Rdev)))
	case source.ModeGitlink:
		return append(display.Plain("submodule "), submoduleCommit(st.Target, len(st.Target))...)
	}
	return display.Plain("?")
}

// submoduleCommit is a submodule's commit, to n digits. A repository in
// the work tree with no commits yet has none.
func submoduleCommit(commit string, n int) []display.Glyph {
	if commit == "" {
		return display.Plain("(no commits)")
	}
	return display.Name(commit[:min(len(commit), n)])
}

// sizeValue is the size in bytes, also in binary units from 1 KiB, as
// "4300 (4.2 KiB)", or a directory's number of entries, where its size in
// bytes says little.
func sizeValue(st *stat.Stat, entries int) string {
	if st.Mode&unix.S_IFMT == unix.S_IFDIR && entries >= 0 {
		if entries == 1 {
			return "1 entry"
		}
		return fmt.Sprintf("%d entries", entries)
	}
	if st.Size < 1024 {
		return fmt.Sprint(st.Size)
	}
	// HumanSize ends in the unit's letter, from 1 KiB.
	human := HumanSize(st.Size)
	number, unit := human[:len(human)-1], human[len(human)-1:]
	return fmt.Sprintf("%d (%s %siB)", st.Size, number, unit)
}

// ownerValue is user:group, by name where known, then by id.
func ownerValue(st *stat.Stat, user, group string) []display.Glyph {
	ids := fmt.Sprintf("%d:%d", st.Uid, st.Gid)
	if user == "" && group == "" {
		return display.Plain(ids)
	}
	if user == "" {
		user = fmt.Sprint(st.Uid)
	}
	if group == "" {
		group = fmt.Sprint(st.Gid)
	}
	return append(display.Name(user+":"+group), display.Plain(" ("+ids+")")...)
}

// formatStatTime formats a time in local time, to the second with digits
// more of its fraction, or "—" for a time not recorded.
func formatStatTime(t time.Time, digits int) string {
	if t.IsZero() {
		return "—"
	}
	layout := "2006-01-02 15:04:05"
	if digits > 0 {
		layout += "." + strings.Repeat("0", digits)
	}
	return t.Local().Format(layout)
}

// fractionDigits returns how many digits of a second's fraction show two
// different times apart, at most 9.
func fractionDigits(a, b time.Time) int {
	unit := time.Second
	for digits := 0; digits < 9; digits++ {
		if !a.Truncate(unit).Equal(b.Truncate(unit)) {
			return digits
		}
		unit /= 10
	}
	return 9
}

// differs reports whether a compared field has another value on the
// other side's rows.
func differs(r statRow, other []statRow) bool {
	if !r.compared {
		return false
	}
	for _, o := range other {
		if o.label == r.label {
			return o.compareKey() != r.compareKey()
		}
	}
	return true
}

// compareKey is what is compared of a row: its key, or its value as shown.
func (r statRow) compareKey() string {
	if r.key != "" {
		return r.key
	}
	return display.String(r.value)
}

// drawPanels draws each pane's panel: a divider joined to the box at row
// y, then up to rows rows below it. Fields worth comparing that differ are
// marked between the panels, as in the tree, and take their mark's colour,
// when both sides were read; the newer mtime is marked too, unless a side
// is in a revision, which has none. The commit that
// last changed the entry, where it differs, takes the style of the item,
// as the tree shows it.
func drawPanels(s *term.Screen, p Panes, y, rows int, sides [2]StatSide, compare scan.Attrs, home string, item term.Style, st colors.Styles) {
	if rows <= 0 {
		return
	}
	all := [2][]statRow{statRows(&sides[0], compare, home), statRows(&sides[1], compare, home)}
	l, r := sides[0].Stat, sides[1].Stat
	both := l != nil && r != nil && sides[0].Err == nil && sides[1].Err == nil
	var newer [2]bool
	var bright bool
	if both {
		all[0], all[1] = alignRows(all[0], all[1])
		lt, rt := l.Mode&unix.S_IFMT, r.Mode&unix.S_IFMT
		if lt != rt {
			// The type differs, which says all; a file's size opposite
			// another type's says nothing more.
			for i := range all {
				for j := range all[i] {
					switch all[i][j].label {
					case "type":
						all[i][j].compared, all[i][j].mark = true, '!'
					case "size":
						all[i][j].compared = false
					}
				}
			}
		}
		isFileOrLink := func(t uint32) bool { return t == unix.S_IFREG || t == unix.S_IFLNK }
		bright = isFileOrLink(lt) || isFileOrLink(rt)
		// Mtimes that differ within a second are shown with as much of the
		// fraction as tells them apart, so that the mark, and which is
		// newer, can be seen. A revision has no mtime to compare.
		mtimes := !sides[0].InRevision && !sides[1].InRevision
		if digits := fractionDigits(l.Mtime, r.Mtime); mtimes && !l.Mtime.Equal(r.Mtime) && digits > 0 {
			for i, t := range []time.Time{l.Mtime, r.Mtime} {
				for j := range all[i] {
					if all[i][j].label == "mtime" {
						all[i][j].value = display.Plain(formatStatTime(t, digits))
					}
				}
			}
		}
		if mtimes {
			newer = [2]bool{l.Mtime.After(r.Mtime), r.Mtime.After(l.Mtime)}
		}
	}
	drawDividers(s, p, y, st)
	lw := labelWidth(all)
	for i := range 2 {
		x, w := p.LeftX, p.LeftW
		if i == 1 {
			x, w = p.RightX, p.RightW
		}
		for j, row := range all[i] {
			if j >= rows {
				break
			}
			var highlight term.Style
			switch {
			case both && differs(row, all[1-i]):
				highlight = panelMarkStyle(row.mark, bright, st)
				if i == 0 {
					s.Set(p.Mark, y+1+j, row.mark, 1, highlight)
				}
			case itemDiffers(row, all[1-i]):
				highlight = item
			}
			drawStatRow(s, x, y+1+j, w, lw, row, highlight, newer[i], st)
		}
	}
}

// alignRows pads the rows before the type, such as an error, on the side
// with fewer, so that each field is on the same row on both sides.
func alignRows(l, r []statRow) ([]statRow, []statRow) {
	typeRow := func(rows []statRow) int {
		return slices.IndexFunc(rows, func(row statRow) bool { return row.label == "type" })
	}
	li, ri := typeRow(l), typeRow(r)
	if li < 0 || ri < 0 {
		return l, r
	}
	pad := func(rows []statRow, at, n int) []statRow {
		return slices.Insert(rows, at, make([]statRow, n)...)
	}
	if li < ri {
		l = pad(l, li, ri-li)
	} else if ri < li {
		r = pad(r, ri, li-ri)
	}
	return l, r
}

// panelMarkStyle returns the style of a field's mark and value: its
// mark's colour, in the bright palette where the tree uses it.
func panelMarkStyle(mark rune, bright bool, st colors.Styles) term.Style {
	style := map[rune]term.Style{'|': st.Changed, '~': st.Attrs, '#': st.Mtime, '!': st.Type}[mark]
	if bright {
		style = brighten(style)
	}
	return style
}

// drawStatRow draws a row, its label right-aligned in labelW columns, its
// value in highlight if set.
func drawStatRow(s *term.Screen, x, y, w, labelW int, row statRow, highlight term.Style, newer bool, st colors.Styles) {
	value := st.Plain
	switch {
	case row.failed:
		value = st.Type
	case highlight != term.Style{}:
		value = highlight
	}
	if row.label == "" {
		if !row.failed {
			value = st.Plain.With(term.Dim)
		}
		s.Text(x, y, w, ShortenEnd(row.value, w), value, st.Escape)
		return
	}
	label := fmt.Sprintf("%*s │ ", labelW, row.label)
	used := s.Label(x, y, w, label, st.Border)
	if row.path {
		// The ends of a path tell the most; the link has it in full.
		room := max(w-used, 0)
		style := st.Plain
		if row.dim {
			style = style.With(term.Dim)
		}
		esc := rowEscape(st.Escape, style)
		value := row.value
		if pre := value[:row.linkFrom]; row.linkFrom > 0 && display.Width(pre) < room {
			// The part not linked, as a revision, kept whole, and the
			// part linked shortened in what room is left.
			n := s.Text(x+used, y, room, pre, style, esc)
			used, room, value = used+n, room-n, value[row.linkFrom:]
		} else if row.linkFrom > 0 {
			// No room for the part linked to show: none is.
			row.link = ""
		}
		style.Link, esc.Link = row.link, row.link
		s.Text(x+used, y, room, ShortenMiddle(value, room), style, esc)
		return
	}
	var tag []display.Glyph
	if newer && row.label == "mtime" {
		tag = display.Plain(" (newer)")
	}
	room := max(w-used-display.Width(tag), 0)
	used += s.Text(x+used, y, room, ShortenEnd(row.value, room), value, st.Escape)
	s.Text(x+used, y, w-used, tag, st.Plain.With(term.Bold), st.Escape)
}
