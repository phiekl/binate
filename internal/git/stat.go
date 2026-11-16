// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"

	"pxy.se/go/binate/internal/diff"
)

// FileStat is a file changed between two trees, as git's diff stat
// counts it: its path, its modes before and after, 0 where it is missing,
// and the lines added and deleted, or for a binary file its sizes after
// and before.
type FileStat struct {
	Path             string
	OldMode, NewMode Mode
	Added, Deleted   int64
	Binary           bool
}

// fileChange is a file changed between two trees: its entries before and
// after, with mode 0 where it is missing.
type fileChange struct {
	path     string
	old, new TreeEntry
}

// changes appends the files changed from the tree a to the tree b, either
// zero for an empty tree, under prefix, in the order git's diff lists
// them: by path, a directory's name as with a slash after it.
func (r *Repo) changes(ctx context.Context, a, b ID, prefix string, out []fileChange) ([]fileChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	read := func(id ID) ([]TreeEntry, error) {
		if id.IsZero() {
			return nil, nil
		}
		return r.ReadTree(id)
	}
	as, err := read(a)
	if err != nil {
		return nil, err
	}
	bs, err := read(b)
	if err != nil {
		return nil, err
	}
	key := func(e TreeEntry) string {
		if e.Mode == ModeTree {
			return e.Name + "/"
		}
		return e.Name
	}
	// one adds the change of e alone on a side: its tree's files, or it.
	one := func(e TreeEntry, removed bool) error {
		if e.Mode == ModeTree {
			x, y := e.ID, ID{}
			if !removed {
				x, y = y, x
			}
			out, err = r.changes(ctx, x, y, prefix+e.Name+"/", out)
			return err
		}
		c := fileChange{path: prefix + e.Name, new: e}
		if removed {
			c.old, c.new = e, TreeEntry{}
		}
		out = append(out, c)
		return nil
	}
	for len(as) > 0 || len(bs) > 0 {
		var cmp int
		switch {
		case len(as) == 0:
			cmp = 1
		case len(bs) == 0:
			cmp = -1
		default:
			cmp = strings.Compare(key(as[0]), key(bs[0]))
		}
		switch {
		case cmp < 0:
			err = one(as[0], true)
			as = as[1:]
		case cmp > 0:
			err = one(bs[0], false)
			bs = bs[1:]
		default:
			x, y := as[0], bs[0]
			as, bs = as[1:], bs[1:]
			switch {
			case x.Mode == ModeTree && x.ID != y.ID:
				out, err = r.changes(ctx, x.ID, y.ID, prefix+x.Name+"/", out)
			case x.Mode != ModeTree && (x.Mode != y.Mode || x.ID != y.ID):
				out = append(out, fileChange{path: prefix + x.Name, old: x, new: y})
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// maxStatBlob is the largest file whose lines are counted: a larger one
// is counted as binary, as git does past its core.bigFileThreshold.
const maxStatBlob = 512 << 20

// binaryCheck is how much of a file is looked at for a NUL, which makes
// it binary, as git looks.
const binaryCheck = 8000

// DiffStat returns the files changed from the tree a to the tree b, either
// zero for an empty tree, with their lines counted as "git diff --stat
// --no-renames" counts them, in its order. A file is binary if it has a
// NUL in its first 8000 bytes; attributes are not read. It is canceled
// with ctx.
func (r *Repo) DiffStat(ctx context.Context, a, b ID) ([]FileStat, error) {
	changes, err := r.changes(ctx, a, b, "", nil)
	if err != nil {
		return nil, err
	}
	stats := make([]FileStat, len(changes))
	for i, c := range changes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		st := FileStat{Path: c.path, OldMode: c.old.Mode, NewMode: c.new.Mode}
		if c.old.ID != c.new.ID {
			old, oldBin, err := r.statContent(c.old)
			if err != nil {
				return nil, err
			}
			new, newBin, err := r.statContent(c.new)
			if err != nil {
				return nil, err
			}
			if oldBin || newBin {
				st.Binary = true
				if st.Deleted, err = r.statSize(c.old, old); err == nil {
					st.Added, err = r.statSize(c.new, new)
				}
				if err != nil {
					return nil, err
				}
			} else {
				added, deleted := diff.Count(old, new)
				st.Added, st.Deleted = int64(added), int64(deleted)
			}
		} else if c.old.Mode != 0 && c.new.Mode != 0 {
			// Only the mode changed: binary if the file is, but 0 either
			// way.
			_, st.Binary, err = r.statContent(c.new)
			if err != nil {
				return nil, err
			}
		}
		stats[i] = st
	}
	return stats, nil
}

// statContent returns the contents of e as git's diff sees them: a
// file's or symlink's blob, a submodule's commit as a line, nothing if
// missing, and whether they are binary, without them if too large.
func (r *Repo) statContent(e TreeEntry) ([]byte, bool, error) {
	switch e.Mode {
	case 0:
		return nil, false, nil
	case ModeSubmodule:
		return []byte("Subproject commit " + e.ID.String() + "\n"), false, nil
	}
	size, err := r.Size(e.ID)
	if err != nil {
		return nil, false, err
	}
	if size > maxStatBlob {
		return nil, true, nil
	}
	typ, data, err := r.Read(e.ID, maxStatBlob)
	if err != nil {
		return nil, false, err
	}
	if typ != Blob {
		return nil, false, corrupt(e.ID, "file of a %s", typ)
	}
	return data, bytes.IndexByte(data[:min(len(data), binaryCheck)], 0) >= 0, nil
}

// statSize returns the size of e, whose contents read are data, unless
// too large to have been read.
func (r *Repo) statSize(e TreeEntry, data []byte) (int64, error) {
	if e.Mode == 0 || data != nil {
		return int64(len(data)), nil
	}
	return r.Size(e.ID)
}

// FormatStat returns the lines "git show --stat=width --summary" writes
// of files: a line for each, its name, its lines changed and a graph of
// them, scaled to fit width, then the totals, then the files created,
// deleted, and whose modes changed. None if there are no files.
func FormatStat(files []FileStat, width int) []string {
	if len(files) == 0 {
		return nil
	}
	names := make([]string, len(files))
	var maxLen, binWidth, numberWidth int
	var maxChange int64
	for i, f := range files {
		names[i] = quotePath(f.Path)
		maxLen = max(maxLen, len(names[i]))
		if f.Binary {
			// "Bin XXX -> YYY bytes", the numbers aligned with "Bin".
			binWidth = max(binWidth, 14+decimalWidth(f.Added)+decimalWidth(f.Deleted))
			numberWidth = 3
			continue
		}
		maxChange = max(maxChange, f.Added+f.Deleted)
	}
	numberWidth = max(numberWidth, decimalWidth(maxChange))
	width = max(width, 16+6+numberWidth)
	graphWidth := int(maxChange)
	if maxChange+4 <= int64(binWidth) {
		graphWidth = binWidth - 4
	}
	nameWidth := maxLen
	if nameWidth+numberWidth+6+graphWidth > width {
		if graphWidth > width*3/8-numberWidth-6 {
			graphWidth = max(width*3/8-numberWidth-6, 6)
		}
		if nameWidth > width-numberWidth-6-graphWidth {
			nameWidth = width - numberWidth - 6 - graphWidth
		} else {
			graphWidth = width - numberWidth - 6 - nameWidth
		}
	}
	var lines []string
	var adds, dels int64
	for i, f := range files {
		name, prefix, room := names[i], "", nameWidth
		if len(name) > nameWidth {
			// The end of the name, from a slash if there is one.
			prefix, room = "...", max(nameWidth-3, 0)
			name = name[len(name)-room:]
			if slash := strings.IndexByte(name, '/'); slash >= 0 {
				name = name[slash:]
			}
		}
		pad := strings.Repeat(" ", max(room-len(name), 0))
		if f.Binary {
			line := fmt.Sprintf(" %s%s%s | %*s", prefix, name, pad, numberWidth, "Bin")
			if f.Added != 0 || f.Deleted != 0 {
				line += fmt.Sprintf(" %d -> %d bytes", f.Deleted, f.Added)
			}
			lines = append(lines, line)
			continue
		}
		adds += f.Added
		dels += f.Deleted
		add, del := f.Added, f.Deleted
		if int64(graphWidth) <= maxChange {
			total := scaleLinear(add+del, graphWidth, maxChange)
			if total < 2 && add != 0 && del != 0 {
				total = 2
			}
			if add < del {
				add = scaleLinear(add, graphWidth, maxChange)
				del = total - add
			} else {
				del = scaleLinear(del, graphWidth, maxChange)
				add = total - del
			}
		}
		line := fmt.Sprintf(" %s%s%s | %*d", prefix, name, pad, numberWidth, f.Added+f.Deleted)
		if f.Added+f.Deleted != 0 {
			line += " "
		}
		lines = append(lines, line+strings.Repeat("+", int(add))+strings.Repeat("-", int(del)))
	}
	lines = append(lines, statTotals(len(files), adds, dels))
	for i, f := range files {
		switch {
		case f.OldMode == 0:
			lines = append(lines, fmt.Sprintf(" create mode %06o %s", f.NewMode, names[i]))
		case f.NewMode == 0:
			lines = append(lines, fmt.Sprintf(" delete mode %06o %s", f.OldMode, names[i]))
		case f.OldMode != f.NewMode:
			lines = append(lines, fmt.Sprintf(" mode change %06o => %06o %s", f.OldMode, f.NewMode, names[i]))
		}
	}
	return lines
}

// statTotals returns the line of totals, as git's: insertions left out if
// none but deletions, and deletions if none but insertions.
func statTotals(files int, adds, dels int64) string {
	plural := func(n int64, word string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, word)
		}
		return fmt.Sprintf("%d %ss", n, word)
	}
	line := " " + plural(int64(files), "file") + " changed"
	if adds != 0 || dels == 0 {
		line += ", " + plural(adds, "insertion") + "(+)"
	}
	if dels != 0 || adds == 0 {
		line += ", " + plural(dels, "deletion") + "(-)"
	}
	return line
}

// scaleLinear scales it, of at most maxChange, to width columns, as git
// does: one at least if it isn't zero.
func scaleLinear(it int64, width int, maxChange int64) int64 {
	if it == 0 {
		return 0
	}
	return 1 + it*int64(width-1)/maxChange
}

// decimalWidth returns the digits of n.
func decimalWidth(n int64) int {
	return len(strconv.FormatInt(n, 10))
}

// quotePath quotes a path as git does with core.quotePath, its default:
// in double quotes, with C escapes and bytes outside printable ASCII in
// octal, if it has any of them, a double quote or a backslash.
func quotePath(p string) string {
	needs := false
	for i := 0; i < len(p); i++ {
		if c := p[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			needs = true
			break
		}
	}
	if !needs {
		return p
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c >= 0x20 && c < 0x7f:
			b.WriteByte(c)
		default:
			if esc := strings.IndexByte("\a\b\t\n\v\f\r", c); esc >= 0 {
				b.WriteByte('\\')
				b.WriteByte("abtnvfr"[esc])
			} else {
				fmt.Fprintf(&b, "\\%03o", c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
