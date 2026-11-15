# SPDX-FileCopyrightText: 2026 Philip Eklöf
#
# SPDX-License-Identifier: MIT

# Post-processes go-md2man's output (sed -E) so that what's typed, such as
# options, commands and dates, keeps ASCII hyphens in UTF-8 output, while
# hyphens of prose, such as "best-effort", stay breakable hyphens.

# Start from plain hyphens, as go-md2man escapes some versions' hyphens.
s/\\-/-/g

# Don't hyphenate, which would split what's typed, as options and paths, at
# line ends. go-md2man's .nh comes before .TH, which turns hyphenation back
# on. HY (groff 1.23) also keeps later macros, as of links, from doing so.
/^\.TH /{
	i\
.nr HY 0
	a\
.nh
}

# The separator of the NAME line, and hyphens in manual names.
s/^(binate(-[a-z]+)?) - /\1 \\- /
s/^(\.TH )?binate-/\1binate\\-/

# Code blocks are typed whole.
/^\.EX$/,/^\.EE$/{
	s/-/\\-/g
	b
}

# Bold and italic spans, up to the next font change, one hyphen at a time.
:span
s/(\\f[BI](([^\\]|\\[^f])*([^\\]|\\-))?)-/\1\\-/
t span

# A tilde, as for home, stays ASCII: groff draws a plain ~ as a raised one.
s/~/\\(ti/g

# A caret, as in revisions and patterns, stays ASCII too: groff draws a
# plain ^ as a modifier circumflex.
s/\^/\\(ha/g
