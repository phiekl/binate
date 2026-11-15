// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package cli

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLoggerSelectsLevelsByVerbosity(t *testing.T) {
	for name, tt := range map[string]struct {
		verbosity int
		want      []string
	}{
		"warnings": {verbosity: 0, want: []string{"warning: careful\n"}},
		"debug1":   {verbosity: 1, want: []string{"warning: careful\n", "debug1: one\n"}},
		"debug2":   {verbosity: 2, want: []string{"warning: careful\n", "debug1: one\n", "debug2: two\n"}},
		"debug3": {
			verbosity: 3,
			want:      []string{"warning: careful\n", "debug1: one\n", "debug2: two\n", "debug3: three\n"},
		},
		"clamped": {
			verbosity: 9,
			want:      []string{"warning: careful\n", "debug1: one\n", "debug2: two\n", "debug3: three\n"},
		},
		"negative": {verbosity: -1, want: []string{"warning: careful\n"}},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			log := NewLogger(&buf, tt.verbosity)
			Warn(log, "careful")
			log.Log(context.Background(), LevelDebug1, "one")
			log.Log(context.Background(), LevelDebug2, "two")
			log.Log(context.Background(), LevelDebug3, "three")
			if got, want := buf.String(), strings.Join(tt.want, ""); got != want {
				t.Errorf("NewLogger(%d) wrote %q, want %q", tt.verbosity, got, want)
			}
		})
	}
}

func TestLoggerFormatsAttributes(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, 1)
	log.Log(
		context.Background(), LevelDebug1, "agent: connected",
		"socket", "/run/agent.sock", "keys", 3,
	)
	want := "debug1: agent: connected socket=/run/agent.sock keys=3\n"
	if got := buf.String(); got != want {
		t.Errorf("logger wrote %q, want %q", got, want)
	}
}

func TestLoggerQuotesUnsafeValues(t *testing.T) {
	esc, bel := string(rune(0x1b)), string(rune(0x07))

	for name, tt := range map[string]struct{ in, want string }{
		"OSC sequence":     {in: "file" + esc + "]0;PWNED" + bel, want: `"file\x1b]0;PWNED\x07"`},
		"C1 introducer":    {in: "file" + string(rune(0x9b)) + "31m", want: `"file\u009b31m"`},
		"invalid UTF-8":    {in: "file" + string([]byte{0x9b}), want: `"file\x9b"`},
		"embedded space":   {in: "no such file", want: `"no such file"`},
		"embedded equals":  {in: "a=b", want: `"a=b"`},
		"embedded quote":   {in: `a"b`, want: `"a\"b"`},
		"empty":            {in: "", want: `""`},
		"nothing to quote": {in: "ssh-ed25519", want: "ssh-ed25519"},
		"non-ASCII text":   {in: "signaturé", want: "signaturé"},
		"bidi override":    {in: "a\u202eb", want: `"a\u202eb"`},
		"combining mark":   {in: "e\u0301", want: `"e\u0301"`},
		"backslash":        {in: `a\b`, want: `"a\\b"`},
	} {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			NewLogger(&buf, 1).Log(context.Background(), LevelDebug1, "msg", "value", tt.in)
			if got, want := buf.String(), "debug1: msg value="+tt.want+"\n"; got != want {
				t.Errorf("logger wrote %q, want %q", got, want)
			}
			if line := strings.TrimSuffix(buf.String(), "\n"); strings.ContainsFunc(line, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
				t.Errorf("logger wrote %q, want no raw control characters", line)
			}
		})
	}
}

func TestLoggerEscapesTheMessage(t *testing.T) {
	var buf bytes.Buffer
	NewLogger(&buf, 1).Log(context.Background(), LevelDebug1, "msg"+string(rune(0x1b))+"[31m")
	if got, want := buf.String(), `debug1: msg\x1b[31m`+"\n"; got != want {
		t.Errorf("logger wrote %q, want %q", got, want)
	}
}

func TestLoggerRendersWithAttrsAndGroups(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, 1).With("command", "verify").WithGroup("signature")
	log.Log(context.Background(), LevelDebug1, "read", "namespace", "file")
	want := "debug1: read command=verify signature.namespace=file\n"
	if got := buf.String(); got != want {
		t.Errorf("logger wrote %q, want %q", got, want)
	}
}

func TestLoggerInlinesGroupValues(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, 1)
	log.Log(
		context.Background(), LevelDebug1, "read",
		slog.Group("key", "type", "ssh-ed25519"),
		// Empty keys inline groups; empty groups are dropped.
		slog.Group("", "line", 1),
		slog.Group("dropped"),
	)
	want := "debug1: read key.type=ssh-ed25519 line=1\n"
	if got := buf.String(); got != want {
		t.Errorf("logger wrote %q, want %q", got, want)
	}
}

func TestLoggerNamesUnknownLevels(t *testing.T) {
	var buf bytes.Buffer
	NewLogger(&buf, 3).Log(context.Background(), slog.LevelError, "careful")
	if got, want := buf.String(), "error: careful\n"; got != want {
		t.Errorf("logger wrote %q, want %q", got, want)
	}
}
