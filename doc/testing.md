<!--
SPDX-FileCopyrightText: 2026 Philip Eklöf

SPDX-License-Identifier: MIT
-->

# Testing

```
make test                    # unit and end-to-end tests
make test TAGS=landlocktsync # the same, with go-landlock's TSYNC minimum
make vet                     # go vet
make fuzz                    # each fuzz target for FUZZTIME, default 10s
staticcheck ./...
```

Exercise both Landlock configurations with `make test` and
`make test TAGS=landlocktsync`. Tests that need Landlock skip on a kernel
without it. Use `go test -v` to see skips.

## Scan benchmarks

```
go test ./internal/scan -run '^$' -bench BenchmarkCompareContents -benchmem
BINATE_BENCH_LEFT=/path/to/left BINATE_BENCH_RIGHT=/path/to/right \
  go test ./internal/scan -run '^$' -bench BenchmarkScanTrees \
  -benchmem -benchtime=3x -count=3
```

The content benchmark covers empty, small and large files, equal or different in
the first block. It warms the comparer before measuring, so per-call allocations
exclude the initial buffer allocation.

The tree benchmark reads existing directories without changing them, comparing
contents with the default worker count. Relative paths are from `internal/scan`.
Repeated runs normally measure cached reads. Storage and cache state affect the
timings.

## Diff layout benchmark

```
go test ./internal/view -run '^$' -bench BenchmarkDiffContextLayout -benchmem
```

This measures rebuilding the layout of files with every row changed and context
covering the whole file, checking scaling as the row count grows.

## End-to-end tests

The tests of the main package start the test binary itself as binate
(`BINATE_TEST_AS_BINATE=1`), in a pseudo-terminal from `/dev/ptmx`, so the
parent, the child and Landlock run as they do for real. They need
pseudo-terminals and skip without them.

- **Under Landlock:** a session with a root given through a symlink: rescanning
  after a file was replaced by rename, and opening the diff view on it, plus
  `-v` reporting Landlock as enabled.
- **Process split:** quitting, `kill -9` of the child, and SIGTERM and SIGHUP
  sent to the parent all leave the terminal in its saved settings and off the
  alternate screen, with the expected exit status and the cursor on a fresh line
  for the next shell prompt.
- **Terminal:** Ctrl+z stops the parent and the child and leaves the terminal in
  its saved settings, and continuing the group takes the screen back. A terminal
  resized too small shows only a message. Quitting, with Ctrl+c answered by a
  second Ctrl+c, x, or Enter or Space after keys that move between the buttons,
  clears the last frame even when alternate-screen switches are ignored. Cancel,
  Esc and other keys close the quit prompt without reaching the tree, and
  neither q nor Esc quits.
- **Tree view:** marks and counts, the panels' full paths and their links,
  showing identical entries, difference jumps and Help, read from the screen as
  a terminal would show it (`ptytest.Term.Screen`).
- **Diff view:** two files with their paths in the panels, `/dev/null`, a pipe
  given as `/dev/fd/3`, a file and a directory, binary input in two-file mode
  and from the tree, and a file on one side only.
- **Descriptors:** inputs named by inherited descriptors, a regular file, a pipe
  and two directories as `/dev/fd/3` and `/dev/fd/4`, which keep their numbers
  in the child beside the socket and streams.
- **Colours:** warnings about `BINATE_COLORS` before the screen opens, escaped
  once, and no colour at all under `NO_COLOR`.
- **Editing:** a fake editor script, run by the parent, that changes a file,
  rewrites it by renaming a new file over it, and deletes it. The tree's marks
  and counts and the diff view's contents follow, in directory and two-file
  mode. The popup shows both paths, opens the one selected with Down and Enter,
  ignores other keys and closes on Esc and e. One editable side is edited at
  once with a note. Directories, `/dev/null` and a missing editor are refused
  with a reason in a red popup, which any key closes without reaching the tree.
- **Arguments:** usage errors, a missing terminal, `--help`, `-V` and
  `--version`, `-c` and `--compare` accepting the same attribute lists, help
  fitting 80 columns without wrapping, and VISUAL or EDITOR checked only where
  it is the one used.

## Package tests

- **display:** every C0 control, DEL, C1, invalid and truncated UTF-8, overlong
  forms, surrogates, zero-width and bidi characters, unusual spaces, combining
  marks, tabs at each column modulo 4, lone CR and a literal `\x1b`, plus
  quoting for the shell as bash's `${s@Q}` does, in printable ASCII alone, which
  bash reads back as the original string. Showing part of a line of contents,
  from a column or a wrapped part's start, gives the same glyphs as the whole
  line from there. Fuzzing checks that the output, shown or quoted, never
  contains a control, invalid UTF-8 or a character that is not printable, that
  the quoted form is ASCII, and that partial lines match whole ones.
- **term:** the key parser (arrows, Home/End, PgUp/PgDn, Alt+arrows in both
  encodings, Ctrl keys, UTF-8, sequences split across reads and the Esc
  timeout), and the flush (only changed cells, styles, wide characters, the
  cursor, and hyperlinks, ended before unlinked text and with their targets
  quoted for the shell so that a name can't end them). Fuzzing checks that the
  parser never panics and always uses up its input once the Esc timeout has
  passed.
- **scan:** temporary trees with every state, the order of entries, excludes,
  unreadable files and directories, both modes, files spanning several blocks, a
  FIFO on either side (the scan must not block), devices, parsing `--compare`
  lists, reclassifying a scanned tree with other attributes or quick mode
  without reading, ignoring entries on one side only or of different types with
  what is below them, and reporting contents never read, each attribute compared
  or not, on files, symlinks and directories, the marks for each kind of
  difference, directories marked by their own attributes alone, including after
  a re-check, cancelling, the path last read, and re-checking an entry after it
  is edited, replaced by rename, or deleted on one or both sides.
- **source:** reading through `source.Disk`: paths joined to the root, listing,
  symlinks followed only when asked, missing entries, and opening files, and the
  action cache keeping results only of files on disk, whose versions it can
  check.
- **git:** reading a repository as git reads it, compared, where git is
  installed, with what git itself reads in repositories made for each test:
  loose objects, in SHA-1 and SHA-256, checked for their header, size and zlib
  checksum, trees, with modes normalized and names git wouldn't check out
  refused, and commits and tags peeled to their trees; objects in packs, before
  loose ones, stored whole or as deltas at offsets or named, with large offsets,
  an index truncated while mapped and objects missing as errors; objects in
  alternates, searched depth first and each once. Fuzzing checks that parsing
  trees and loose objects' headers, and deltas, pack entries' headers and
  indexes never panics.
- **tree:** hiding identical entries, expansion of all directories or just the
  selected subtree, collapsing the selected subtree or the one holding a file,
  the selection returning once shown again, paging, difference jumps (stops,
  directories whose own attributes differ, wrapping, expanding parents), ignored
  entries hidden from the rows, the filter, jumps, search and counts, the filter
  and diffs-only mode, searching forward and back with wrapping, into collapsed
  directories but not hidden identical entries, the selection after a rescan,
  and counting.
- **app:** expanding the initial tree after the scan finishes, leaving
  directories collapsed while loading and preserving later collapses. Enter on a
  directory in the filter results reveals it without collapsing it. Edit tests
  cover the popup's selection and refreshing the tree's panels after editing in
  the diff view. Compare tests cover toggling attributes and contents at once,
  leaving quick mode for good, each row's letter selecting and toggling it but
  not with Alt, * turning all on and off, keys not reaching the tree, closing
  with Esc, and scanning again for contents never read. Error popups close on
  any key without passing it through, and Ctrl+c asks to quit. Expansion tests
  cover L and Shift+Right, and collapse through H and Shift+Left. Filter tests
  cover F opening the bar, Tab switching its mode, search bars ignoring Tab, and
  bash-style editing at the ends and in the middle of words. Search tests cover
  /, n and N, wrapping, matches in collapsed directories, skipping hidden
  identical entries, and Esc ending the search before the filter. Outside a
  search, n and N jump between differences, as Shift+Down and Shift+Up always
  do. Other key tests cover the quit prompt's buttons, Ctrl+c quitting at once
  on a terminal too small for it, gg and G, Help navigation and closing without
  passing the key through, and Esc clearing the filter. Both screens leave q,
  Space and Backspace unbound.
- **diff:** side-by-side alignment, line ends (CR+LF throughout, mixed, lone CR,
  no final line feed), the size limit and binary input, and in-line changes,
  including the length cap and escapes inside changes. Fuzzing checks that every
  line is shown once, in order, and that the unchanged parts of two lines are
  the same.
- **view:** the tree as drawn into the cell buffer, escaped names and their
  highlight in and out of the selection, filter results and the filter and
  search bars' labels, colour and cursor, the status bar naming the applied
  filter and search, the centered loading box and its progress until scanning
  finishes, smoothly increasing counts without overshooting, the spinner turning
  while the count is unchanged, digit grouping, the path being read shortened in
  the middle, padding and the white-on-blue box, the compact layout, rescan
  resets, path shortening, the panels' path row: home as ~, shortened in the
  middle, escaped, linked in full, and dimmed without a link on a side without
  the entry, the panels' marks between them and their colours, for each field,
  only where compared, a regular file's size alone, mtimes within a second shown
  with as much of the fraction as differs, and lined up past an error row, Help
  drawn like the loading box, its keys in columns with the vim-like key first
  and rules between the columns, its sections with their titles between lines
  joining the rules, the selected row and moving it past titles and lines, and
  the keys it lists, with Ctrl+c in both and Esc in the diff view's only when
  opened from a tree, the Edit popup drawn like Help without rules, its
  selection over the path alone, with escaped and shortened paths, the Compare
  popup's rows, their boxes and highlighted letters, the diff view's panels
  following the set, the red error popup with its text wrapped at spaces, at 80
  columns where the screen is wide enough, and escaped, and more than one text
  as a list, and the quit prompt as dialog(1) draws it, in full and compact,
  with its focused button and the x of Exit. The diff view as drawn: collapsed
  and full context, overlapping and separate context ranges, clipping at file
  boundaries, very large context values, in-line and escape highlighting,
  wrapping, scrolling sideways, change jumps, also from a block scrolled to by
  hand, and keeping the position when the texts are reloaded.
- **colors:** `BINATE_COLORS` with every key, 256-colour and truecolour values,
  unknown keys, values with other characters, empty values, and `NO_COLOR`.
- **landlock:** the restrictions themselves, and binate's policy, each in a
  re-executed test process that restricts itself and tries one thing. Under the
  policy, reading beneath a compared directory and reading a file compared on
  its own work, also when given through a symlink to another directory, and also
  after the file was replaced by renaming a new file over it (a test that fails
  with a rule on the file itself). Writing, creating, reading outside the
  inputs, listing a file's directory, running a program and signalling the
  parent are denied. ABI reporting tests cover the errata downgrade, the build's
  minimum, ABI 10 and 11, and the cap on newer kernels.
- **supervise:** the request protocol, and the parent's checks: `..` and
  symlinks out of the root, absolute paths, directories and FIFOs, a missing
  file, an invalid side, and a path sent in two-file mode. The editor starts in
  the checked directory even after it is replaced by a symlink, and a relative
  editor program is located from binate's original working directory. The
  fallback for systems without `openat2` is tested directly too.
- **args:** option and argument parsing, and help.
