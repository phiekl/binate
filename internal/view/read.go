// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package view

import (
	"fmt"
	"strings"
	"time"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/term"
)

// ReadStatus is how far reading the sides, as archives, has got before
// they can be scanned.
type ReadStatus struct {
	Elapsed time.Duration
	// Entries are the entries read, and Current the path of the one being
	// read.
	Entries int64
	Current string
	// Read is how much has been read, of Total, or of an unknown amount
	// if Total is 0, as of a pipe.
	Read, Total int64
	// Failed leaves the loading box out, for reading's error to be shown
	// over the empty boxes alone.
	Failed bool
}

// DrawRead draws the empty boxes, titled left and right, with a loading
// box over them, as the tree view's while it scans: a spinner, the
// entries read, the elapsed time, a bar of how much is read if the total
// is known, the amount read and the size below the percentage, and the
// path being read. The loading box is left out once reading failed.
func DrawRead(s *term.Screen, st colors.Styles, home, left, right string, rs ReadStatus) {
	p := Layout(s.W)
	titles := [2][]display.Glyph{display.Name(HomePath(left, home)), display.Name(HomePath(right, home))}
	drawBoxes(s, p, boxHeight(s.H), nil, titles, [2]string{}, st)
	s.Fill(0, s.H-1, s.W, st.Plain)
	if rs.Failed {
		return
	}

	// The rows, most wanted first: the count, the bar, the amount and the
	// path, the box sized to fit them, as the scan's two.
	want := 3
	if rs.Total > 0 {
		want = 4
	}
	w, h := min(52, max(s.W-4, 0)), min(want+4, max(s.H-2, 3))
	x, y := (s.W-w)/2, max((s.H-h)/2, 0)
	accent := st.Accent
	dim := st.Border.On(accent)
	Panel(s, accent, x, y, w, h)

	frames := []rune(spinner)
	frame := frames[int(max(rs.Elapsed, 0)/spinnerFrame)%len(frames)]
	textX, textW := x+6, max(w-9, 0)
	rows := min(want, h-2)
	top := y + 1 + (h-2-rows)/2
	s.Set(x+3, top, frame, 1, accent.With(term.Bold))
	elapsed := fmt.Sprintf("%.1fs", rs.Elapsed.Seconds())
	count := ShortenEnd(display.Plain(entries(rs.Entries)), max(textW-len(elapsed)-2, 0))
	s.Text(textX, top, textW, count, accent, accent)
	s.Label(textX+max(textW-len(elapsed), 0), top, min(len(elapsed), textW), elapsed, dim)
	row := 1
	if rs.Total > 0 && row < rows {
		drawProgress(s, textX, top+row, textW, float64(rs.Read)/float64(rs.Total), accent, dim)
		row++
	}
	if row < rows {
		// The size, if known, right-aligned below the percentage.
		s.Label(textX, top+row, textW, byteSize(rs.Read)+" read", accent)
		if rs.Total > 0 {
			size := byteSize(rs.Total)
			s.Label(textX+max(textW-len(size), 0), top+row, min(len(size), textW), size, accent)
		}
		row++
	}
	if row < rows {
		path := ShortenMiddle(display.Name(rs.Current), textW)
		s.Text(textX, top+row, textW, path, dim, rowEscape(st.Escape, accent))
	}
}

// drawProgress draws a bar w wide at x, y, filled to the fraction f, in
// eighths of a cell, and the percentage after it.
func drawProgress(s *term.Screen, x, y, w int, f float64, st, dim term.Style) {
	f = min(max(f, 0), 1)
	percent := fmt.Sprintf("%3d%%", int(f*100))
	barW := w - len(percent) - 1
	if barW < 1 {
		s.Label(x, y, w, percent, st)
		return
	}
	eighths := int(f * float64(barW*8))
	full, part := eighths/8, eighths%8
	bar := strings.Repeat("█", full)
	if part > 0 {
		bar += string([]rune(" ▏▎▍▌▋▊▉")[part])
	}
	s.Label(x, y, barW, bar, st)
	if n := len([]rune(bar)); n < barW {
		s.Label(x+n, y, barW-n, strings.Repeat("░", barW-n), dim)
	}
	s.Label(x+barW+1, y, len(percent), percent, st)
}

// byteSize writes n bytes in binary units, as "512 MiB", or in bytes
// below 1 KiB.
func byteSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	h := HumanSize(n)
	return h[:len(h)-1] + " " + h[len(h)-1:] + "iB"
}
