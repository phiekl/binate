// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package colors holds the styles binate draws with, from BINATE_COLORS and
// NO_COLOR.
package colors

import (
	"fmt"
	"strings"

	"pxy.se/go/binate/internal/term"
)

// Styles are the styles of each kind of text.
type Styles struct {
	Changed    term.Style // the | mark
	Attrs      term.Style // the ~ mark
	Mtime      term.Style // the # mark
	LeftOnly   term.Style // the < mark
	RightOnly  term.Style // the > mark
	Unverified term.Style // the ? mark
	Type       term.Style // the ! mark
	Error      term.Style // why a file or field could not be shown
	// The letters of a git status: what is staged, and what is not or
	// untracked, in git's colours.
	Staged   term.Style
	Unstaged term.Style
	// The backgrounds of differing lines in the diff view, by side, as
	// git diff's red for a and green for b, and of changed text inside a
	// changed line.
	LeftBg      term.Style
	RightBg     term.Style
	LeftInline  term.Style
	RightInline term.Style
	Select      term.Style // the selected row
	Dir         term.Style // the dir type
	Link        term.Style // the link type
	Pipe        term.Style // the pipe type
	Device      term.Style // the char and bdev types
	Socket      term.Style // the sock type
	Exec        term.Style // the file type of an executable file
	Escape      term.Style // escaped characters
	Border      term.Style // the panes' borders
	Status      term.Style // the status bar's background and plain text
	Plain       term.Style // everything else

	Accent     term.Style // the filter bar, the loading box and Help
	HelpSelect term.Style // the selected row in Help
	Quit       term.Style // the quit prompt
	QuitFocus  term.Style // the quit prompt's focused button
	QuitKey    term.Style // the key of the quit prompt's Exit button, over the button, and the Compare popup's row keys
}

// Default returns the default styles on a terminal with truecolour. Text
// and marks use only the 16 ANSI colours, so they follow the terminal's
// palette, light or dark. The diff view's backgrounds are faint truecolour tints,
// which a light terminal needs others for, changed text inside a line a
// stronger one, and the selection is a grey. Escapes are in reverse video,
// so they never look like changed text. The entry types take ls's default
// colours.
func Default() Styles {
	return Styles{
		Changed:    term.Style{SGR: "33"},
		Attrs:      term.Style{SGR: "36"},
		Mtime:      term.Style{SGR: "1"},
		Dir:        term.Style{SGR: "1;34"},
		Link:       term.Style{SGR: "1;36"},
		Pipe:       term.Style{SGR: "33"},
		Device:     term.Style{SGR: "1;33"},
		Socket:     term.Style{SGR: "1;35"},
		Exec:       term.Style{SGR: "1;32"},
		LeftOnly:   term.Style{SGR: "31"},
		RightOnly:  term.Style{SGR: "32"},
		Unverified: term.Style{SGR: "90"},
		Type:       term.Style{SGR: "1;35"},
		Error:      term.Style{SGR: "1;31"},
		Staged:     term.Style{SGR: "32"},
		Unstaged:   term.Style{SGR: "31"},

		LeftBg:      term.Style{SGR: "48;2;46;24;26"},
		RightBg:     term.Style{SGR: "48;2;22;40;28"},
		LeftInline:  term.Style{SGR: "48;2;104;38;42"},
		RightInline: term.Style{SGR: "48;2;36;92;48"},
		Select:      term.Style{SGR: "48;2;52;54;60"},

		Escape: term.Style{SGR: "7"},
		Border: term.Style{SGR: "2"},
		Status: term.Style{},

		Accent:     term.Style{SGR: "97;44"},
		HelpSelect: term.Style{SGR: "30;46"},
		Quit:       term.Style{SGR: "97;41"},
		QuitFocus:  term.Style{SGR: "1;97;44"},
		QuitKey:    term.Style{SGR: "1;93"},
	}
}

// Default256 returns the default styles on a terminal without truecolour:
// the backgrounds are the closest the 256-colour palette has, stronger
// than the truecolour ones, as its darkest colours are far from black.
func Default256() Styles {
	s := Default()
	s.LeftBg = term.Style{SGR: "48;5;52"}
	s.RightBg = term.Style{SGR: "48;5;22"}
	s.LeftInline = term.Style{SGR: "48;5;88"}
	s.RightInline = term.Style{SGR: "48;5;28"}
	s.Select = term.Style{SGR: "48;5;238"}
	return s
}

// NoColor returns the styles without colour: the marks are bold, in-line
// changes bold and underlined, escapes and the selected row in reverse
// video, the borders dim, and the rows untinted, so the
// meaning stays without colour.
func NoColor() Styles {
	bold := term.Style{Attr: term.Bold}
	return Styles{
		Changed:     bold,
		Attrs:       bold,
		Mtime:       bold,
		LeftInline:  term.Style{Attr: term.Bold | term.Underline},
		RightInline: term.Style{Attr: term.Bold | term.Underline},
		Dir:         bold,
		LeftOnly:    bold,
		RightOnly:   bold,
		Unverified:  bold,
		Type:        bold,
		Error:       bold,
		Staged:      bold,
		Unstaged:    bold,
		Select:      term.Style{Attr: term.Reverse},
		Escape:      term.Style{Attr: term.Reverse},
		Border:      term.Style{Attr: term.Dim},
		Status:      term.Style{},

		Accent:     term.Style{Attr: term.Reverse},
		HelpSelect: term.Style{},
		Quit:       term.Style{Attr: term.Reverse | term.Bold},
		QuitFocus:  term.Style{Attr: term.Bold},
		QuitKey:    term.Style{Attr: term.Underline},
	}
}

// Result is the styles in effect, and the warnings about BINATE_COLORS.
type Result struct {
	Styles   Styles
	Warnings []string
}

// Env is what the styles depend on in the environment.
type Env struct {
	// Colors is BINATE_COLORS.
	Colors string
	// NoColor is set if NO_COLOR is, and not empty.
	NoColor bool
	// TrueColor is set if the terminal says it shows truecolour.
	TrueColor bool
}

// FromEnv reads Env with getenv, such as os.Getenv. A terminal shows
// truecolour if COLORTERM is "truecolor" or "24bit", or TERM ends in
// "-direct": there is no reliable way to ask the terminal, so this is the
// convention terminals and programs follow.
func FromEnv(getenv func(string) string) Env {
	ct := getenv("COLORTERM")
	return Env{
		Colors:    getenv("BINATE_COLORS"),
		NoColor:   getenv("NO_COLOR") != "",
		TrueColor: ct == "truecolor" || ct == "24bit" || strings.HasSuffix(getenv("TERM"), "-direct"),
	}
}

// Parse returns the styles for an environment: the defaults for its
// terminal, or none with NO_COLOR, changed by BINATE_COLORS. That is a
// list of key=SGR separated by colons, as LS_COLORS, for example
// "dir=1;34:leftinline=1;41".
// An SGR value may contain only digits and semicolons, so it can't add
// other escape sequences. An unknown key or invalid value is a warning, and
// the default stays. Warnings quote the text as is: escaping it is up to
// whoever writes them (display.Stderr).
func Parse(e Env) Result {
	if e.NoColor {
		return Result{Styles: NoColor()}
	}
	r := Result{Styles: Default()}
	if !e.TrueColor {
		r.Styles = Default256()
	}
	fields := map[string]*term.Style{
		"changed":     &r.Styles.Changed,
		"attrs":       &r.Styles.Attrs,
		"mtime":       &r.Styles.Mtime,
		"dir":         &r.Styles.Dir,
		"link":        &r.Styles.Link,
		"pipe":        &r.Styles.Pipe,
		"device":      &r.Styles.Device,
		"socket":      &r.Styles.Socket,
		"exec":        &r.Styles.Exec,
		"leftonly":    &r.Styles.LeftOnly,
		"rightonly":   &r.Styles.RightOnly,
		"unverified":  &r.Styles.Unverified,
		"type":        &r.Styles.Type,
		"error":       &r.Styles.Error,
		"staged":      &r.Styles.Staged,
		"unstaged":    &r.Styles.Unstaged,
		"leftbg":      &r.Styles.LeftBg,
		"rightbg":     &r.Styles.RightBg,
		"leftinline":  &r.Styles.LeftInline,
		"rightinline": &r.Styles.RightInline,
		"select":      &r.Styles.Select,
		"escape":      &r.Styles.Escape,
		"border":      &r.Styles.Border,
		"status":      &r.Styles.Status,

		"accent":     &r.Styles.Accent,
		"helpselect": &r.Styles.HelpSelect,
		"quit":       &r.Styles.Quit,
		"quitfocus":  &r.Styles.QuitFocus,
		"quitkey":    &r.Styles.QuitKey,
	}
	for _, entry := range strings.Split(e.Colors, ":") {
		if entry == "" {
			continue
		}
		key, value, ok := strings.Cut(entry, "=")
		field := fields[key]
		switch {
		case !ok:
			r.Warnings = append(r.Warnings, fmt.Sprintf(`BINATE_COLORS: no value in "%s"`, entry))
		case field == nil:
			r.Warnings = append(r.Warnings, fmt.Sprintf(`BINATE_COLORS: unknown key "%s"`, key))
		case !validSGR(value):
			r.Warnings = append(r.Warnings, fmt.Sprintf(`BINATE_COLORS: invalid value "%s" for %s, which may contain only digits and ';'`, value, key))
		default:
			*field = term.Style{SGR: value}
		}
	}
	return r
}

func validSGR(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != ';' {
			return false
		}
	}
	return true
}
