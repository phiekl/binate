<!--
SPDX-FileCopyrightText: 2026 Philip Eklöf

SPDX-License-Identifier: MIT
-->

# Contributing

## Conventions

- Use established dependencies where possible. Copy code only when no
  suitable dependency exists, as with Go's `internal/diff`, and keep its
  copyright notices and license.
- Return errors for expected failures. A panic means there is a bug.
- Pass loggers explicitly, through arguments or option structs. Do not use
  a global logger.
- Always show warnings. Use `-v`, `-vv` and `-vvv` only to control the
  level of debug output.
- Show text from the file system (names, paths, file contents, error
  messages that contain paths) only through `internal/display`, on the
  screen and on stderr alike. Never write it to the terminal any other way.
- Keep the child restricted: anything it needs outside the compared
  directories and files must be opened before it calls `Restrict`. Work
  that needs more, such as running the editor, belongs in the parent and
  goes through the request protocol in `internal/supervise`.
- Word messages on the screen, in the status bar and popups, without
  joining their parts with colons or semicolons. A system error follows
  in parentheses. Messages on stderr follow the usual `error: what: why`
  form of command-line tools instead. Say "unable to"
  for what isn't possible and "failed" for what went wrong trying, in
  popups' titles too. A popup's text is a sentence, capitalized and ending
  in a full stop, without raw paths from the operating system.
- Format code with gofmt and fix issues reported by `go vet` and
  staticcheck.
- Run `make test` and `make test TAGS=landlocktsync` to test both Landlock
  configurations. See [testing.md](testing.md) for details.

## Commits

A short imperative title of at most 72 characters saying what changes for
the user, then a body of one to three sentences, wrapped at 72 columns, on
the new behaviour and the problem it avoids. Leave the details to the
documentation: end with a `See doc/<file>.md#<section>` line and a
`Signed-off-by:` line (`git commit -s`). The referenced documentation is
updated in the same commit, with anything worth keeping that would
otherwise be only in the message. One change per commit.
