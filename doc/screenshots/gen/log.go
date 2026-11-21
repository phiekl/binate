// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"io"
	"time"
)

// logger writes progress lines, each with the time since the start and a
// prefix such as the screenshot's name.
type logger struct {
	w      io.Writer
	start  time.Time
	prefix string
}

func newLogger(w io.Writer) *logger {
	return &logger{w: w, start: time.Now()}
}

func (l *logger) printf(format string, args ...any) {
	fmt.Fprintf(l.w, "%6.1fs %s%s\n", time.Since(l.start).Seconds(), l.prefix, fmt.Sprintf(format, args...))
}

// with returns a logger whose lines start with prefix.
func (l *logger) with(prefix string) *logger {
	c := *l
	c.prefix = l.prefix + prefix
	return &c
}
