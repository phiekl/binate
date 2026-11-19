// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"path"
	"slices"
	"strconv"
	"strings"
)

// Attribute states other than a value, which can't hold a NUL.
const (
	attrSet         = "\x00set"
	attrUnset       = "\x00unset"
	attrUnspecified = "\x00unspecified"
)

// attrAssign sets an attribute to a state: attrSet, attrUnset,
// attrUnspecified or a value.
type attrAssign struct{ name, state string }

// attrRule assigns attributes to the paths its pattern matches.
type attrRule struct {
	pattern ignorePattern
	assigns []attrAssign
}

// attrFile is an attributes file's rules, for paths beneath prefix, and
// the macros it defines.
type attrFile struct {
	prefix string
	rules  []attrRule
	macros map[string][]attrAssign
}

// builtinMacros are the macros Git defines itself.
var builtinMacros = map[string][]attrAssign{
	"binary": {{"diff", attrUnset}, {"merge", attrUnset}, {"text", attrUnset}},
}

// parseAttributes parses an attributes file for paths beneath prefix, ""
// or ending in "/", as Git does. Macros are kept where macros is set, and
// lines that define them are otherwise ignored, as Git ignores them in a
// directory's .gitattributes.
func parseAttributes(text, prefix string, fold, macros bool) *attrFile {
	f := &attrFile{prefix: prefix}
	for line := range strings.Lines(strings.TrimPrefix(text, "\ufeff")) {
		line = strings.TrimLeft(strings.TrimRight(line, "\r\n"), " \t")
		if line == "" || line[0] == '#' {
			continue
		}
		pattern, rest, ok := attrPattern(line)
		if !ok {
			continue
		}
		assigns, ok := parseAssigns(rest)
		if !ok {
			continue
		}
		if name, ok := strings.CutPrefix(pattern, "[attr]"); ok {
			if macros && validAttrName(name) {
				if f.macros == nil {
					f.macros = map[string][]attrAssign{}
				}
				f.macros[name] = assigns
			}
			continue
		}
		// Git ignores negative patterns, and one for directories
		// matches no file.
		if strings.HasPrefix(pattern, "!") || strings.HasSuffix(pattern, "/") {
			continue
		}
		p, ok := newIgnorePattern(pattern, fold)
		if !ok {
			continue
		}
		f.rules = append(f.rules, attrRule{pattern: p, assigns: assigns})
	}
	return f
}

// attrPattern splits line into its pattern, unquoted if quoted as a C
// string, and the rest.
func attrPattern(line string) (pattern, rest string, ok bool) {
	if line[0] != '"' {
		end := strings.IndexAny(line, " \t")
		if end < 0 {
			return line, "", true
		}
		return line[:end], line[end:], true
	}
	for i := 1; i < len(line); i++ {
		switch line[i] {
		case '\\':
			i++
		case '"':
			// Go's escapes are C's, but for some Git doesn't write.
			p, err := strconv.Unquote(line[:i+1])
			return p, line[i+1:], err == nil
		}
	}
	return "", "", false
}

// parseAssigns parses a rule's attributes. A name Git doesn't allow makes
// it ignore the line.
func parseAssigns(text string) ([]attrAssign, bool) {
	var assigns []attrAssign
	for _, field := range strings.Fields(text) {
		var a attrAssign
		switch field[0] {
		case '-':
			a = attrAssign{field[1:], attrUnset}
		case '!':
			a = attrAssign{field[1:], attrUnspecified}
		default:
			name, value, ok := strings.Cut(field, "=")
			a = attrAssign{name, attrSet}
			if ok {
				a.state = value
			}
		}
		if !validAttrName(a.name) {
			return nil, false
		}
		assigns = append(assigns, a)
	}
	return assigns, true
}

// validAttrName reports whether Git allows name as an attribute's.
func validAttrName(name string) bool {
	if name == "" || name[0] == '-' {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c != '-' && c != '.' && c != '_' && !('0' <= c && c <= '9') && !('a' <= c && c <= 'z') && !('A' <= c && c <= 'Z') {
			return false
		}
	}
	return true
}

// matches reports whether r applies to rel, beneath its file's prefix.
func (r *attrRule) matches(rel, prefix string) bool {
	candidate := rel[len(prefix):]
	if r.pattern.basename {
		candidate = path.Base(rel)
	}
	return r.pattern.matches(candidate, byteRunes(candidate))
}

// filterAttributes are the attributes by which Git changes a file's bytes
// as it stages it.
var filterAttributes = []string{"text", "eol", "crlf", "filter", "ident", "working-tree-encoding"}

// filtering returns f with only the attributes of its rules that may make
// Git filter a file, those of filterAttributes or macros, and only the
// rules left with any, nil if none, as the others need no matching.
func (f *attrFile) filtering(macros map[string][]attrAssign) *attrFile {
	if f == nil {
		return nil
	}
	out := &attrFile{prefix: f.prefix}
	for _, r := range f.rules {
		var assigns []attrAssign
		for _, a := range r.assigns {
			if _, macro := macros[a.name]; macro || slices.Contains(filterAttributes, a.name) {
				assigns = append(assigns, a)
			}
		}
		if assigns != nil {
			out.rules = append(out.rules, attrRule{pattern: r.pattern, assigns: assigns})
		}
	}
	if out.rules == nil {
		return nil
	}
	return out
}

// attributesOf returns the attributes rel has from files, each taking
// precedence over the next, with macros those defined, as Git assigns
// them: each attribute as the first rule that assigns it, read from the
// end of each file, and a macro set as the attributes it stands for.
func attributesOf(rel string, files []*attrFile, macros map[string][]attrAssign) map[string]string {
	got := map[string]string{}
	var fill func([]attrAssign)
	fill = func(assigns []attrAssign) {
		for i := len(assigns) - 1; i >= 0; i-- {
			a := assigns[i]
			if _, ok := got[a.name]; ok {
				continue
			}
			got[a.name] = a.state
			if m, ok := macros[a.name]; ok && a.state == attrSet {
				fill(m)
			}
		}
	}
	for _, f := range files {
		if !strings.HasPrefix(rel, f.prefix) {
			continue
		}
		for i := len(f.rules) - 1; i >= 0; i-- {
			if f.rules[i].matches(rel, f.prefix) {
				fill(f.rules[i].assigns)
			}
		}
	}
	return got
}

// macrosOf returns the macros files define, each taking precedence over
// the next, and Git's own.
func macrosOf(files []*attrFile) map[string][]attrAssign {
	macros := map[string][]attrAssign{}
	for i := len(files) - 1; i >= -1; i-- {
		m := builtinMacros
		if i >= 0 {
			m = files[i].macros
		}
		for name, assigns := range m {
			if i < 0 {
				if _, ok := macros[name]; ok {
					continue
				}
			}
			macros[name] = assigns
		}
	}
	return macros
}

// filters reports whether Git filters a file of attributes attrs as it
// stages it, where autocrlf is core.autoCRLF and driver whether a filter
// driver is configured: it converts line endings, expands ident or
// converts the encoding, or runs a filter.
func filters(attrs map[string]string, autocrlf bool, driver func(string) bool) bool {
	text, ok := attrs["text"]
	if !ok || text == attrUnspecified {
		// The old crlf attribute takes its place.
		text = attrs["crlf"]
	}
	switch text {
	case attrSet, "auto", "input":
		return true
	case attrUnset:
	default:
		// eol makes a file text, and core.autoCRLF one not said to be
		// text or not.
		if eol := attrs["eol"]; eol == "lf" || eol == "crlf" || autocrlf {
			return true
		}
	}
	if attrs["ident"] == attrSet {
		return true
	}
	if enc, ok := attrs["working-tree-encoding"]; ok && !strings.HasPrefix(enc, "\x00") {
		return true
	}
	filter, ok := attrs["filter"]
	return ok && !strings.HasPrefix(filter, "\x00") && driver(filter)
}
