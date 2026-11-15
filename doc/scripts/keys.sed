# SPDX-FileCopyrightText: 2026 Philip Eklöf
#
# SPDX-License-Identifier: MIT

# Post-processes binate(1) after man.sed (sed -E).

# One line per key: no blank lines or paragraph spacing between the
# entries under each heading of KEYS. Headings keep theirs.
/^\.SH KEYS$/,/^\.SH /{
	/^$/d
	/^\.SS /{
		i\
.PD
		a\
.PD 0
	}
	/^\.SH [^K]/i\
.PD
}
