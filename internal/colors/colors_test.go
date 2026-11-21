// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package colors

import (
	"strings"
	"testing"

	"pxy.se/go/binate/internal/term"
)

func TestParse(t *testing.T) {
	r := Parse(Env{TrueColor: true, Colors: "changed=33:leftinline=1;41:dir=1;34:link=1;36:pipe=33:device=1;33:socket=1;35:exec=1;32:leftonly=32:rightonly=34:unverified=35:type=1;31:leftbg=48;5;52:rightbg=48;5;22:rightinline=48;5;28:select=48;5;240:escape=7:border=2:status=7"})
	if len(r.Warnings) != 0 {
		t.Errorf("warnings %q", r.Warnings)
	}
	if r.Styles.LeftInline != (term.Style{SGR: "1;41"}) {
		t.Errorf("inline %+v", r.Styles.LeftInline)
	}

	r = Parse(Env{Colors: "dir=38;5;208:escape=38;2;255;0;0:accent=1;30;46:quit=1;37;45:helpselect=1;33"})
	if r.Styles.Dir.SGR != "38;5;208" || r.Styles.Escape.SGR != "38;2;255;0;0" {
		t.Errorf("256-colour and truecolour values: %+v", r.Styles)
	}
	if r.Styles.HelpSelect.SGR != "1;33" {
		t.Errorf("helpselect colour: %+v", r.Styles.HelpSelect)
	}
	if r.Styles.Accent.SGR != "1;30;46" || r.Styles.Quit.SGR != "1;37;45" {
		t.Errorf("accent and quit colours: %+v, %+v", r.Styles.Accent, r.Styles.Quit)
	}
	if r.Styles.Changed != Default().Changed || r.Styles.LeftBg != Default256().LeftBg {
		t.Errorf("unset keys keep their defaults: %+v", r.Styles.Changed)
	}
}

func TestParseWarnings(t *testing.T) {
	r := Parse(Env{Colors: "bogus=1:dir=1;34m\x1b[31:only:escape=:changed=33::"})
	want := []string{
		`unknown key "bogus"`,
		"invalid value \"1;34m\x1b[31\" for dir",
		`no value in "only"`,
		`invalid value "" for escape`,
	}
	if len(r.Warnings) != len(want) {
		t.Fatalf("warnings %q", r.Warnings)
	}
	for i, w := range want {
		if !strings.Contains(r.Warnings[i], w) {
			t.Errorf("warning %q, want %q", r.Warnings[i], w)
		}
	}
	// Invalid values keep the defaults; valid ones apply.
	if r.Styles.Dir != Default256().Dir || r.Styles.Escape != Default256().Escape || r.Styles.Changed.SGR != "33" {
		t.Errorf("styles %+v", r.Styles)
	}
}

func TestNoColor(t *testing.T) {
	r := Parse(Env{Colors: "dir=1;34", NoColor: true, TrueColor: true})
	s := r.Styles
	for _, st := range []term.Style{s.Changed, s.LeftInline, s.RightInline, s.Dir, s.Link, s.Pipe, s.Device, s.Socket, s.Exec, s.LeftOnly, s.RightOnly, s.Unverified, s.Type, s.LeftBg, s.RightBg, s.Select, s.Escape, s.Border, s.Status, s.Accent, s.HelpSelect, s.Quit, s.QuitFocus, s.QuitKey, s.Plain} {
		if st.SGR != "" {
			t.Errorf("colour %q under NO_COLOR", st.SGR)
		}
	}
	if s.Select.Attr != term.Reverse || s.Escape.Attr != term.Reverse || s.LeftInline.Attr != term.Bold|term.Underline || s.RightInline.Attr != term.Bold|term.Underline || s.Changed.Attr != term.Bold {
		t.Errorf("attributes %+v", s)
	}
	// Escapes and in-line changes stay apart.
	if s.Escape == s.LeftInline {
		t.Error("escapes look like in-line changes")
	}
}

func TestFromEnv(t *testing.T) {
	for _, tt := range []struct {
		colorterm, term string
		want            bool
	}{
		{"truecolor", "xterm-256color", true},
		{"24bit", "xterm-256color", true},
		{"", "xterm-direct", true},
		{"", "xterm-256color", false},
		{"yes", "screen", false},
	} {
		env := map[string]string{"COLORTERM": tt.colorterm, "TERM": tt.term, "BINATE_COLORS": "dir=1", "NO_COLOR": ""}
		e := FromEnv(func(k string) string { return env[k] })
		if e.TrueColor != tt.want || e.Colors != "dir=1" || e.NoColor {
			t.Errorf("COLORTERM=%q TERM=%q: %+v", tt.colorterm, tt.term, e)
		}
	}
	// The backgrounds follow; text and marks are the same either way.
	tc, c256 := Parse(Env{TrueColor: true}).Styles, Parse(Env{}).Styles
	if tc != Default() || c256 != Default256() || tc.LeftBg == c256.LeftBg || tc.Changed != c256.Changed {
		t.Errorf("truecolour %+v\n256 colours %+v", tc, c256)
	}
}
