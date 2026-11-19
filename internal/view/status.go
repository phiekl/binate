// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/term"
)

// statusWidth is the columns a git status takes before a side's type:
// a space and its two letters.
const statusWidth = 3

// statusVerbs name the letters of a git status.
var statusVerbs = map[byte]string{
	'A': "added",
	'M': "modified",
	'D': "deleted",
	'T': "type changed",
}

// unmergedText describes each kind of conflict, as git status does.
var unmergedText = map[string]string{
	"DD": "unmerged, both deleted",
	"AU": "unmerged, added by us",
	"UD": "unmerged, deleted by them",
	"UA": "unmerged, added by them",
	"DU": "unmerged, deleted by us",
	"AA": "unmerged, both added",
	"UU": "unmerged, both modified",
}

// NodeStatusText describes the status of n in words, of a work tree on the
// right side if right. Where a file's deletion is staged, only another
// file, not a directory, in its place keeps it, untracked.
func NodeStatusText(n *scan.Node, right bool) string {
	kind := n.Side(right).Kind
	return StatusText(n.Status, kind != scan.Missing && kind != scan.Dir)
}

// StatusText describes a git status in words, as "modified, not staged",
// or is "" for none. present reports whether the work tree has the entry,
// which one whose deletion is staged may still have, untracked.
func StatusText(st source.Status, present bool) string {
	switch {
	case st.IsZero():
		return ""
	case st.Untracked():
		return "untracked"
	case st.Unmerged():
		return unmergedText[st.String()]
	}
	if st.Staged == st.Unstaged {
		// Changed again after staging, as "MM".
		return statusVerbs[st.Staged] + ", partly staged"
	}
	var text string
	if st.Staged != ' ' {
		text = statusVerbs[st.Staged] + ", staged"
		if st.Staged == 'D' && present {
			text += ", kept untracked"
		}
	}
	if st.Unstaged != ' ' {
		if text != "" {
			text += ", then "
		}
		text += statusVerbs[st.Unstaged] + ", not staged"
	}
	return text
}

// statusStyles returns the styles of a status's letters: what is staged
// in one, and what is not in another, as git colours them. Both letters
// of an untracked or unmerged entry are of what is not staged.
func statusStyles(st source.Status, styles colors.Styles) (staged, unstaged term.Style) {
	if st.Untracked() || st.Unmerged() {
		return styles.Unstaged, styles.Unstaged
	}
	return styles.Staged, styles.Unstaged
}
