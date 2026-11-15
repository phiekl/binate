// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package args parses command-line options and positional arguments.
package args

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// ErrHelp means -h/--help was given. The caller writes Help.
var ErrHelp = errors.New("help requested")

// ErrUsage means no arguments were given, though arguments are expected.
// The caller writes Help.
var ErrUsage = errors.New("missing arguments")

type kind int

const (
	kindString kind = iota
	kindBool
	kindCount
	kindFunc
)

type flag struct {
	long, short string
	usage       string
	def         string
	kind        kind
	str         *string
	boolean     *bool
	count       *int
	fn          func(string)
	changed     bool
	global      bool
	positional  bool
}

// Set holds flags and positional arguments.
type Set struct {
	name      string
	flags     []*flag
	args      []*flag
	help      bool
	denyEmpty []string
}

// NewSet returns a set named name, with -h/--help registered.
func NewSet(name string) *Set {
	s := &Set{name: name}
	s.Bool(&s.help, "help", "h", "show help and exit")
	s.Global("help")
	return s
}

// String registers a string flag with default value def.
func (s *Set) String(p *string, long, short, def, usage string) {
	*p = def
	s.add(&flag{long: long, short: short, usage: usage, def: def, kind: kindString, str: p})
}

// Bool registers a flag that is set to true when given.
func (s *Set) Bool(p *bool, long, short, usage string) {
	*p = false
	s.add(&flag{long: long, short: short, usage: usage, kind: kindBool, boolean: p})
}

// Count registers a flag that counts how many times it is given.
func (s *Set) Count(p *int, long, short, usage string) {
	*p = 0
	s.add(&flag{long: long, short: short, usage: usage, kind: kindCount, count: p})
}

// Func registers a flag with a value, calling fn with the value each time
// it's given, in the order given, also among other Func flags.
func (s *Set) Func(long, short, usage string, fn func(string)) {
	s.add(&flag{long: long, short: short, usage: usage, kind: kindFunc, fn: fn})
}

func (s *Set) add(f *flag) {
	if f.long == "" || strings.HasPrefix(f.long, "-") || strings.Contains(f.long, "=") {
		panic(fmt.Sprintf("args: invalid flag name %q", f.long))
	}
	if f.short != "" && (utf8.RuneCountInString(f.short) != 1 || f.short == "-") {
		panic(fmt.Sprintf("args: invalid shorthand %q for flag %q", f.short, f.long))
	}
	if s.lookupArg(f.long) != nil {
		panic(fmt.Sprintf("args: flag %q is also an argument", f.long))
	}
	for _, other := range s.flags {
		if other.long == f.long || (f.short != "" && other.short == f.short) {
			panic(fmt.Sprintf("args: flag %q redefined", f.long))
		}
	}
	s.flags = append(s.flags, f)
}

// Arg registers a positional argument, after those registered before it.
// Arguments are required: Parse rejects a missing one, unless none are
// given at all, which is ErrUsage. Flags may come before, between or after
// arguments.
func (s *Set) Arg(p *string, name, usage string) {
	if s.lookupArg(name) != nil {
		panic(fmt.Sprintf("args: argument %q redefined", name))
	}
	if s.lookup(name) != nil {
		panic(fmt.Sprintf("args: argument %q is also a flag", name))
	}
	*p = ""
	s.args = append(s.args, &flag{long: name, usage: usage, kind: kindString, str: p, positional: true})
}

func (s *Set) lookupArg(name string) *flag {
	for _, f := range s.args {
		if f.long == name {
			return f
		}
	}
	return nil
}

// Global puts the named flags last in help, without wrapping their
// descriptions. Each group is sorted by long option name.
func (s *Set) Global(names ...string) {
	for _, name := range names {
		s.mustLookup(name).global = true
	}
}

// DenyEmpty makes Parse reject a string flag or the argument given an empty
// value.
func (s *Set) DenyEmpty(name string) {
	if s.mustLookupAny(name).kind != kindString {
		panic(fmt.Sprintf("args: flag %q is not a string", name))
	}
	s.denyEmpty = append(s.denyEmpty, name)
}

func (s *Set) mustLookup(name string) *flag {
	f := s.lookup(name)
	if f == nil {
		panic(fmt.Sprintf("args: undefined flag %q", name))
	}
	return f
}

// mustLookupAny also finds arguments.
func (s *Set) mustLookupAny(name string) *flag {
	if f := s.lookupArg(name); f != nil {
		return f
	}
	return s.mustLookup(name)
}

func (s *Set) lookup(name string) *flag {
	for _, f := range s.flags {
		if f.long == name {
			return f
		}
	}
	return nil
}

func (s *Set) lookupShort(short string) *flag {
	for _, f := range s.flags {
		if f.short == short {
			return f
		}
	}
	return nil
}

// Parse parses flags and the registered arguments, rejecting other
// positional arguments. Arguments after "--" are positional. Help takes
// precedence over validation errors, but not syntax errors.
func (s *Set) Parse(argv []string) error {
	var positional []string
	for {
		rest, terminated, err := s.parseFlags(argv)
		if err != nil {
			return err
		}
		if terminated || len(rest) == 0 {
			positional = append(positional, rest...)
			break
		}
		positional, argv = append(positional, rest[0]), rest[1:]
	}
	if s.help {
		return ErrHelp
	}
	switch {
	case len(positional) == 0 && len(s.args) > 0:
		return ErrUsage
	case len(s.args) == 0 && len(positional) > 0:
		return errors.New("no positional arguments expected")
	case len(positional) > len(s.args):
		last := s.args[len(s.args)-1].long
		return fmt.Errorf("unexpected argument after %s: %s", last, positional[len(s.args)])
	case len(positional) < len(s.args):
		return fmt.Errorf("missing argument: %s", s.args[len(positional)].long)
	}
	for i, value := range positional {
		s.args[i].set(value)
	}
	return s.validate()
}

// parseFlags stops at the first positional argument and returns it and all
// that follow, or at "--" and returns all that follow with terminated set.
func (s *Set) parseFlags(argv []string) (rest []string, terminated bool, err error) {
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "--":
			return argv[i+1:], true, nil
		case arg == "-" || !strings.HasPrefix(arg, "-"):
			return argv[i:], false, nil
		case strings.HasPrefix(arg, "--"):
			consumed, err := s.parseLong(arg, argv[i+1:])
			if err != nil {
				return nil, false, err
			}
			i += consumed
		default:
			consumed, err := s.parseShort(arg, argv[i+1:])
			if err != nil {
				return nil, false, err
			}
			i += consumed
		}
	}
	return nil, false, nil
}

func (s *Set) parseLong(arg string, next []string) (int, error) {
	name, value, hasValue := strings.Cut(arg[2:], "=")
	if name == "" || strings.HasPrefix(name, "-") {
		return 0, fmt.Errorf("bad flag syntax: %s", arg)
	}
	f := s.lookup(name)
	if f == nil {
		return 0, fmt.Errorf("unknown flag: --%s", name)
	}
	if !f.hasValue() {
		if hasValue {
			return 0, fmt.Errorf("flag does not take a value: --%s", name)
		}
		f.set("")
		return 0, nil
	}
	if hasValue {
		f.set(value)
		return 0, nil
	}
	if len(next) == 0 {
		return 0, fmt.Errorf("flag needs an argument: --%s", name)
	}
	f.set(next[0])
	return 1, nil
}

// parseShort handles clusters and values attached to short flags.
func (s *Set) parseShort(arg string, next []string) (int, error) {
	shorts := arg[1:]
	for j, r := range shorts {
		f := s.lookupShort(string(r))
		if f == nil {
			return 0, fmt.Errorf("unknown shorthand flag: %q in %s", r, arg)
		}
		if !f.hasValue() {
			f.set("")
			continue
		}
		_, size := utf8.DecodeRuneInString(shorts[j:])
		if value := shorts[j+size:]; value != "" {
			f.set(value)
			return 0, nil
		}
		if len(next) == 0 {
			return 0, fmt.Errorf("flag needs an argument: %q in %s", r, arg)
		}
		f.set(next[0])
		return 1, nil
	}
	return 0, nil
}

// hasValue returns whether the flag takes a value.
func (f *flag) hasValue() bool {
	return f.kind == kindString || f.kind == kindFunc
}

func (f *flag) set(value string) {
	f.changed = true
	switch f.kind {
	case kindString:
		*f.str = value
	case kindBool:
		*f.boolean = true
	case kindCount:
		*f.count++
	case kindFunc:
		f.fn(value)
	}
}

func (s *Set) validate() error {
	var empty []string
	for _, name := range s.denyEmpty {
		if f := s.mustLookupAny(name); f.changed && *f.str == "" {
			empty = append(empty, name)
		}
	}
	switch len(empty) {
	case 0:
	case 1:
		return fmt.Errorf("flag/argument is empty: %s", empty[0])
	default:
		return fmt.Errorf("flags/arguments are empty: %s", strings.Join(empty, ", "))
	}
	return nil
}

// Help lists positional arguments, then options sorted by long name, with
// global options last. Non-global descriptions wrap at the given number
// of columns. Nonpositive values disable wrapping.
func (s *Set) Help(columns int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "usage: %s [option]...", s.name)
	for _, f := range s.args {
		fmt.Fprintf(&b, " %s", f.long)
	}
	b.WriteByte('\n')
	options := slices.Clone(s.flags)
	slices.SortFunc(options, func(a, b *flag) int {
		if a.global != b.global {
			if a.global {
				return 1
			}
			return -1
		}
		return strings.Compare(a.long, b.long)
	})
	flags := append(slices.Clone(s.args), options...)
	names := make([]string, len(flags))
	width := 0
	for i, f := range flags {
		names[i] = f.helpName()
		width = max(width, utf8.RuneCountInString(names[i]))
	}
	indent := strings.Repeat(" ", width+3)
	for i, f := range flags {
		if i == 0 || f.positional != flags[i-1].positional {
			if f.positional {
				b.WriteString("\n" + plural("argument", len(s.args)) + ":\n")
			} else {
				b.WriteString("\noptions:\n")
			}
		} else if f.global && !flags[i-1].global {
			b.WriteByte('\n')
		}
		usage := f.usage
		if f.kind == kindString && f.def != "" {
			usage += fmt.Sprintf(" (default %q)", f.def)
		}
		if !f.global && columns > len(indent) {
			usage = strings.ReplaceAll(wrapWords(usage, columns-len(indent)), "\n", "\n"+indent)
		}
		fmt.Fprintf(&b, "%-*s   %s\n", width, names[i], usage)
	}
	return b.String()
}

func plural(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// wrapWords breaks on whitespace, leaving words wider than width intact.
func wrapWords(text string, width int) string {
	var b strings.Builder
	column := 0
	for _, word := range strings.Fields(text) {
		length := utf8.RuneCountInString(word)
		if column > 0 {
			if column+1+length > width {
				b.WriteByte('\n')
				column = 0
			} else {
				b.WriteByte(' ')
				column++
			}
		}
		b.WriteString(word)
		column += length
	}
	return b.String()
}

func (f *flag) helpName() string {
	if f.positional {
		return "  " + f.long
	}
	if f.short != "" {
		return "  -" + f.short + ", --" + f.long
	}
	return "      --" + f.long
}
