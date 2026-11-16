// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package args

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

type testOpts struct {
	left    string
	right   string
	ns      string
	key     string
	noNS    bool
	json    bool
	verbose int
}

func newTestSet(o *testOpts) *Set {
	s := NewSet("prog cmd")
	s.Arg(&o.left, "left", "read the left side")
	s.Arg(&o.right, "right", "read the right side")
	s.DenyEmpty("left")
	s.DenyEmpty("right")
	s.String(&o.ns, "namespace", "n", "file", "use namespace")
	s.Bool(&o.noNS, "no-namespace", "N", "ignore namespace")
	s.String(&o.key, "key", "k", "", "use key")
	s.Bool(&o.json, "json", "j", "enable JSON output")
	s.Count(&o.verbose, "verbose", "v", "be verbose")
	s.Global("json", "verbose")
	return s
}

func TestParseValues(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want testOpts
	}{
		{"long separate", []string{"--key", "k", "--namespace", "x", "a", "b"}, testOpts{key: "k", ns: "x", left: "a", right: "b"}},
		{"long equals", []string{"--key=k", "--namespace=x", "a", "b"}, testOpts{key: "k", ns: "x", left: "a", right: "b"}},
		{"long equals keeps later equals", []string{"--key=a=b", "a", "b"}, testOpts{key: "a=b", ns: "file", left: "a", right: "b"}},
		{"long takes dash value", []string{"--key", "-j", "a", "b"}, testOpts{key: "-j", ns: "file", left: "a", right: "b"}},
		{"short separate", []string{"-k", "k", "a", "b"}, testOpts{key: "k", ns: "file", left: "a", right: "b"}},
		{"short attached", []string{"-kk", "a", "b"}, testOpts{key: "k", ns: "file", left: "a", right: "b"}},
		{"short equals is literal", []string{"-k=k", "a", "b"}, testOpts{key: "=k", ns: "file", left: "a", right: "b"}},
		{"short takes dash value", []string{"-k", "-h", "a", "b"}, testOpts{key: "-h", ns: "file", left: "a", right: "b"}},
		{"cluster", []string{"-jNvvk", "k", "a", "b"}, testOpts{key: "k", ns: "file", noNS: true, json: true, verbose: 2, left: "a", right: "b"}},
		{"cluster attached value", []string{"-jkk", "a", "b"}, testOpts{key: "k", ns: "file", json: true, left: "a", right: "b"}},
		{"count", []string{"-k", "k", "-v", "--verbose", "-vv", "a", "b"}, testOpts{key: "k", ns: "file", verbose: 4, left: "a", right: "b"}},
		{"last wins", []string{"-k", "a", "-k", "b", "a", "b"}, testOpts{key: "b", ns: "file", left: "a", right: "b"}},
		{"terminator", []string{"-k", "k", "--", "a", "b"}, testOpts{key: "k", ns: "file", left: "a", right: "b"}},
		{"flags between arguments", []string{"x", "-k", "k", "y", "-j"}, testOpts{key: "k", left: "x", right: "y", ns: "file", json: true}},
		{"dash arguments", []string{"-k", "k", "-", "-"}, testOpts{key: "k", left: "-", right: "-", ns: "file"}},
		{"arguments after terminator", []string{"-k", "k", "--", "-j", "-v"}, testOpts{key: "k", left: "-j", right: "-v", ns: "file"}},
		{"argument before terminator", []string{"-k", "k", "x", "--", "-j"}, testOpts{key: "k", left: "x", right: "-j", ns: "file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got testOpts
			if err := newTestSet(&got).Parse(tt.argv); err != nil {
				t.Fatalf("Parse(%q) error = %v", tt.argv, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.argv, got, tt.want)
			}
		})
	}
}

func TestParseShortInvalidUTF8(t *testing.T) {
	var value string
	s := NewSet("prog")
	s.String(&value, "value", "�", "", "")
	if err := s.Parse([]string{"-" + string([]byte{0xff}) + "x"}); err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if value != "x" {
		t.Errorf("value = %q, want %q", value, "x")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{"unknown long", []string{"--bogus"}, "unknown flag: --bogus"},
		{"unknown long with value", []string{"--bogus=x"}, "unknown flag: --bogus"},
		{"unknown short", []string{"-x"}, "unknown shorthand flag: 'x' in -x"},
		{"unknown short in cluster", []string{"-jx"}, "unknown shorthand flag: 'x' in -jx"},
		{"long missing value", []string{"--key"}, "flag needs an argument: --key"},
		{"short missing value", []string{"-k"}, "flag needs an argument: 'k' in -k"},
		{"cluster missing value", []string{"-jk"}, "flag needs an argument: 'k' in -jk"},
		{"bool with value", []string{"--json=true"}, "flag does not take a value: --json"},
		{"count with value", []string{"--verbose=3"}, "flag does not take a value: --verbose"},
		{"triple dash", []string{"---foo"}, "bad flag syntax: ---foo"},
		{"empty long name", []string{"--=x"}, "bad flag syntax: --=x"},
		{"third argument", []string{"-k", "k", "a", "b", "c"}, "unexpected argument after right: c"},
		{"third argument after flag", []string{"-k", "k", "a", "b", "-j", "c"}, "unexpected argument after right: c"},
		{"third argument after terminator", []string{"-k", "k", "a", "b", "--", "-j"}, "unexpected argument after right: -j"},
		{"missing second argument", []string{"-k", "k", "a"}, "missing argument: right"},
		{"missing second argument after terminator", []string{"-k", "k", "--", "a"}, "missing argument: right"},
		{"parse error before argument", []string{"-x", "foo"}, "unknown shorthand flag: 'x' in -x"},
		{"parse error after argument", []string{"foo", "--bogus"}, "unknown flag: --bogus"},
		{"empty", []string{"-k", "k", "", "b"}, "flag/argument is empty: left"},
		{"both empty", []string{"-k", "k", "", ""}, "flags/arguments are empty: left, right"},
		{"empty after terminator", []string{"-k", "k", "--", "a", ""}, "flag/argument is empty: right"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var o testOpts
			err := newTestSet(&o).Parse(tt.argv)
			if err == nil {
				t.Fatalf("Parse(%q) unexpectedly succeeded", tt.argv)
			}
			if errors.Is(err, ErrHelp) {
				t.Fatalf("Parse(%q) error = ErrHelp", tt.argv)
			}
			if err.Error() != tt.want {
				t.Errorf("Parse(%q) error = %q, want %q", tt.argv, err, tt.want)
			}
		})
	}
}

func TestParseNoArguments(t *testing.T) {
	var o testOpts
	for _, argv := range [][]string{nil, {"-k", "k"}, {"-j", "--"}} {
		if err := newTestSet(&o).Parse(argv); err != ErrUsage {
			t.Errorf("Parse(%q) error = %v, want ErrUsage", argv, err)
		}
	}
}

func TestParseWithoutArgument(t *testing.T) {
	tests := []struct {
		argv []string
		want error
	}{
		{[]string{"foo"}, nil},
		{[]string{"-"}, nil},
		{[]string{"--", "-h"}, nil},
		{[]string{"foo", "-h"}, ErrHelp},
	}
	for _, tt := range tests {
		var v bool
		s := NewSet("prog")
		s.Bool(&v, "value", "V", "")
		err := s.Parse(tt.argv)
		if tt.want != nil {
			if err != tt.want {
				t.Errorf("Parse(%q) error = %v, want %v", tt.argv, err, tt.want)
			}
		} else if err == nil || err.Error() != "no positional arguments expected" {
			t.Errorf("Parse(%q) error = %v, want no positional arguments", tt.argv, err)
		}
	}
}

func TestFunc(t *testing.T) {
	var got []string
	s := NewSet("prog")
	s.Func("include", "i", "", func(v string) { got = append(got, "+"+v) })
	s.Func("exclude", "e", "", func(v string) { got = append(got, "-"+v) })
	if err := s.Parse([]string{"-i", "a", "--exclude=b", "-ic", "--include", "", "-e", "-d"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"+a", "-b", "+c", "+", "--d"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if err := s.Parse([]string{"-i"}); err == nil || err.Error() != `flag needs an argument: 'i' in -i` {
		t.Errorf("Parse(-i) error = %v", err)
	}
}

func TestParseReportsAllEmpty(t *testing.T) {
	var a, b string
	s := NewSet("prog")
	s.String(&a, "aa", "a", "", "")
	s.String(&b, "bb", "b", "", "")
	s.DenyEmpty("aa")
	s.DenyEmpty("bb")
	if err := s.Parse([]string{"-a", "", "-b", ""}); err == nil ||
		err.Error() != "flags/arguments are empty: aa, bb" {
		t.Errorf("Parse() error = %v", err)
	}
}

func TestParseHelp(t *testing.T) {
	tests := [][]string{
		{"-h"},
		{"--help"},
		{"-jh"},
		{"-h", "foo"},
		{"-h", "-n", "a", "-N"},
		{"", "-h"},
		{"a", "b", "-h"},
		{"foo", "-h"},
	}
	for _, argv := range tests {
		var o testOpts
		if err := newTestSet(&o).Parse(argv); err != ErrHelp {
			t.Errorf("Parse(%q) error = %v, want ErrHelp", argv, err)
		}
	}

	notHelp := map[string][]string{
		"unknown flag: --bogus": {"-h", "--bogus"},
	}
	for want, argv := range notHelp {
		var o testOpts
		if err := newTestSet(&o).Parse(argv); err == nil || err.Error() != want {
			t.Errorf("Parse(%q) error = %v, want %q", argv, err, want)
		}
	}
}

func TestHelp(t *testing.T) {
	var o testOpts
	want := `usage: prog cmd [option]... left right

arguments:
  left                 read the left side
  right                read the right side

options:
  -k, --key            use key
  -n, --namespace      use namespace (default "file")
  -N, --no-namespace   ignore namespace

  -h, --help           show help and exit
  -j, --json           enable JSON output
  -v, --verbose        be verbose
`
	if got := newTestSet(&o).Help(0); got != want {
		t.Errorf("Help() =\n%s\nwant\n%s", got, want)
	}
}

func TestHide(t *testing.T) {
	var o testOpts
	s := newTestSet(&o)
	s.Hide("key", "json")
	help := s.Help(0)
	for _, name := range []string{"--key", "--json"} {
		if strings.Contains(help, name) {
			t.Errorf("hidden %s in help:\n%s", name, help)
		}
	}
	if !strings.Contains(help, "--namespace") || !strings.Contains(help, "--verbose") {
		t.Errorf("shown flags missing from help:\n%s", help)
	}
	if err := s.Parse([]string{"-k", "x", "--json", "a", "b"}); err != nil || o.key != "x" || !o.json {
		t.Errorf("hidden flags not parsed: %+v, %v", o, err)
	}
}

func TestLongOnlyHelp(t *testing.T) {
	var v string
	s := NewSet("prog")
	s.String(&v, "value", "", "", "set value")
	want := `usage: prog [option]...

options:
      --value   set value

  -h, --help    show help and exit
`
	if got := s.Help(0); got != want {
		t.Errorf("Help() =\n%s\nwant\n%s", got, want)
	}
}

func TestDefinitionErrorsPanic(t *testing.T) {
	tests := map[string]func(s *Set){
		"duplicate long": func(s *Set) {
			var v bool
			s.Bool(&v, "help", "", "")
		},
		"duplicate short": func(s *Set) {
			var v bool
			s.Bool(&v, "other", "h", "")
		},
		"multi-rune short": func(s *Set) {
			var v bool
			s.Bool(&v, "other", "ab", "")
		},
		"undefined global": func(s *Set) { s.Global("nope") },
		"undefined deny":   func(s *Set) { s.DenyEmpty("nope") },
		"deny on bool":     func(s *Set) { s.DenyEmpty("help") },
		"duplicate argument": func(s *Set) {
			var a, b string
			s.Arg(&a, "a", "")
			s.Arg(&b, "a", "")
		},
		"argument named as flag": func(s *Set) {
			var v string
			s.Arg(&v, "help", "")
		},
		"flag named as argument": func(s *Set) {
			var a string
			var v bool
			s.Arg(&a, "file", "")
			s.Bool(&v, "file", "", "")
		},
	}
	for name, define := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("definition did not panic")
				}
			}()
			define(NewSet("prog"))
		})
	}
}

func TestHelpWrapping(t *testing.T) {
	s := NewSet("prog")
	var file string
	s.String(&file, "file", "f", "src", "read the input archive from this directory")
	want := `usage: prog [option]...

options:
  -f, --file   read the input
               archive from this
               directory (default
               "src")

  -h, --help   show help and exit
`
	if got := s.Help(35); got != want {
		t.Fatalf("wrapped help:\n%s\nwant:\n%s", got, want)
	}
	for _, columns := range []int{0, -1, 10, 100} {
		if got := s.Help(columns); got != s.Help(0) {
			t.Errorf("width %d unexpectedly wrapped help:\n%s", columns, got)
		}
	}
}

func TestWrapWords(t *testing.T) {
	for _, c := range []struct {
		name, text string
		width      int
		want       string
	}{
		{"long word", "read long-filename now", 6, "read\nlong-filename\nnow"},
		{"exact fit", "one two", 7, "one two"},
		{"unicode fits", "café café", 9, "café café"},
		{"unicode wraps", "café café", 8, "café\ncafé"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := wrapWords(c.text, c.width); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
