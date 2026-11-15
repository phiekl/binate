// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"strings"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/term"
)

// Panel draws a box with a sharp-cornered border, all in st, as the
// loading box, Help and the quit prompt use, blanking its inside.
func Panel(s *term.Screen, st term.Style, x, y, w, h int) {
	horizontal := strings.Repeat("─", max(w-2, 0))
	s.Label(x, y, w, "┌"+horizontal+"┐", st)
	s.Label(x, y+h-1, w, "└"+horizontal+"┘", st)
	for row := y + 1; row < y+h-1; row++ {
		s.Set(x, row, '│', 1, st)
		s.Fill(x+1, row, w-2, st)
		s.Set(x+w-1, row, '│', 1, st)
	}
}

// The quit prompt's question and buttons, as dialog(1) draws them. The
// Exit button's x is its key, at exitKey.
const (
	QuitQuestion = "Are you sure you want to exit?"
	ExitButton   = "< Exit >"
	CancelButton = "<Cancel>"
	exitKey      = 3
)

// DrawQuit draws the quit prompt, centered over what s holds: the question,
// and below a rule its two buttons, the one to exit focused if exit is
// set. Blank rows pad the question when there is room.
func DrawQuit(s *term.Screen, st colors.Styles, exit bool) {
	const gap = 3
	buttons := len(ExitButton) + gap + len(CancelButton)
	w, h := min(len(QuitQuestion)+10, s.W), min(7, s.H)
	x, y := (s.W-w)/2, (s.H-h)/2
	Panel(s, st.Quit, x, y, w, h)
	if h < 5 {
		return
	}
	// The question sits in the rows above the rule, padded when it can be.
	textY := y + 1 + (h-4)/2
	s.Label(x+max((w-len(QuitQuestion))/2, 1), textY, max(w-2, 0), QuitQuestion, st.Quit)
	s.Label(x, y+h-3, w, "├"+strings.Repeat("─", max(w-2, 0))+"┤", st.Quit)

	exitStyle, cancelStyle := st.QuitFocus, st.Quit
	if !exit {
		exitStyle, cancelStyle = cancelStyle, exitStyle
	}
	bx, by := x+max((w-buttons)/2, 1), y+h-2
	room := max(w-2-(bx-x-1), 0)
	used := s.Label(bx, by, room, ExitButton, exitStyle)
	if used > exitKey {
		s.Set(bx+exitKey, by, rune(ExitButton[exitKey]), 1, exitStyle.On(st.QuitKey))
	}
	if room >= used+gap+len(CancelButton) {
		s.Label(bx+used+gap, by, len(CancelButton), CancelButton, cancelStyle)
	}
}

// listMark starts each text of a list in the error popup.
const listMark = "- "

// errorWidth is the widest the error popup's text is wrapped to, where
// the screen leaves room: narrower screens wrap it narrower.
const errorWidth = 80

// DrawError draws the error popup, centered over what s holds, in the quit
// prompt's colour: the title in bold, and below a line the message, set
// three columns in as in Help and wrapped at spaces to fit. More than one
// text is a list, each starting with "- ", and its lines after the first
// set in under its text, but for a first text ending in a colon, which
// leads it. The title and texts are escaped as names are, since an error
// may quote anything.
func DrawError(s *term.Screen, st colors.Styles, title string, texts ...string) {
	titleGlyphs := display.Name(title)
	width := max(min(errorWidth, s.W-8), 1)
	var lines [][]display.Glyph
	for i, text := range texts {
		glyphs := display.Name(strings.Join(strings.Fields(text), " "))
		if len(texts) == 1 || i == 0 && strings.HasSuffix(text, ":") {
			lines = append(lines, breakLines(glyphs, width)...)
			continue
		}
		for i, l := range breakLines(glyphs, max(width-len(listMark), 1)) {
			lead := listMark
			if i > 0 {
				lead = strings.Repeat(" ", len(listMark))
			}
			lines = append(lines, append(display.Plain(lead), l...))
		}
	}
	if len(lines) == 0 {
		lines = [][]display.Glyph{nil}
	}
	w := display.Width(titleGlyphs)
	for _, l := range lines {
		w = max(w, display.Width(l))
	}
	w = min(w+6, s.W)
	h := min(len(lines)+4, s.H)
	x, y := (s.W-w)/2, (s.H-h)/2
	Panel(s, st.Quit, x, y, w, h)
	if h < 4 {
		return
	}
	esc := rowEscape(st.Escape, st.Quit)
	s.Text(x+3, y+1, max(w-6, 0), titleGlyphs, st.Quit.With(term.Bold), esc)
	s.Label(x, y+2, w, "├"+strings.Repeat("─", max(w-2, 0))+"┤", st.Quit)
	for i, l := range lines {
		if y+3+i >= y+h-1 {
			break
		}
		s.Text(x+3, y+3+i, max(w-6, 0), l, st.Quit, esc)
	}
}
