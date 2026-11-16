// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"strings"

	"pxy.se/go/binate/internal/display"
)

// showDate is the layout of git's default date format.
const showDate = "Mon Jan 2 15:04:05 2006 -0700"

// Show returns the commit id as "git show --no-patch" shows it, in git's
// medium format: its name, its parents if more than one, its author and
// the time authored, then its message indented, without blank lines
// around it, spaces at the ends of lines, or tabs. The author is as
// recorded, not mapped by a .mailmap, and no signature is checked.
func (r *Repo) Show(id ID) (string, error) {
	c, err := r.ReadCommit(id)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("commit " + id.String() + "\n")
	if len(c.Parents) > 1 {
		b.WriteString("Merge:")
		for _, p := range c.Parents {
			b.WriteString(" " + r.Abbrev(p))
		}
		b.WriteString("\n")
	}
	b.WriteString("Author: " + c.AuthorName + " <" + c.AuthorEmail + ">\n")
	b.WriteString("Date:   " + c.Author.Format(showDate) + "\n")
	lines := strings.Split(c.Message, "\n")
	// Blank lines around the message are left out.
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 {
		b.WriteString("\n")
	}
	for _, line := range lines {
		b.WriteString("    " + expandTabs(strings.TrimRight(line, " \t\r")) + "\n")
	}
	return b.String(), nil
}

// tabWidth is the width git expands a message's tabs to.
const tabWidth = 8

// expandTabs expands the tabs of a message's line, as git does: to the
// next multiple of tabWidth columns, East Asian wide characters taking
// two.
func expandTabs(line string) string {
	if !strings.Contains(line, "\t") {
		return line
	}
	var b strings.Builder
	col := 0
	for _, r := range line {
		if r == '\t' {
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += display.RuneWidth(r)
	}
	return b.String()
}
