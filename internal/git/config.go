// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"fmt"
	"strconv"
	"strings"
)

// config is a git config file's variables, by their full lowercased name,
// as "core.repositoryformatversion", or with a subsection, as
// "remote.Origin.url": the section and key lowercased, the subsection as
// written. Each has the values given, in order; a key alone has the value
// "".
type config struct {
	values map[string][]string
	// bare holds the names whose last value is a key alone, which git
	// reads as true, unlike an empty value.
	bare map[string]bool
}

func newConfig() config {
	return config{values: map[string][]string{}, bare: map[string]bool{}}
}

func (c config) add(name, value string, bare bool) {
	c.values[name] = append(c.values[name], value)
	c.bare[name] = bare
}

// get returns the last value of name, as git takes it.
func (c config) get(name string) (string, bool) {
	v := c.values[name]
	if len(v) == 0 {
		return "", false
	}
	return v[len(v)-1], true
}

// merge appends the values of later, which take precedence.
func (c config) merge(later config) {
	for name, values := range later.values {
		c.values[name] = append(c.values[name], values...)
		c.bare[name] = later.bare[name]
	}
}

// bool returns the boolean name, or def if unset, as git reads one: a key
// alone is true, and an empty value false.
func (c config) bool(name string, def bool) (bool, error) {
	v, ok := c.get(name)
	if !ok {
		return def, nil
	}
	if c.bare[name] {
		return true, nil
	}
	switch strings.ToLower(v) {
	case "true", "yes", "on":
		return true, nil
	case "", "false", "no", "off":
		return false, nil
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n != 0, nil
	}
	return false, fmt.Errorf("bad boolean config value %q for %s", v, name)
}

// configError is the error of a config file that isn't well formed.
type configError struct {
	line int
	msg  string
}

func (e *configError) Error() string {
	return fmt.Sprintf("bad config line %d: %s", e.line, e.msg)
}

// parseConfig parses a config file as git does, with its sections,
// subsections, quoting, escapes, comments and continued lines. Includes
// are not followed: git doesn't either for what is read here, the
// repository's format.
func parseConfig(data string) (config, error) {
	p := &configParser{data: data, line: 1}
	c := newConfig()
	section := ""
	// A UTF-8 byte order mark is skipped, as git does.
	p.data = strings.TrimPrefix(p.data, "\ufeff")
	for {
		p.skipSpace()
		if p.done() {
			return c, nil
		}
		switch ch := p.peek(); {
		case ch == '\n':
			p.next()
		case ch == '#' || ch == ';':
			p.skipLine()
		case ch == '[':
			s, err := p.section()
			if err != nil {
				return config{}, err
			}
			section = s
		case isAlpha(ch):
			if section == "" {
				return config{}, p.errorf("variable outside a section")
			}
			key, value, bare, err := p.variable()
			if err != nil {
				return config{}, err
			}
			c.add(section+"."+key, value, bare)
		default:
			return config{}, p.errorf("unexpected %q", ch)
		}
	}
}

type configParser struct {
	data string
	pos  int
	line int
}

func (p *configParser) done() bool { return p.pos >= len(p.data) }
func (p *configParser) peek() byte { return p.data[p.pos] }

func (p *configParser) next() byte {
	c := p.data[p.pos]
	p.pos++
	if c == '\n' {
		p.line++
	}
	return c
}

func (p *configParser) errorf(format string, args ...any) error {
	return &configError{line: p.line, msg: fmt.Sprintf(format, args...)}
}

// skipSpace skips spaces and tabs, not newlines.
func (p *configParser) skipSpace() {
	for !p.done() && (p.peek() == ' ' || p.peek() == '\t' || p.peek() == '\r') {
		p.pos++
	}
}

func (p *configParser) skipLine() {
	for !p.done() && p.next() != '\n' {
	}
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isAlnum(c byte) bool { return isAlpha(c) || c >= '0' && c <= '9' }

// section parses a section header: "[name]", "[name "sub"]", or the old
// "[name.sub]", whose subsection is lowercased.
func (p *configParser) section() (string, error) {
	p.next() // [
	var name strings.Builder
	for {
		if p.done() || p.peek() == '\n' {
			return "", p.errorf("section header not ended")
		}
		c := p.next()
		switch {
		case c == ']':
			if name.Len() == 0 {
				return "", p.errorf("empty section name")
			}
			return strings.ToLower(name.String()), nil
		case c == ' ' || c == '\t':
			p.skipSpace()
			if p.done() || p.peek() != '"' {
				return "", p.errorf("invalid section header")
			}
			p.next()
			sub, err := p.subsection()
			if err != nil {
				return "", err
			}
			if p.done() || p.next() != ']' {
				return "", p.errorf("invalid section header")
			}
			if name.Len() == 0 {
				return "", p.errorf("empty section name")
			}
			return strings.ToLower(name.String()) + "." + sub, nil
		case isAlnum(c) || c == '-' || c == '.':
			name.WriteByte(c)
		default:
			return "", p.errorf("invalid section name")
		}
	}
}

// subsection parses a quoted subsection, its opening quote read: a
// backslash takes the next character as it is.
func (p *configParser) subsection() (string, error) {
	var sub strings.Builder
	for {
		if p.done() || p.peek() == '\n' {
			return "", p.errorf("subsection not ended")
		}
		c := p.next()
		switch c {
		case '"':
			return sub.String(), nil
		case '\\':
			if p.done() || p.peek() == '\n' {
				return "", p.errorf("subsection not ended")
			}
			c = p.next()
		}
		sub.WriteByte(c)
	}
}

// variable parses "key = value", or "key" alone, and reports which.
func (p *configParser) variable() (string, string, bool, error) {
	start := p.pos
	for !p.done() && (isAlnum(p.peek()) || p.peek() == '-') {
		p.pos++
	}
	key := strings.ToLower(p.data[start:p.pos])
	p.skipSpace()
	if p.done() || p.peek() == '\n' || p.peek() == '#' || p.peek() == ';' {
		p.skipLine()
		return key, "", true, nil
	}
	if p.next() != '=' {
		return "", "", false, p.errorf("invalid key %q", key)
	}
	value, err := p.value()
	return key, value, false, err
}

// value parses a value up to the line's end: spaces around it and a
// comment after it are dropped, quoting keeps them, and a backslash at a
// line's end continues it on the next.
func (p *configParser) value() (string, error) {
	var v strings.Builder
	quoted := false
	// pending are spaces seen outside quotes after some of the value,
	// kept only if more follows.
	pending := 0
	for {
		if p.done() {
			break
		}
		c := p.next()
		if c == '\n' {
			if quoted {
				return "", p.errorf("quoted value not ended")
			}
			break
		}
		if !quoted && (c == ' ' || c == '\t' || c == '\r') {
			if v.Len() > 0 {
				pending++
			}
			continue
		}
		if !quoted && (c == '#' || c == ';') {
			p.skipLine()
			break
		}
		if pending > 0 {
			v.WriteString(strings.Repeat(" ", pending))
			pending = 0
		}
		switch c {
		case '"':
			quoted = !quoted
		case '\\':
			if p.done() {
				return "", p.errorf("value ends in a backslash")
			}
			switch e := p.next(); e {
			case '\n':
				// Continued on the next line.
			case 'n':
				v.WriteByte('\n')
			case 't':
				v.WriteByte('\t')
			case 'b':
				s := v.String()
				if s != "" {
					v.Reset()
					v.WriteString(s[:len(s)-1])
				}
			case '\\', '"':
				v.WriteByte(e)
			default:
				return "", p.errorf("invalid escape \\%c", e)
			}
		default:
			v.WriteByte(c)
		}
	}
	return v.String(), nil
}
