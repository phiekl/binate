// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import "testing"

func TestIgnoreNonASCII(t *testing.T) {
	root := &ignoreRules{patterns: parseIgnore("d\xe9j\xe0/*.l?g\n", false)}
	nested := &ignoreRules{prefix: "d\xe9j\xe0/\xff/", patterns: parseIgnore("[\xe0-\xef]?\n!\xe9x\nsub/*\xff\n", false), parent: root}
	for _, tc := range []struct {
		rel  string
		want bool
	}{
		{"d\xe9j\xe0/a.l\xffg", true},
		{"d\xe9j\xe0/\xff/\xe1z", true},
		{"d\xe9j\xe0/\xff/\xe9x", false},
		{"d\xe9j\xe0/\xff/\xf0z", false},
		{"d\xe9j\xe0/\xff/sub/a\xff", true},
		{"d\xe9j\xe0/\xff/sub/a\xfe", false},
		{"caf\xc3\xa9/\xe1z", false},
	} {
		if got := nested.ignores(tc.rel, false); got != tc.want {
			t.Errorf("ignores(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}
