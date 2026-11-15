<!--
SPDX-FileCopyrightText: 2026 Philip Eklöf

SPDX-License-Identifier: MIT
-->

# binate manual

Full reference for the binate file and directory difference viewer.

```
binate [options] left right
```

**binate** shows two directory trees, or two files, side by side, and marks
where they differ. It never changes anything itself: the only way a file changes
is through your own editor, which **e** opens.

It is Linux-only and needs a terminal: if stdin or stdout is not a terminal, it
exits with an error before drawing anything. The man page,
[**binate**(1)](binate.1.md), is a short summary of the options, marks and keys.

- [Inputs](#inputs)
- [Options](#options)
- [Comparing](#comparing)
- [Tree view](#tree-view)
- [Diff view](#diff-view)
- [Editing](#editing)
- [Keys](#keys)
- [Showing text safely](#showing-text-safely)
- [Environment](#environment)
- [Exit status](#exit-status)
- [Process structure](#process-structure)
- [Landlock](#landlock)
- [Caveats](#caveats)

## Inputs

*left* and *right* accept directories, files, or a mixture. When just one
argument is a directory, **binate** looks there for an entry matching the other
argument's basename, following **diff**(1)'s convention. Symlinks given on the
command line are followed. **/dev/null** supplies no contents. Pipes, including
process substitutions such as **<(**_command_**)**, and other special files
supply a stream read once at startup. These inputs allow use with
**git difftool --no-prompt -x binate**. While waiting for a pipe to end, the
diff view displays a reading message and accepts only **Ctrl+c** and **Ctrl+z**.

An input may be named by a file descriptor **binate** inherits, as **/dev/fd/3**
given with **3<**_file_, and is then whatever that descriptor is open on, a
directory, a file or a pipe: descriptors keep their numbers in the child, and
those **binate** passes it besides take free ones.

## Options

Options may come before, between or after *left* and *right*. After **--**, the
next arguments are *left* and *right* even if they start with a dash.

- **-c**, **--compare** *list*: Compare what *list* names, comma-separated. The
  default is **left,right,type,content,target,device,mode,owner**. Prefixing
  every item in *list* with **+** or **-** adds it to the default or takes it
  away instead of replacing the default: **--compare +mtime** compares
  everything, and **--compare -owner,+mtime** all but the owner. A list without
  signs replaces the default whole, so **--compare mode** also leaves out the
  entries on one side only, and **--compare none** compares nothing, showing
  only entries of the same type on both sides, all identical. **c** changes it
  while **binate** runs (see [Compare](#compare)). This option also selects
  which differences the panels highlight. The diff view always compares file
  contents. See [Comparing](#comparing). The items are:

  - **left**, **right**: entries on the left, or the right, side only (**<**,
    **>**). Left out, they are hidden, with everything below them.
  - **type**: entries of different types on the two sides (**!**). Left out,
    they are hidden, with everything below them.
  - **content**: a regular file's size and contents.
  - **target**: a symlink's target.
  - **device**: a device's major and minor number.
  - **mode**: the permission bits, with setuid, setgid and sticky.
  - **owner**: the user and group, by number.
  - **mtime**: the modification time, to the nanosecond.

- **-C**, **--context** *n*: Show *n* lines of context around changes in the
  collapsed diff view (default 3). Must not be negative.

- **-x**, **--exclude** *pattern*: Skip entries whose name matches *pattern*, in
  the syntax of Go's **path.Match**: **\***, **?**, **[**...**]**, and a
  backslash to quote. The pattern is matched against each entry's name, not its
  path. Repeatable.

- **-q**, **--quick**: In the tree view, compare files by size and modification
  time instead of by contents.

- **-h**, **--help**: Show the options and exit.

- **-v**, **--verbose**: Write debug messages to stderr, including whether
  Landlock is active and at which ABI, and whether the terminal is taken to show
  truecolour (see [BINATE_COLORS](#environment)). Repeat for more (**-vv**,
  **-vvv**).

- **-V**, **--version**: Show the version and exit.

## Comparing

**binate** walks both directory trees at once in the background, pairs entries
by name, and compares each pair. A loading box shows progress until the scan
finishes, when the tree rows and panels appear. Quitting and suspending remain
available while scanning.

By default it compares regular files by their contents, reading both in large
blocks and stopping at the first difference. Files of different sizes differ
without being read. With **-q**, it compares size and modification time instead:
the same size with a different time can't be told apart without reading, so it's
marked unverified. While **binate** runs, **c** can leave quick mode, to read
the contents, or stop comparing them (see [Compare](#compare)).

During a directory scan, only regular files are opened for content comparison.
FIFOs, sockets and devices are compared by type without being opened, since
opening a FIFO can block forever and reading a device can have side effects: two
FIFOs, or two sockets, are the same, and two devices are the same when their
device numbers match. Symlinks within the trees are compared by their stored
targets without being followed. A directory is compared by its entries.

Every type of entry, directories included, is also compared by its mode and
owner, and with **--compare**, by its modification time. Each one that differs
makes the pair differ, even when its contents could not be read. **--compare**
leaves out any of these comparisons, or the contents, targets or device numbers:
an entry whose remaining attributes are equal is the same. A pair present on one
side only, or of different types, differs, unless **--compare** leaves out
**left**, **right** or **type**: then it is hidden, as if excluded, with
everything below it, until they are compared again. With **content** left out,
files are never read, and **-q** has no effect.

Entries are listed directories first, then everything else, each sorted by name
byte for byte, independent of the locale.

A fixed number of workers, twice the number of CPUs, read and compare files.
Each worker opens at most two files at a time. An entry that can't be read
records the error and is marked unverified. The scan goes on. If a directory
can't be listed on one side, the entries found on the other side are unverified,
not one-sided: they may be there too. If a root can't be listed and there is
nothing to show, its error is shown in its box.

### Marks

The column between the two sides marks each pair, in the style of **sdiff**(1):

- **=**: No difference found.

- **|**: The data differs: the size or contents of a file, the target of a
  symlink, or the number of a device.

- **~**: The attributes differ: the mode or owner.

- **#**: The modification time differs, if compared (see **--compare**).

- **\***: More than one of the above differs.

- **?**: Not verified: a side could not be read, or, in quick mode, the size is
  the same but the modification time is not.

- **<**, **>**: Present on the left, or the right, side only.

- **!**: Different entry types, such as a file opposite a directory or a
  symlink.

A difference is only found in what **--compare** compares. A directory present
on both sides is marked by its own mode, owner and modification time alone:
**~**, **#** or **\*** if they differ, **?** if a side could not be listed, and
**=** otherwise, even when entries below it differ, or their number does.
Diffs-only mode keeps it all the same when something below it differs. Contents
that could not be read don't count: a file whose mode differs and whose contents
could not be read is marked **~**.

A pair not compared yet has no mark. Each mark but **=** and **\*** has a style
of its own (see [BINATE_COLORS](#environment)). **\*** takes that of the most
significant difference it stands for: data, then attributes, then modification
time. In the tree view, names take their mark's colour, never bold. Identical
entries and entries not yet compared use the normal text colour, directory names
included. A difference with a file or symlink on either side shows its name and
mark in the bright version of the mark's ANSI colour, which drops its bold. A
directory's mark and the **dir** type are not bold either. Other marks and types
keep their styles, so by default the **link**, **char**, **bdev** and **sock**
types, and the **file** type of an executable, are bold, as are **#** and a
**!** between two types that are neither files, symlinks nor directories. Rows
use the terminal's background. The selected row keeps its highlight.

## Tree view

The tree view pairs entries by name, with a row for each pair. An entry found in
only one tree has a blank cell opposite it. A box surrounds each tree, with its
root path in the title: home becomes **~**, and long paths lose their middle to
fit. In each row, the type precedes the escaped name (see [Showing text
safely](#showing-text-safely)), as **dir │ ▾ src/**. The types are **file**,
**dir**, **link** (symlink), **pipe** (FIFO), **char** and **bdev** (character
and block device), **sock** (socket) and **?** (one that could not be read), in
the colours **ls** gives them by default, an executable file in green. A
directory has a **▸** or **▾** before its name and a **/** after it. A symlink
shows its target after **->**. The mark (see [Marks](#marks)) is shown once per
row, in a column of its own between the two boxes. The bottom border of each box
shows the selected row and the number of rows shown, as **3/12**.

Once the initial scan finishes, all directories expand, including one-sided
ones. Identical entries start hidden. Directories with differences below them
stay shown. **\*** shows or hides them.

The bottom of each box is a panel describing the selected entry on that side,
read again after a rescan or an edit (see [Panels](#panels)). On a tall enough
terminal, a preview above the panels shows the first changes of a pair of files
whose contents differ, or of a file on one side only (see [Preview](#preview)).
The status bar counts the pairs of the whole tree, each count in brackets and in
the colour of its mark, leaving out those that are zero, as **[23523 items
total] [23500 identical] [3 left] [5 right] [5 type] [4 contents] [2 attrs] [30
mtime]**, then **unverified** and **pending**. The total counts each pair once,
except directories present on both sides and nonempty one-sided directories,
whose contents are counted instead. Each kind of difference counts the pairs
that differ in it, so a pair that differs in more than one counts under each,
and a directory on both sides counts under its own attributes. While a scan
runs, a centered white-on-blue box replaces the status bar. Beside a spinner, it
shows the number of entries read so far, the elapsed time, and the directory or
file being read, relative to the roots. Entries are counted as they are read.
The total is unknown until the scan finishes, so no percentage is shown. Short
terminals leave out the path. Tree rows and entry panels are drawn once the scan
finishes, when the box disappears and the status bar returns. Rescanning shows
the box again and keeps the selection and expansion by path.

- **j**, **Down** / **k**, **Up**: Move the selection down or up.

- **PgDn**, **Ctrl+f** / **PgUp**, **Ctrl+b**: Move the selection by a screen.

- **Home**, **gg** / **End**, **G**: Move to the first or last row.

- **l**, **Right**: Expand the selected directory.

- **L** (Shift+l), **Shift+Right**: Expand the selected directory and every
  directory below it, including one-sided ones, even if the selected directory
  is already expanded. Other directories keep their expansion. Unavailable while
  a filter is applied.

- **h**, **Left**: Collapse the selected directory, or if it is collapsed or not
  a directory, select its parent.

- **H** (Shift+h), **Shift+Left**: Collapse the selected directory and every
  directory below it, so expanding it again shows one level. On a file, do so
  for the directory holding it and select that directory. Other directories keep
  their expansion. Unavailable while a filter is applied.

- **+**, **=** / **-**: Expand every directory, one-sided ones included, or
  collapse every directory. Unavailable while a filter is applied.

- **\***: Show or hide identical entries. A hidden selection moves to its
  nearest shown parent, and returns when shown again unless the selection moved.

- **n**, **Shift+Down** / **N**, **Shift+Up**: Select the next or previous
  difference in tree order, expanding the directories above it, and wrapping
  around at the end. Unverified entries are not stops, and a directory on both
  sides is one only if its own attributes differ. Without a filter, a one-sided
  directory, or one opposite another type, is one stop, not one per entry in it.
  While a filter is applied, only the rows it lists are stops. While searching,
  **n** and **N** go between matches instead (see [Search](#search)).

- **Enter**: Open the diff view on a pair of files, or on a file present on one
  side only, against an empty side. Expand or collapse a directory. On a filter
  result, show it in the tree instead. Symlinks and special files are not
  opened.

- **F**: Filter by name (see [Filter](#filter)).

- **/**: Search the names (see [Search](#search)).

- **e**: Edit the selected entry (see [Editing](#editing)).

- **c**: Choose what to compare (see [Compare](#compare)).

- **a**: Run an action on the selected entry, as a checksum (see
  [Actions](#actions)).

- **r**: Scan again, comparing what is chosen. A running scan is cancelled.

- **Esc**: Close an action's result, or else end a search, or else clear an
  applied filter.

### Panels

Below a line across each box, a panel shows the entry on that side, one field
per row:

- **path**: the full path, from the root made absolute, with home as **~**,
  shortened in the middle when long, and escaped. It is a hyperlink (OSC 8) to
  the full path, for terminals that support them, quoted for the shell (see
  [Showing text safely](#showing-text-safely)) rather than as a **file://** URL,
  as **'/home/al/it'\''s here'**, to paste into a command line. A side without
  the entry has its path dimmed, and no link.
- **type**: as in the tree, with a symlink's target after **→** and a device's
  major and minor number.
- **size**: in bytes, from 1 KiB also in binary units, as **4300 (4.2 KiB)**.
  For a directory, the number of entries the scan found.
- **mode**: the permission bits in octal, with setuid, setgid and sticky.
- **owner**: user and group, by name where **/etc/passwd** and **/etc/group**
  name them, then by number.
- **links**, **inode**: the number of hard links, and the inode number.
- **mtime**, **ctime**, **atime**, **btime**: the times of the last
  modification, status change and access, and of creation, in local time to the
  second, or **—** where the file system doesn't record one.

Fields that differ between two sides that could be read are marked between the
panels with the tree's marks, and their values take the mark's colour on both
sides: **|** for a regular file's size, or a symlink's target or a device's
number in the type, **~** for the mode and owner, **#** for the mtime, and **!**
for one type opposite another, which then leaves the size unmarked. A
directory's number of entries, and a symlink's size, which is its target's
length, are not marked. A difference in entry type is always marked. Other
fields are marked only when chosen for comparison (see **--compare** and
[Compare](#compare)). The diff view's panels follow the tree's comparison
settings, or with two files, **--compare**. The mtime is compared to the
nanosecond, as in the tree: two that differ within the same second show as much
of the fraction as tells them apart, as **14:10:00.4** and **14:10:00.5**. The
more recent one is marked **(newer)**. The inode and the other times differ
between almost any two trees, and are not marked. A side with no entry says
**missing**, and one that can't be read says why. An entry the scan could not
read fully starts with an **error** row, as **error │ permission denied**, and
the other side's rows move down with it, so the fields stay side by side.

The panels are read in the background, without following symlinks, so a slow
file system doesn't hold the screen. They show **…** meanwhile. A box keeps at
least 5 rows for the tree above its panel, which grows to 11 rows on a tall
enough terminal. On a shorter one there is no panel, and the status bar shows
what could not be read of the selected entry instead, as **failed reading the
left side (permission denied)**.

### Preview

On a tall enough terminal, the tree gives some of its rows to a preview, between
lines above the panels. When the selected entry is a pair of files whose
contents differ, or a file on one side only, it shows the entry's first changes,
as the diff view shows them, with one unchanged line around each change and the
unchanged lines before the first left out. It doesn't scroll: when more changes
are left out, its last row says **more changes, Enter shows all** on both sides,
or **Enter shows all** in narrower panes, as Enter opens the diff view. A side
that can't be shown says why in its pane, as the diff view does (see [Diff
view](#diff-view)). For any other entry it says why, dimmed and centered between
dots on both sides, as **··· contents identical ···**, **··· contents not
verified ···** or **··· directories have no preview ···**: its rows stay, so the
tree keeps its height as the selection moves. The preview takes a third of the
tree's rows, from 3 to 8, and only while the tree keeps at least 10, so a
terminal needs about 31 rows for it. Like the panels, the files are read in the
background, showing **…** meanwhile, and again after a rescan or an edit.

### Compare

**c** opens a popup, in the style of the Edit popup, listing what the tree
compares, **--compare** at first:

    [x]  left      entries on the left only (<)
    [x]  right     entries on the right only (>)
    [x]  type      entries of different types (!)
    [x]  content   files' size and contents
    [x]  target    symlinks' targets
    [x]  device    devices' numbers
    [x]  mode      permission bits
    [x]  owner     user and group
    [ ]  mtime     modification time

**j**, **k** and the other keys that move through Help move the selection.
**Space** or **Enter** changes the selected row. Each row also has a letter of
its own, highlighted in its name, that selects and changes it at once: **l**
left, **r** right, **y** type, **c** content, **a** target, **d** device, **m**
mode, **o** owner and **t** mtime. **\*** turns them all on, or all off if they
are. **Esc** closes the popup. Other keys are ignored. Started with **-q**, the
**content** row shows **[q]**. Changing it leaves quick mode for good, reading
the contents in full, as quick mode only saves reading at startup.

Each change applies at once: the scan keeps what it read of each entry, its
size, mode, owner, times, a symlink's target, a device's number, and the result
of comparing a pair of files' contents, so the tree is compared again without
reading anything, and the marks, colours, panels, counts, diffs-only mode and
difference jumps follow. Only contents the scan never read, as when it started
without them or in quick mode, need a new scan, which starts by itself, keeping
the expanded directories and the selection. A change while a scan runs also
starts it again. The status bar says why a new scan started. **r** scans again
with what is chosen.

### Actions

**a** opens a popup, in the style of the Compare popup, listing actions to run
on the selected entry, each with its key:

    2  SHA-256  checksum of each side
    3  SHA-384  checksum of each side
    5  SHA-512  checksum of each side

**j**, **k** and the other keys that move through Help move the selection, and
**Enter** runs the selected action, as does its key at once. **Esc** closes the
popup. An action that can't run on the entry, as a checksum of a directory, is
dimmed, and choosing it closes the popup and says why in the status bar, as
**SHA-256 needs a regular file on a side**.

An action's result replaces the preview, below a divider titled with the
action's name, or, on a terminal too short for the preview, the panels. On one
too short for either, the status bar says so, as
**SHA-256 needs a taller terminal**. A checksum is shown in hexadecimal, broken
into lines of the pane's width, and a side whose file is missing or not a
regular file says so instead. Reading shows **reading… 45%**, in the background,
so the tree stays usable. Once both sides are done, the mark between them is
**=** if the sums are the same, and **|** if they differ. A file that can't be
read says why, in red. Files are opened as the scan opens them: a symlink is not
followed, and only a regular file is read.

Each sum is a hyperlink (OSC 8), for terminals that support them, whose target
is the line **sha256sum**(1), **sha384sum**(1) or **sha512sum**(1) prints for
the file, with its path from its side's root, as **9f86d081… a/b/file**. Copying
the link gives a line to paste into a **SHA256SUMS** file, or to check with
**sha256sum -c** in either root. As GNU coreutils does, a name holding a
backslash, newline or carriage return has them escaped as **\\**, **\n** and
**\r**, and the line starts with a backslash. A name that is not valid UTF-8, or
holds another control character, as a tab, gets no link (see [Showing text
safely](#showing-text-safely)).

The result stays until **Esc**, until the selection moves, or until the entry
may have changed, as after an edit or a rescan. Each of these stops a checksum
still being read.

A finished checksum is kept, in memory, until **binate** exits, and shown at
once when asked for again while the file is unchanged: the same device, inode,
size, mtime and ctime, to the nanosecond. Any write changes the ctime, which
can't be set back, so a changed file is read again. Since file systems record
times at a clock tick's resolution, a file that changed less than 2 seconds
before it was read is not kept, as a same-sized rewrite within the same tick
would keep its times. Neither is a sum of a file that changed while it was read,
or of one that was not the file found at the path, as when a directory above it
was swapped while it was opened, nor one that failed or was stopped. Up to
10,000 sums are kept, dropping the least recently used.

### Filter

**F** opens the filter bar at the bottom, white on blue and starting with
**Filter (all, regex):** or, in diffs-only mode, **Filter (diffs, regex):**. The
rows it matches are listed flat, each as **parent › name**: every entry of the
whole tree whose name matches the query, ignoring case, including hidden
identical entries. In diffs-only mode, entries found identical are left out,
while unverified entries remain shown. An empty query lists every remaining
entry. While a filter is applied, the status bar names it, as **[filter (diffs):
name]**.

The query is a regular expression in Go's RE2 syntax, matched anywhere in the
name unless anchored with **^** or **$**. **Ctrl+r** switches to literal text,
shown as **literal** in place of **regex**, and back. While the expression is
invalid the label says **invalid regex**, and **Enter** does nothing. The bar
opens in the mode last applied. In the status bar, only literal text is marked,
as **[filter (all, literal): name]**.

The filter bar captures typing: printable keys add to the query, including those
that normally control the tree.

- **Left**, **Ctrl+b** / **Right**, **Ctrl+f**: Move the cursor by a character.

- **Home**, **Ctrl+a** / **End**, **Ctrl+e**: Move the cursor to the start or
  the end.

- **Alt+b** / **Alt+f**: Move the cursor to the start of the word, or of the one
  before, or to the end of the word, or of the next one. As in bash, a word is a
  run of letters and digits.

- **Backspace** / **Delete**, **Ctrl+d**: Delete the character before or at the
  cursor.

- **Alt+Backspace** / **Alt+d**: Delete back to the start of the word, or on to
  its end.

- **Ctrl+u** / **Ctrl+k**: Delete everything before the cursor, or from it to
  the end.

- **Tab**: Switch between all entries and diffs-only mode.

- **Ctrl+r**: Switch between a regular expression and literal text.

- **Up**, **Ctrl+p** / **Down**, **Ctrl+n**: Recall the previous or next query
  applied in this bar, with its mode, as in bash; past the newest, the query
  being typed returns. Queries the same as the one shown are skipped, and empty
  ones are not kept. The filter and search bars each have their own history of
  up to 100 queries, kept until binate exits.

- **Enter**: Apply the query and the mode together.

- **Esc**: Close the bar, keeping the filter as it was.

### Search

**/** opens the search bar at the bottom, white on blue and starting with
**Search (regex):**, with the same keys for editing as the filter bar but
**Tab**, and the same regular expressions, or with **Ctrl+r** literal text.
**Enter** selects the next entry after the selection, in tree order, whose name
matches the query, ignoring case, wrapping around at the end, and expands the
directories above it. **Esc** closes the bar instead. Entries inside collapsed
directories are searched too, but not identical entries while they are hidden
(see **\*** above), and while a filter is applied, only its results.

While searching, the status bar shows the query, as **[search: name]**, or
**[search (literal): name]**, and these keys go from match to match, wrapping
around at the end with a note in the status bar:

- **n** / **N**: Select the next or previous match.

**Esc** ends the search, and **n** and **N** jump between differences again.
**Shift+Down** and **Shift+Up** jump between differences throughout. A search
with an empty query ends searching too.

## Diff view

The diff view shows two files side by side, each in a box titled with its path
after a dimmed **(viewing)**, which distinguishes the diff view from the tree
view. Where the whole path doesn't fit after it, it is shortened to **(v)**
first, and then the path in the middle, but never left out. The lines have line
numbers, and a mark between the boxes: blank for an unchanged line, **|** for a
changed line, and **<** or **>** for a line on one side only. As in the tree,
the marks have their colours, and a marked line has a faint background in its
mark's colour across both sides. Changed text inside a changed line has a
stronger one. It opens on **Enter** in the tree, and directly when **binate** is
given two files or streams, or a file and a directory.

It shows UTF-8 encoded text of up to 10 MiB per side. A side that is not valid
UTF-8, such as a binary file, or is larger, does not open: the tree stays, and a
red box titled **Unable to open** says why, as
**Only UTF-8 encoded text files can be opened (the left side is not).**, wrapped
at 80 columns, or narrower on a narrower terminal, until a key closes it. When
the sides fail differently, the box lists each side's reason on its own line,
starting with **-**. Tree entries open only if each side is a regular file or
missing. Symlinks and special entries in the tree don't open. In two-file mode,
which has no tree, each side that can't be shown says why in its own box
instead, in red, as **Unable to show data that is not UTF-8 encoded.**, and the
box of a side that could be says which side couldn't, as **Unable to compare
files due to an error on the left side.**

Lines are compared with an anchored diff: lines that appear once on each side
anchor the matching regions, so unrelated blank lines and closing braces are not
paired up. In each changed block, lines are paired as changed in order, and the
lines left over on the longer side are shown as one-sided. Inside a changed
pair, words, runs of spaces and other single characters are compared, and those
that differ are highlighted. A line longer than 10,000 characters, such as a
minified file on one line, is highlighted whole instead, since comparing it
would take too long.

Below the text, panels describe the two files as in the tree view (see
[Panels](#panels)), read again after an edit. A pipe's panel describes the pipe,
as it was when **binate** started.

Text is shown escaped (see [Showing text safely](#showing-text-safely)), with
tabs expanded to 4 columns. The status bar counts the lines changed, and on the
left or right only, as the tree's (**[1 changed] [2 right]**), then the scroll
position. A file that uses CR+LF throughout is marked **CRLF** in the status
bar. If only the line ends differ, the status bar says so. A last line without a
line feed differs from the same line with one.

By default, only changes are shown, with 3 lines of context (see **-C**). Each
run of lines left out is shown as one separator line. If there are no changed
lines, the whole file is shown.

- **j**, **Down** / **k**, **Up**: Scroll down or up.

- **PgDn**, **Ctrl+f** / **PgUp**, **Ctrl+b**: Scroll by a screen.

- **Home**, **gg** / **End**, **G**: Go to the start or end.

- **Left** / **Right**: Scroll sideways, when wrap is off.

- **n**, **Shift+Down** / **N**, **Shift+Up**: Go to the next or previous block
  of changes.

- **w**: Wrap long lines, or not (the default). A wide character that would
  cross the edge starts the next line.

- **f**: Show the full files, or only the changes with context.

- **e**: Edit a side (see [Editing](#editing)).

- **Esc**: Go back to the tree if the diff view was opened from it. In a session
  started on two files or streams, do nothing.

## Editing

**e** opens the selected entry in the tree view, or the pair shown in the diff
view, in your editor: **VISUAL**, or else **EDITOR** (see
[Environment](#environment)). It is the only way **binate** changes a file.

If both sides can be edited, a box like Help, titled **Select side to edit**,
lists both paths after their keys (home as **~**, shortened in the middle when
long, escaped), with the left one selected. **h** or **Left** opens the left
file at once, and **l** or **Right** the right one. **j**, **k**, **Down**,
**Up** and the other keys that move through Help move the selection, and
**Enter** or **Space** opens the selected file. **Esc** or **e** closes the box
without editing, and other keys are ignored meanwhile. A side can be edited only
if it is an existing regular file. Directories, missing sides and streams such
as **/dev/null** or pipes cannot be edited. Symlinks in the tree cannot be
edited either, while a file given through a symlink on the command line is
followed instead. If only one side can be edited, **e** opens it directly and
the status bar says which. If neither can, or neither **VISUAL** nor **EDITOR**
is set, a red box titled **Unable to edit** says why. If editing fails, as when
the editor can't be started or exits with an error status, one titled **Failed
to edit** does. Any key closes it, without acting on the screen behind.
**Ctrl+c** closes it and asks to quit.

The editor is run by the unrestricted parent (see [Process
structure](#process-structure)), attached to the terminal, and runs without
Landlock. The child sends the parent which side to edit, and in directory mode
the path relative to that side's root. The parent opens that path beneath the
root, with **openat2**(2) or, where it is unavailable, one name at a time,
without following symlinks or **..** out of it, and refuses anything but a
regular file. The editor starts in the file's directory, the very one the parent
opened, and is given only the file's name, as **./name**. In two-file mode, no
path is sent, and the editor starts in the directory **binate** was started in,
on the file given on the command line.

Once the editor exits, the file is read again by path, whether or not the
editor's exit status says it changed anything, so an editor that saves by
renaming a new file over the old one is followed. In the diff view, both sides
are read again and compared, keeping the view near the same line. A missing side
that the editor created is read too (or, if it can't be told whether it is
there, the error shown), and a file that is now gone, or no longer UTF-8 within
the size limit, shows that instead of its old contents. In the tree, the entry
is compared again in the scan's mode, after a running scan finishes, and its
mark and those of the directories above it follow: it may become identical,
different, unverified, or one-sided if the file was deleted.

The editor is not restricted, and follows symlinks like any program, each time
it opens or saves the file. Starting it in the directory the parent checked
means a directory on the way, replaced by a symlink while you edit, can't lead
it out of the tree. The file itself can still be: if someone else who can write
to its directory replaces it with a symlink while you edit, the editor follows
it, and may save outside the tree. Only edit a file in a directory others can
write to if you trust them.

## Keys

**?** shows the keys of the current screen, over it, in columns, with the first
row selected. **j**, **k**, **Down**, **Up**, **Ctrl+f**, **PgDn**, **Ctrl+b**,
**PgUp**, **gg**, **Home**, **G** and **End** move the selection, scrolling as
needed, and any other key closes it, without acting on the screen behind. The
selection only marks a row. These keys work everywhere:

- **Ctrl+c**: Quit, once confirmed.

Quitting asks first, as **dialog**(1) does: "Are you sure you want to exit?",
with two buttons, **Exit** and **Cancel**, the second focused. **x**, **X** or a
second **Ctrl+c** quits. **Left** focuses **Exit**, **Right** **Cancel**, and
**Tab** the other one. **Enter** or **Space** presses the focused button. Any
other key cancels. **Esc** never quits.

- **Ctrl+z**: Suspend. The shell's **fg** resumes. **binate** releases the
  terminal, and the parent stops the whole job, as the shell expects. If it
  can't, a red box titled **Failed to suspend** says why, as **Failed stopping
  the job (operation not permitted).**

A terminal narrower than 40 columns or shorter than 5 rows shows only a
"terminal too small" message until it is resized. Only quitting, which then
needs no confirmation, and suspending work meanwhile.

Keys are read in raw mode. A lone **Esc** waits 25 ms for the rest of an escape
sequence. Over a slow connection, a sequence split for longer reads as **Esc**
and other keys.

## Showing text safely

File names and file contents can contain characters that would control the
terminal, such as escape sequences or the bidi override U+202E, or that are
invisible, such as zero-width spaces. **binate** never writes such a character
to the terminal. It shows an escaped form instead, highlighted with the
**escape** style, so it is never mistaken for the same text written literally in
the file: a file containing the seven characters **\x1b[0m** shows them plainly,
while a file containing the ESC byte shows a highlighted **\x1b**.

- **\x**_NN_ for C0 controls (below U+0020), DEL (U+007F), and each byte that is
  not part of valid UTF-8, such as truncated or overlong sequences and encoded
  surrogates.
- **\u**_XXXX_ (or **\U**_XXXXXXXX_ above U+FFFF) for C1 controls (U+0080 to
  U+009F), every other character that is not printable, and every character with
  no width: format characters such as zero-width spaces, joiners and bidi
  controls, spaces other than U+0020, unassigned and private-use characters, and
  combining marks.
- In file contents, a tab is expanded with spaces to the next multiple of 4
  columns. In names and paths it is shown as **\t**, and a line feed as **\n**.
- Line ends are not shown: a line feed ends a line, and so does CR+LF in a file
  that uses CR+LF throughout. Any other CR, such as a lone CR, or one in a file
  that otherwise uses line feeds, is shown as **\r**, so line-ending differences
  between the two sides are visible.
- A backslash is shown as itself. The highlight tells an escape apart from a
  literal backslash.
- The target of a path's hyperlink, which is not shown, is quoted for the shell
  as bash's **${path@Q}** quotes it, but in printable ASCII alone: in single
  quotes, or, if it holds any other byte, in **$'...'** with each such byte as
  an escape, as **\E** for ESC and **\303\245** for **å**. Unlike bash,
  printable characters beyond ASCII are escaped too, as invalid UTF-8 is.
- Any other hyperlink, as a checksum's (see [Actions](#actions)), is written
  only if its target is valid UTF-8 without control characters, C0, DEL or C1,
  which could end the sequence. Otherwise the text is shown without a link.

An escape takes as many columns as it has characters: 4 for **\x1b**, 6 for
**\u200b**. Text written with decomposed accents (**e** followed by U+0301)
therefore shows the accent as **\u0301**, and the variation selector and joiners
in emoji sequences show too. This keeps columns aligned and makes invisible
differences visible, at some cost to readability.

The same applies to everything from the file system that reaches the screen:
file contents, names in the tree and filter results, and paths in the boxes'
titles and the Edit popup. Messages on stderr, such as warnings and errors,
can't be highlighted, so there a literal backslash is doubled as `\\` to keep
escapes unambiguous.

## Environment

- **BINATE_COLORS**: Colours, as a list of *key*=*SGR* separated by colons, in
  the style of **LS_COLORS**, for example
  **changed=33:leftonly=32:dir=1;34:select=48;5;238**. The values are SGR
  parameters as the terminal takes them, so 16-colour, 256-colour (**38;5;**_N_)
  and truecolour (**38;2;**_R_**;**_G_**;**_B_) values all work. A value may
  contain only digits and **;**, so the variable can't add other escape
  sequences. An unknown key or an invalid value prints a warning before the
  screen opens, and the default stays. See [Colours](#colours).

- **COLORTERM**, **TERM**: Whether the terminal shows truecolour, for the
  default backgrounds (see [BINATE_COLORS](#environment)).

- **NO_COLOR**: If set and not empty, turns colour off, and **BINATE_COLORS** is
  not read. The marks are then bold, changed text inside lines bold and
  underlined, escapes, the selected row, the filter bar and the boxes in reverse
  video, with Help's selected row not reversed, the quit prompt bold with its
  focused button bold instead and the **x** of **Exit** underlined, the borders
  dim, and rows and lines have no background. In the selected row and in reverse
  video, escapes are underlined instead.

- **VISUAL**, **EDITOR**: The editor **e** runs, given the path to edit as its
  last argument: a relative path starts with **./**, so that no file name can be
  taken for an option (**-name**) or an editor command (**+name**). The parent
  reads it once, at startup, and splits it on spaces, without shell quoting. The
  one used, **VISUAL** unless it is empty or only spaces, may contain only ASCII
  letters, digits, spaces and **-\_./+=,:@%~**. If it contains anything else,
  **binate** exits with an error before it starts. The other one is not read.
  These are enough for an ordinary command, and keep control characters out of
  the editor's name, which the error popups quote.

- **HOME**: Paths under it are shown with **~**.

### Colours

The keys of **BINATE_COLORS**, with their defaults:

- **changed** (33): the **|** mark.
- **attrs** (36): the **~** mark.
- **mtime** (1): the **#** mark.
- **inline**: the background of changed text inside a changed line in the diff
  view, a stronger yellow.
- **dir** (1;34): the **dir** type.
- **link** (1;36): the **link** type.
- **pipe** (33): the **pipe** type.
- **device** (1;33): the **char** and **bdev** types.
- **socket** (1;35): the **sock** type.
- **exec** (1;32): the **file** type of a file with an execute bit.
- **leftonly** (32): the **<** mark.
- **rightonly** (34): the **>** mark.
- **unverified** (35): the **?** mark.
- **type** (1;31): the **!** mark.
- **changedbg**, **leftonlybg**, **rightonlybg**: the background of a line in
  the diff view marked **|**, **<** or **>**: dark yellow, green and blue. Tree
  rows have no background by state.
- **select**: the background of the selected row, grey.
- **escape** (7): escaped characters (see [Showing text
  safely](#showing-text-safely)).
- **border** (2): the panes' borders, also the loading box's elapsed time and
  path, over **accent**.
- **status** (none): the status bar's background and its text other than counts.
  The brackets take **border**.
- **accent** (97;44): the filter bar, the loading box and Help, white on blue.
- **helpselect** (30;46): the selected row in Help, black on cyan.
- **quit** (97;41): the quit prompt and error popups, white on red.
- **quitfocus** (1;97;44): the quit prompt's focused button, bold white on blue.
- **quitkey** (1;93): the **x** of the **Exit** button, bold yellow, over the
  button's own colour, and the letters of the Compare popup's rows.

Text and marks use only the 16 ANSI colours by default, so they follow the
terminal's palette, light or dark. The backgrounds are faint tints for a dark
terminal: in truecolour if the terminal says it shows it, with **COLORTERM** set
to **truecolor** or **24bit** or a **TERM** ending in **-direct**, and otherwise
from the 256-colour palette, which has only stronger ones. There is no reliable
way to ask a terminal. Over **ssh**(1), or in **tmux**(1) started without it,
**COLORTERM** is often unset, and setting it there gives the truecolour tints.
The defaults, as **BINATE_COLORS**, in truecolour:

    changedbg=48;2;34;31;24:leftonlybg=48;2;22;34;28:
    rightonlybg=48;2;22;28;42:inline=48;2;70;60;32:select=48;2;52;54;60

and in 256 colours:

    changedbg=48;5;58:leftonlybg=48;5;22:rightonlybg=48;5;17:
    inline=48;5;100:select=48;5;238

On a light terminal, set light ones, such as:

    changedbg=48;2;252;248;222:leftonlybg=48;2;228;246;230:
    rightonlybg=48;2;228;236;252:inline=48;2;246;232;150:
    select=48;2;214;214;214

(each as one line). **inline** is a background colour and **escape** reverse
video, so the two never look alike.

## Exit status

- **0**: After a normal quit.

- **2**: On errors: bad arguments, unreadable inputs, or no terminal.

Unlike **diff**(1), the status does not say whether the inputs differ, since
**binate** is interactive. If binate is ended by a signal, the status is 128
plus the signal's number, as the shell reports it.

## Process structure

**binate** runs as two processes. The first, the parent, parses the arguments,
reads **VISUAL** and **EDITOR**, saves the terminal settings and starts the
second, the child, which does everything else: scanning, comparing and drawing.
The child runs restricted by Landlock (see [Landlock](#landlock)). The parent
does not, so it can run your editor and stop the job for **Ctrl+z**.

Landlock can't be lifted once applied, and every program a restricted process
starts inherits it. Restricting only some threads doesn't work in Go, which
moves goroutines between threads freely. Hence the second process.

The child reads input only while it holds the terminal, so it never competes
with the shell or the editor for keys. Landlock keeps it from signalling the
job, so for **Ctrl+z** it releases the terminal, asks the parent to stop the
job, and waits until the job is continued.

The parent restores the terminal settings it saved when the child exits for any
reason, leaves the alternate screen if the child held it, and starts a fresh,
cleared line for diagnostics and the shell's next prompt. The child clears its
last frame before leaving the alternate screen. A crash or **kill -9** of the
child leaves a usable terminal. **SIGTERM** sent to the parent, as by
**kill**(1), and **SIGHUP** are passed on to the child, which restores the
terminal and exits. **Ctrl+c** sends no signal while **binate** holds the
terminal. It is read as a key and quits, once confirmed.

## Landlock

The child restricts itself with Landlock before it reads any input:

- Directories compared as trees may be listed and read, with everything beneath
  them, including files created later.
- A file compared on its own may be read through a rule on its directory, which
  allows reading files but not listing directories. A rule on the file itself
  would not survive an editor saving by renaming a new file over the old one.
  The cost: a Landlock rule covers everything beneath its directory, so the
  child could read any file in that directory or in a directory below it whose
  path it knows. Comparing **~/a.txt** with another file lets it read
  **~/.ssh/id_ed25519**, if it asks for it by name. It still can't write, run
  programs or list those directories.
- Rules are placed on resolved paths: a directory or file given through a
  symlink gets its rule where the symlink leads, since that is where Landlock
  looks.
- Nothing may be written, no program run, no TCP connection made or port bound,
  no UDP port bound or datagram sent, and no process outside the child
  signalled, nor an abstract unix socket outside it reached, as far as the
  kernel's Landlock ABI restricts each (see below).
- **/etc/passwd** and **/etc/group**, which name the owners in the panels, are
  read before the restriction, and not again.

The editor is run by the parent, and is not restricted (see
[Editing](#editing)).

The protection depends on the kernel. Each Landlock ABI adds restrictions, and
**binate** applies those the kernel has, leaving out the rest without failing:

- Writing and running programs are denied from ABI 1 (Linux 5.13), except
  truncating a file, which only ABI 3 (Linux 6.2) denies.
- TCP, connecting and binding, from ABI 4 (Linux 6.7).
- Signalling other processes and reaching abstract unix sockets outside the
  child from ABI 6 (Linux 6.12), with its fix for signal scoping.
- UDP, binding and sending, only from ABI 10.

The policy targets ABI 11, which adds no access restrictions beyond ABI 10.

Below ABI 3, a restricted child can still empty or shorten any file it can
reach, with **truncate**(2), though it can't otherwise change its contents.
Landlock restricts no other network protocol at any ABI, such as ICMP through
ping sockets, or netlink. So even on a recent kernel, the network protection is
partial: below ABI 10, a restricted child can still send UDP datagrams, as to a
DNS server. On a kernel without Landlock, or with it disabled, **binate** runs
unprotected without failing. **-v** reports whether Landlock is active and at
which ABI.

## Caveats

**binate** assumes an xterm-style terminal, which includes the Linux console,
and always writes UTF-8, whatever the locale. The marks are ASCII, but the Help
box, the **›** in filter results and file names themselves need a UTF-8
terminal.

Column widths come from Unicode's East Asian Width property, which gets some
emoji wrong. Such characters can misalign columns. Characters with no width
can't, since they are escaped.

See also [**binate**(1)](binate.1.md), **diff**(1) and **sdiff**(1).
