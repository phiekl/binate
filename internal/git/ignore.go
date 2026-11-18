// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"strings"

	"pxy.se/go/binate/internal/files"
)

const maxIgnoreFile = 8 << 20

type ignorePattern struct {
	literal  string
	expr     *regexp.Regexp
	basename bool
	dirOnly  bool
	include  bool
	// fold matches literal ASCII letters of either case.
	fold bool
}

// ignoreRules is a directory's rules followed by those it inherited.
// Patterns in a file are tried in reverse order.
type ignoreRules struct {
	prefix   string
	patterns []ignorePattern
	parent   *ignoreRules
}

func (r *ignoreRules) ignores(rel string, isDir bool) bool {
	name := path.Base(rel)
	wide := byteRunes(rel)
	wideName := wide[wideLen(rel[:len(rel)-len(name)]):]
	for ; r != nil; r = r.parent {
		local, ok := strings.CutPrefix(rel, r.prefix)
		if !ok {
			continue
		}
		wideLocal := wide[wideLen(r.prefix):]
		for i := len(r.patterns) - 1; i >= 0; i-- {
			p := &r.patterns[i]
			if p.dirOnly && !isDir {
				continue
			}
			candidate, wideCandidate := local, wideLocal
			if p.basename {
				candidate, wideCandidate = name, wideName
			}
			if p.matches(candidate, wideCandidate) {
				return !p.include
			}
		}
	}
	return false
}

// matches reports whether p matches name, given also as byteRunes(name).
func (p *ignorePattern) matches(name, wide string) bool {
	if p.expr == nil {
		if p.fold {
			return asciiEqualFold(p.literal, name)
		}
		return p.literal == name
	}
	return p.expr.MatchString(wide)
}

// byteRunes returns s with each byte from 0x80 as the rune of that value.
// Git wildcards match bytes, including in non-UTF-8 file names, and so
// RE2 can then match each byte as one character.
func byteRunes(s string) string {
	for i := range len(s) {
		if s[i] >= 0x80 {
			var b strings.Builder
			b.Grow(wideLen(s))
			b.WriteString(s[:i])
			for j := i; j < len(s); j++ {
				b.WriteRune(rune(s[j]))
			}
			return b.String()
		}
	}
	return s
}

// wideLen returns the length of byteRunes(s). A byte from 0x80 takes two
// bytes as a rune.
func wideLen(s string) int {
	n := len(s)
	for i := range len(s) {
		if s[i] >= 0x80 {
			n++
		}
	}
	return n
}

// asciiEqualFold reports whether a and b are equal with ASCII letters of
// either case, as Git compares paths where core.ignoreCase is set.
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if asciiLower(a[i]) != asciiLower(b[i]) {
			return false
		}
	}
	return true
}

func asciiLower(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// parseIgnore parses an ignore file's patterns, matching letters of
// either case if fold is set.
func parseIgnore(data string, fold bool) []ignorePattern {
	var patterns []ignorePattern
	for line := range strings.Lines(strings.TrimPrefix(data, "\ufeff")) {
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		line, _, _ = strings.Cut(line, "\x00")
		line = trimIgnoreSpaces(line)
		if line == "" || line[0] == '#' {
			continue
		}
		line, include := strings.CutPrefix(line, "!")
		dirOnly := strings.HasSuffix(line, "/")
		if dirOnly {
			line = line[:len(line)-1]
		}
		basename := !strings.Contains(line, "/")
		line, _ = strings.CutPrefix(line, "/")
		if line == "" {
			continue
		}
		p := ignorePattern{basename: basename, dirOnly: dirOnly, include: include, fold: fold}
		if strings.ContainsAny(line, "*?[\\") {
			p.expr = compileIgnore(line, fold)
			if p.expr == nil {
				continue
			}
		} else {
			p.literal = line
		}
		patterns = append(patterns, p)
	}
	return patterns
}

func trimIgnoreSpaces(line string) string {
	for strings.HasSuffix(line, " ") {
		i := len(line) - 1
		for i > 0 && line[i-1] == '\\' {
			i--
		}
		if (len(line)-1-i)%2 != 0 {
			break
		}
		line = line[:len(line)-1]
	}
	return line
}

func compileIgnore(pattern string, fold bool) *regexp.Regexp {
	var expr strings.Builder
	expr.WriteString(`\A`)
	for i := 0; i < len(pattern); {
		c := pattern[i]
		switch c {
		case '*':
			end := i + 1
			for end < len(pattern) && pattern[end] == '*' {
				end++
			}
			wholeComponent := end-i > 1 && (i == 0 || pattern[i-1] == '/')
			switch {
			case wholeComponent && end < len(pattern) && pattern[end] == '/':
				expr.WriteString(`(?:[^/]+/)*`)
				end++
			case wholeComponent && end == len(pattern) && i > 0:
				expr.WriteString(`(?s:.+)`)
			default:
				expr.WriteString(`[^/]*`)
			}
			i = end
		case '?':
			expr.WriteString(`[^/]`)
			i++
		case '[':
			n := writeIgnoreClass(&expr, pattern[i:], fold)
			if n == 0 {
				return nil
			}
			i += n
		default:
			if c == '\\' {
				i++
				if i == len(pattern) {
					return nil
				}
				c = pattern[i]
			}
			if lower := asciiLower(c); fold && 'a' <= lower && lower <= 'z' {
				fmt.Fprintf(&expr, "[%c%c]", lower, lower+'A'-'a')
			} else {
				expr.WriteString(regexp.QuoteMeta(string(rune(c))))
			}
			i++
		}
	}
	expr.WriteString(`\z`)
	re, _ := regexp.Compile(expr.String())
	return re
}

// writeIgnoreClass parses one bracket expression. Remove slash from the
// resulting set, since even negated and POSIX classes can't cross it.
//
// With fold, Git lowercases the name's byte and matches it, or for a range
// or POSIX class also its uppercase, before negation. An uppercase letter
// on its own then matches nothing.
func writeIgnoreClass(expr *strings.Builder, pattern string, fold bool) int {
	var class strings.Builder
	class.WriteByte('[')
	i := 1
	negated := i < len(pattern) && (pattern[i] == '!' || pattern[i] == '^')
	if negated {
		i++
	}
	first := i
	rangeEnd := false
	single := func(c byte, next int) {
		rangeStart := next+1 < len(pattern) && pattern[next] == '-' && pattern[next+1] != ']'
		if !fold || c < 'A' || c > 'Z' || rangeStart || rangeEnd {
			fmt.Fprintf(&class, `\x%02x`, c)
		}
		rangeEnd = false
	}
	for i < len(pattern) {
		c := pattern[i]
		switch {
		case c == ']' && i > first:
			// Folding can leave nothing, as of [!X], which Git still
			// matches as an empty set.
			set := new([256]bool)
			if class.Len() > 1 {
				class.WriteByte(']')
				parsed, err := syntax.Parse(class.String(), syntax.Perl)
				if err != nil {
					return 0
				}
				set = classBytes(parsed)
			}
			writeClassBytes(expr, set, negated, fold)
			return i + 1
		case c == '\\':
			i++
			if i == len(pattern) {
				return 0
			}
			single(pattern[i], i+1)
		case strings.HasPrefix(pattern[i:], "[:"):
			end := strings.Index(pattern[i+2:], ":]")
			if end < 0 {
				return 0
			}
			end += i + 4
			class.WriteString(pattern[i:end])
			i = end - 1
			rangeEnd = false
		case c == '-':
			class.WriteByte(c)
			rangeEnd = i > first && i+1 < len(pattern) && pattern[i+1] != ']'
		default:
			single(c, i+1)
		}
		i++
	}
	return 0
}

// classBytes returns the bytes in a parsed character class.
func classBytes(class *syntax.Regexp) *[256]bool {
	ranges := class.Rune
	switch class.Op {
	case syntax.OpLiteral:
		ranges = []rune{ranges[0], ranges[0]}
	case syntax.OpAnyChar:
		ranges = []rune{0, 255}
	case syntax.OpAnyCharNotNL:
		ranges = []rune{0, '\n' - 1, '\n' + 1, 255}
	}
	var set [256]bool
	for i := 0; i+1 < len(ranges); i += 2 {
		for c := ranges[i]; c <= min(ranges[i+1], 255); c++ {
			set[c] = true
		}
	}
	return &set
}

func writeClassBytes(expr *strings.Builder, set *[256]bool, negated, fold bool) {
	if fold {
		for c := 'a'; c <= 'z'; c++ {
			upper := c + 'A' - 'a'
			set[c] = set[c] || set[upper]
			set[upper] = set[c]
		}
	}
	expr.WriteByte('[')
	// There is no NUL in a path, so this also gives an empty set a
	// valid regexp representation.
	expr.WriteString(`\x00`)
	for lo := 1; lo < len(set); lo++ {
		if set[lo] == negated || lo == '/' {
			continue
		}
		hi := lo
		for hi+1 < len(set) && set[hi+1] != negated && hi+1 != '/' {
			hi++
		}
		fmt.Fprintf(expr, `\x%02x-\x%02x`, lo, hi)
		lo = hi
	}
	expr.WriteByte(']')
}

// GlobalIgnore supplies the user's ignore file, and the user's
// configuration beneath the repository's. The file is parsed with the
// repository's core.ignoreCase. A relative core.excludesFile is opened
// beneath the work tree after confinement, as is one the repository's
// configuration sets in its place.
type GlobalIgnore struct {
	text     string
	relative string
	config   config
	// home expands "~/" in the repository's core.excludesFile.
	home string
}

// ReadGlobalIgnore reads user-level configuration and the user-selected
// ignore file before the child restricts its filesystem access.
func ReadGlobalIgnore(getenv func(string) string) (GlobalIgnore, error) {
	home, xdg := getenv("HOME"), getenv("XDG_CONFIG_HOME")
	if xdg == "" && home != "" {
		xdg = filepath.Join(home, ".config")
	}
	var configs []string
	if name := getenv("GIT_CONFIG_GLOBAL"); name != "" {
		// /dev/null is commonly used to disable user configuration.
		if name != os.DevNull {
			configs = append(configs, name)
		}
	} else {
		if xdg != "" {
			configs = append(configs, filepath.Join(xdg, "git", "config"))
		}
		if home != "" {
			configs = append(configs, filepath.Join(home, ".gitconfig"))
		}
	}
	user := newConfig()
	var exclude string
	if xdg != "" {
		exclude = filepath.Join(xdg, "git", "ignore")
	}
	for _, name := range configs {
		data, err := readUserFile(name)
		if err != nil {
			return GlobalIgnore{}, err
		}
		c, err := parseConfig(data)
		if err != nil {
			return GlobalIgnore{}, fmt.Errorf("%s: %w", name, err)
		}
		if value, ok := c.get("core.excludesfile"); ok {
			exclude = value
		}
		user.merge(c)
	}
	g := GlobalIgnore{config: user, home: home}
	if exclude == "" || exclude == os.DevNull {
		return g, nil
	}
	exclude = expandHome(exclude, home)
	if !filepath.IsAbs(exclude) {
		g.relative = exclude
		return g, nil
	}
	// As in the work tree, an unreadable excludes file adds no rules.
	g.text, _ = readUserFile(exclude)
	return g, nil
}

// expandHome replaces a leading "~/" in a configured path by home.
// The rest is kept as written, as Git opens it.
func expandHome(name, home string) string {
	if rest, ok := strings.CutPrefix(name, "~/"); ok && home != "" {
		return strings.TrimSuffix(home, "/") + "/" + rest
	}
	return name
}

func readUserFile(name string) (string, error) {
	f, err := files.OpenRegular(name, true)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	defer func() { _ = f.Close() }()
	data, err := readBounded(f, maxIgnoreFile)
	return string(data), err
}
