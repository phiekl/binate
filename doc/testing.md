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
without it, and those that make repositories with git skip without git. Use
`go test -v` to see skips. The tests unset inherited `GIT_` variables first, as
a Git hook running them sets `GIT_DIR` and `GIT_INDEX_FILE`, which would point
git at another repository than a test's own.

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
pseudo-terminals and skip without them. Once the program exits, the terminal
passes on the rest of its output in the background, and `ptytest.Term.Exit`
waits for it.

- **Under Landlock:** a session with a root given through a symlink: rescanning
  after a file was replaced by rename, and opening the diff view on it, plus
  `-v` reporting Landlock and the seccomp filter as enabled, or the filter as
  unavailable on a kernel without seccomp filters.
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
  a terminal would show it (`ptytest.Term.Screen`). Two directories compared
  without `-g` show files that a `.gitignore`, a repository's exclude file and
  the user's global one would hide in a work tree, as `git check-ignore`
  confirms when git is installed.
- **Diff view:** opened from the tree, with the paths in the panels, binary
  input, and a file on one side only.
- **Inputs:** files refused, opposite a file or a directory, and `/dev/null`,
  given as it is or through a symlink, as an empty tree opposite a directory on
  either side and opposite a revision's tree, but not on both sides. A pipe
  given as `/dev/fd/3`, a FIFO without a writer and `/dev/zero` are refused
  without waiting. The combined archive size is known for regular files,
  including zero-length files. A pipe makes the total unknown.
- **Descriptors:** directories named by inherited descriptors as `/dev/fd/3`
  and `/dev/fd/4`, which keep their numbers in the child beside the socket.
- **Colours:** warnings about `BINATE_COLORS` before the screen opens, escaped
  once, and no colour at all under `NO_COLOR`.
- **Editing:** a fake editor script, run by the parent, that changes a file,
  rewrites it by renaming a new file over it, and deletes it. The tree's marks
  and counts and the diff view's contents follow, and a file deleted from the
  diff view says so in its box. The popup shows both paths, opens the one
  selected with Down and Enter, ignores other keys and closes on Esc and e. With
  one editable side it lists that side alone, says why the other can't be
  edited and ignores the other side's key. Directories, `/dev/null` and a
  missing editor are refused with a reason in a red popup, which any key closes
  without reaching the tree.
- **Arguments:** usage errors, a missing terminal, `--help`, `-V` and
  `--version`, `-c` and `--compare` accepting the same attribute lists, help
  fitting 80 columns without wrapping as either name, and VISUAL or EDITOR
  checked only where it is the one used. Sides in other repositories are told
  from revisions by their paths, split at the last `@` with a directory before
  it, as with `@` in the directory or the revision, from `-C` if relative, and
  `~` expanded after `git:`. Without `-g`, other names before a colon are
  unknown backends, and names that aren't backends' are paths.
- **Other repositories:** another repository's revision opposite the work
  tree, also with `GIT_DIR` set, which is ignored, two other repositories run
  from a directory in none, relative to `-C`, and of different object formats,
  and `git:` sides without `-g`, each with the header saying what each side is
  and linking to it. A revision against a directory on disk, whose files have
  the modes Git records for comparison, and the edit picker offers only the
  directory side. A work tree against a directory, both on disk, compares no
  times even when asked. Two work trees, the same index on both sides, also
  through a symlink, a directory in no repository, a revision naming a file and
  `-q` with a `git:` side are refused, and a `git:` side under `-g` fails with a
  hint to drop the prefix.

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
  unreadable files and directories, both modes, files spanning several blocks,
  a FIFO on either side (the scan must not block),
  devices, parsing `--compare` lists, reclassifying a scanned tree with other
  attributes or quick mode without reading, ignoring entries on one side only or
  of different types with what is below them, and reporting contents never read,
  each attribute compared or not, on files, symlinks and directories, the marks
  for each kind of difference, directories marked by their own attributes alone,
  including after a re-check, cancelling, the path last read, re-checking an
  entry after it is edited, replaced by rename or deleted on one or both
  sides, and statuses read
  for every entry of the side having them, left or right, with or without its
  entry, and again on a re-check, and entries recorded on neither side added,
  unmarked, except those excluded, and listed below a file that has some
  recorded below it.

  Timestamp tests cover exact comparison, archive precision of 100 nanoseconds,
  one microsecond, one second and two seconds, rounding in both directions and
  dates centuries apart. Two archive timestamps are compared exactly. Implicit
  archive directories have no attribute differences of their own.
- **source:** reading through `source.Disk`: paths joined to the root, listing,
  symlinks followed only when asked, missing entries, and opening files, and the
  action cache keeping results only of files on disk, whose versions it can
  check. Listing opens only a directory, not a FIFO, which isn't waited for,
  a device or a symlink put in its place, but for the root given through a
  symlink. A regular file is opened as it was checked, though a device took its
  path since. A disk side opposite git has the modes Git records, for each type,
  in what is compared and what the panels show.
- **git:** reading a repository as git reads it, compared, where git is
  installed, with what git itself reads in repositories made for each test:
  loose objects, in SHA-1 and SHA-256, checked for their header, size and zlib
  checksum, trees, with modes normalized and names git wouldn't check out
  refused, and commits and tags peeled to their trees; objects in packs, before
  loose ones, stored whole or as deltas at offsets or named, with large offsets,
  an index truncated while mapped and objects missing as errors; objects in
  alternates, searched depth first and each once; a tree as a side of the
  comparison, with git's modes and no times or owners, two commits compared, and
  a commit and its checkout differing in nothing git records; finding a
  repository from a directory up, bare or through a gitfile, whatever
  `GIT_DIR` says, its format and configuration, refusing reftable, and
  resolving revisions, ambiguous abbreviations included, and paths ending in a
  slash, `.` or `..` only as trees, as git rev-parse does. Fuzzing checks that
  parsing trees and loose objects' headers, deltas, pack entries' headers and
  indexes, and configuration and revisions never panics.
- **git work tree:** path visibility checked against Git across index versions
  and hash formats, nested and global ignore rules, tracked paths in ignored
  directories, unsupported indexes, rescans, and symlink confinement. Each
  path's status is checked against **git status --porcelain** in both hash
  formats, for staged and unstaged changes, deletions, type changes, untracked
  and intent-to-add files, a conflict, a file rewritten unchanged and a file in
  place of a tracked directory, a directory staged in place of a file, and a
  file changed as the index was written is read, as is one modified earlier in
  the second the index was written. Files unchanged since staged, and
  skip-worktree files, have their blob's ID, and changed and intent-to-add files
  have none, and a scan against HEAD reads the changed ones and finds them
  different. Files have no ID where core.autoCRLF or attributes that filter
  apply, from their directory or above it, tracked or not, info/attributes or
  outside the repository, which the system's configuration may name, as Git
  assigns them, with later lines, deeper files and info/attributes taking
  precedence, macros defined at the top only, quoted patterns, and filter
  drivers only where configured, but do where attributes only set diff drivers,
  are unset or unspecified, or match other paths. A file filtered as staged and
  modified earlier in the second the index was written is unchanged, as in Git,
  and is read once attributes no longer filter it, and a CRLF file committed as
  text differs from its LF blob, also once the attributes are removed, by its
  blob's size, and core.autoCRLF given as a key alone is on. A rescan finds HEAD
  again after a commit with packed refs, hashing stops once canceled, and
  against an older revision, entries staged for deletion or added and then
  deleted are found on neither side, and those of a directory a staged file
  replaced below the file, also once it is deleted. Terminal tests open the diff
  on a file whose staged and on-disk contents differ.
- **formats/archive:** Shared tests cover storing file contents sequentially
  in the temporary file and preserving populated directories, or all
  directories when `KeepDirectories` is enabled. `Puts` determines
  whether an entry can be placed before its contents are stored. Directories
  are listed and their links counted. A member beneath a symlink to a
  directory follows the link if the format permits it. Otherwise the member
  replaces the link with a directory.

  A hard link shares the target's contents and link count. Unreadable and
  directory targets produce an error, and a rejected hard link leaves the
  link count unchanged. When a target name is replaced, its remaining hard
  link counts only its own name. A symlink to a missing entry is left
  unresolved. Marked entries can appear at their own path or at a missing
  parent. They leave existing entries in place, give way to later entries
  and contribute no links.

  The mode returned by `Open` agrees with `Stat`, including in git mode.
  A regular archive file is read directly. A pipe is copied into the
  temporary file, and the copy's space can be freed after reading. Progress
  counts distinct bytes, including for overlapping or out-of-order reads.
- **tree:** hiding identical entries, expansion of all directories or just the
  selected subtree, collapsing the selected subtree or the one holding a file,
  the selection returning once shown again, paging, difference jumps (stops,
  directories whose own attributes differ, wrapping, expanding parents), ignored
  entries hidden from the rows, the filter, jumps, search and counts, the filter
  and diffs-only mode, searching forward and back with wrapping, into collapsed
  directories but not hidden identical entries, the selection after a rescan,
  counting, and entries with a git status shown though identical, with their
  directories and in diffs-only mode, and counted by status, those on neither
  side by status alone, and a one-sided file with entries below it as a file.
- **app:** expanding the initial tree after the scan finishes, leaving
  directories collapsed while loading and preserving later collapses. Enter on a
  directory in the filter results reveals it without collapsing it. Edit tests
  cover the popup's selection and refreshing the tree's panels after editing in
  the diff view. Compare tests cover toggling attributes and contents at once,
  leaving quick mode for good, each row's letter selecting and toggling it but
  not with Alt, * turning all off while any is on, and on when none is, keys not
  reaching the tree, closing with Esc, and scanning again for contents never
  read. Error popups close on any key without passing it through, and Ctrl+c
  asks to quit. Expansion tests cover L and Shift+Right, and collapse through H
  and Shift+Left. Filter tests cover F opening the bar, Tab switching its mode,
  search bars ignoring Tab, and bash-style editing at the ends and in the middle
  of words. Search tests cover /, n and N, wrapping, matches in collapsed
  directories, skipping hidden identical entries, and Esc ending the search
  before the filter. Outside a search, n and N jump between differences, as
  Shift+Down and Shift+Up always do. Other key tests cover the quit prompt's
  buttons, Ctrl+c quitting at once on a terminal too small for it, gg and G,
  Help navigation and closing without passing the key through, and Esc clearing
  the filter. Both screens leave q, Space and Backspace unbound. A commit's
  screen shows the header of the side whose commit it is above both boxes, and
  going back restores the previous header. The commit picker opens a shared
  commit directly and lists the available sides when a choice is needed.
  Attribute tests cover ignoring owners and times beside git and omitting
  modes or owners absent from an archive.

  The read screen shows loading progress, then opens the tree on success.
  On failure, it hides the loading box and shows a side-specific error.
  Any key, including Ctrl+c, closes the popup and ends the loop with the
  error. If the terminal is already too small or shrinks below the minimum,
  the loop ends immediately.
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
  resets and path shortening. The archive loading box shows the amount read
  in binary units and, for a known total, the size and a percentage bar
  filled in eighths of a cell.

  The panels' path row shows home as `~`, shortens long paths in the middle
  and links the full escaped path. A missing entry is dimmed and has no link.
  Difference marks and their colours apply to fields being compared. Owner
  marks use numeric IDs, and size marks
  apply to regular files. Mtimes display the fractional digits needed to
  show a difference. Tests cover the archive timestamp precision rules,
  exact comparisons between archives and the absence of mode, owner and
  mtime for implicit directories. The panels stay aligned across error rows.
  Help is drawn like the loading box, its keys in columns with the vim-like
  key first and rules between
  the columns, its sections with their titles between lines joining the rules,
  the selected row and moving it past titles and lines, and the keys it lists,
  with Ctrl+c in both and Esc in the diff view's only when opened from a tree,
  and the letters of a git status only with statuses, git statuses before the
  types on either side in git's colours, said in words in an empty cell on
  either side, once for an untracked directory, entries on neither side dimmed,
  a deleted file kept untracked only by a file in its place, and in the panel's
  git row, the Edit popup drawn like Help without rules, its selection over the
  path alone, with escaped and shortened paths, the Compare popup's rows, their
  boxes and highlighted letters, and attributes not recorded in git revisions or
  in the archive, the diff view's panels following the set, the red error popup
  with its text wrapped at spaces, at 80 columns where the screen is wide
  enough, and escaped, and more than one text as a list, and the quit prompt as
  dialog(1) draws it, in full and compact, with its focused button and the x of
  Exit. The diff view as drawn: collapsed and full context, overlapping and
  separate context ranges, clipping at file boundaries, very large context
  values, in-line and escape highlighting, wrapping, scrolling sideways, change
  jumps, also from a block scrolled to by hand, and keeping the position when
  the texts are reloaded.
- **colors:** `BINATE_COLORS` with every key, 256-colour and truecolour values,
  unknown keys, values with other characters, empty values, and `NO_COLOR`.
- **landlock:** the restrictions themselves, and binate's policy, each in a
  re-executed test process that restricts itself and tries one thing. Under the
  policy, listing and reading beneath a compared directory work, also when it
  is given through a symlink to another directory. Writing, creating, reading
  outside the inputs, running a program and signalling the parent are
  denied. ABI reporting tests cover the errata downgrade, the build's
  minimum, ABI 10 and 11, and the cap on newer kernels.
- **seccomp:** the filter, in a re-executed test process that installs it,
  refusing TIOCSTI and TIOCLINUX, also with bits set above the request's 32 and
  on a thread started before it, while other requests still reach the kernel,
  and setting no_new_privs. The program is also run by a small BPF interpreter,
  for what a live process can't try, as syscalls of i386 or x32, x32's numbers
  without its bit among them, and the request's half on a big-endian machine.
- **supervise:** the request protocol, and the parent's checks: `..` and
  symlinks out of the root, absolute paths, directories and FIFOs, a missing
  file, an invalid side, a request without a path, and a side that is no
  directory, as `/dev/null`. The editor starts in
  the checked directory even after it is replaced by a symlink, and a relative
  editor program is located from binate's original working directory. The
  fallback for systems without `openat2` is tested directly too.
- **args:** option and argument parsing, and help.
