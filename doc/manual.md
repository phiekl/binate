<!--
SPDX-FileCopyrightText: 2026 Philip Eklöf

SPDX-License-Identifier: MIT
-->

# binate manual

Full reference for the binate file and directory difference viewer.

```
binate [options] left right
binate -g [-C directory] [options] [left [right]]
agitate [-C directory] [options] [left [right]]
```

**binate** shows two directory trees side by side, and marks where they
differ. With **-g**, or run as **agitate**, a symlink to it, they are trees of
two git revisions, or a revision and the work tree. **binate** never changes
files itself. **e** opens eligible files in your editor.

It is Linux-only, needs /proc mounted to open files, and needs a terminal: if
stdin or stdout is not a terminal, it exits with an error before drawing
anything. The man page,
[**binate**(1)](binate.1.md), is a short summary of the options, marks and keys.

- [Inputs](#inputs)
- [Options](#options)
- [Git](#git)
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

*left* and *right* are directories. Symlinks given on the command line are
followed. **binate** compares no files on their own. Two files are for
**diff**(1), or **git diff** for files in a repository.

**/dev/null** stands for nothing, an empty directory, with everything on the
other side shown as added or removed. It is told apart by what it is, the null
device by any name, as through a symlink, without being opened. It can't be
given on both sides. Any other input is refused without being opened, as a
file, a pipe, a process substitution such as **<(**_command_**)**, a FIFO,
another device or a socket.

An input may be named by a file descriptor **binate** inherits, as **/dev/fd/3**
given with **3<**_directory_, and is then the directory that descriptor is open
on. Descriptors keep their numbers in the child, and those **binate** passes it
besides take free ones.

Without **-g**, an argument starting with a backend's name and a colon, as
**git:~/src/app@main**, is a side read by that backend instead of a path. The
name is a letter followed by letters, digits, **+**, **-** and **.**, as a URL's
scheme is. **git** is the only backend, and any other name is an error. A path
of that form, as a directory named **notes:old**, is given as **./notes:old**. A
revision of the current repository, as **HEAD:src**, takes **-g**. With
**git:**, the rest names a repository's directory and, after an **@**, a
revision, its work tree or its index (see
[Other repositories](#other-repositories)). The other argument may be a
directory, **/dev/null** or another backend's side.

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

  - **left**, **right**: entries on the left, or the right, side only (**-**,
    **+**). Left out, they are hidden, with everything below them.
  - **type**: entries of different types on the two sides (**!**). Left out,
    they are hidden, with everything below them.
  - **content**: a regular file's size and contents, and the commit of a git
    submodule (see [Git](#git)).
  - **target**: a symlink's target.
  - **device**: a device's major and minor number.
  - **mode**: the permission bits, with setuid, setgid and sticky.
  - **owner**: the user and group, by number.
  - **mtime**: the modification time, to the nanosecond.

- **-U**, **--unified** *n*: Show *n* lines of context around changes in the
  collapsed diff view (default 3), as **diff -U** and **git diff -U** do. Must
  not be negative.

- **-x**, **--exclude** *pattern*: Skip entries whose name matches *pattern*, in
  the syntax of Go's **path.Match**: **\***, **?**, **[**...**]**, and a
  backslash to quote. The pattern is matched against each entry's name, not its
  path. Repeatable.

- **-q**, **--quick**: In the tree view, compare files by size and modification
  time instead of by contents. Not with **-g** or a **git:** side, since
  revisions do not record modification times.

- **-g**, **--git**: Take *left* and *right* as revisions of the git repository
  of the current directory, or of **-C**, such as **main**, **HEAD~2** or
  **v1.0:src**, and compare their trees. Regular files are read only within the
  repository. Give **.** for the work tree on one side, or **:** for the index.
  A missing *right* defaults to **.**, and a missing *left* defaults to
  **HEAD**. A side in another repository is its directory, **@** and the
  revision, as **~/src/app@main** (see
  [Other repositories](#other-repositories)). See [Git](#git).

- **--cached**: With **-g**, compare *left*, or **HEAD** if missing, with the
  index of its repository, as **git diff --cached** does. The same as giving
  **:** as *right*, or **~/src/app@:** for **~/src/app@main**. *left* must be a
  revision, not the work tree or the index.

- **-C**, **--directory** *directory*: With **-g**, find the repository from
  *directory* instead of the current directory, as **git -C** does: revisions'
  paths starting with **./** or **../** and other repositories' relative
  directories are relative to it.

- **-h**, **--help**: Show the options and exit.

- **-v**, **--verbose**: Write debug messages to stderr, including whether
  Landlock is active and at which ABI, and whether the terminal is taken to show
  truecolour (see [BINATE_COLORS](#environment)). Repeat for more (**-vv**,
  **-vvv**).

- **-V**, **--version**: Show the version and exit.

## Git

With **-g**, or when run as **agitate**, by a symlink of that name, *left* and
*right* name revisions or the work tree of the repository found from the
current directory or **-C**.
Before applying the sandbox, it resolves symlinks along the starting directory,
which may read links outside the repository. It then finds the top as git finds
it, by names without reading regular files on the way: the
nearest of the directory and those above it with a **.git** in it, a directory
or a file naming one, not past a file system's boundary or a directory in
**GIT_CEILING_DIRECTORIES**, unless **GIT_DISCOVERY_ACROSS_FILESYSTEM** is true;
or the directory itself, if a bare repository, which is told apart only there. A
repository with any of its directories outside the top, symlinks followed, is
refused before anything there is read: a linked work tree, whose git directory
is in the main one's, a submodule checked out, whose git directory is in its
superproject's, one apart from its work tree, as **git init --separate-git-dir**
makes, or one with alternates elsewhere, as **git clone --reference** makes. An
alternate that doesn't exist is skipped, as git skips it. Symlinks in the git
directory to files, as an old repository's **HEAD**, are not read either.
**GIT_DIR** and **GIT_WORK_TREE** are ignored. A repository elsewhere is given
by its directory instead, as in **~/.dotfiles@main** (see
[Other repositories](#other-repositories)). A row at the top of the screen says
what each side is, above its box, unless the terminal is too short to spare it.
A side in a repository shows **(git)** and the repository's work tree, or its
git directory if it is bare, linked to in full as the panels' paths are, even
where both sides are in the same repository. A directory or **/dev/null**
opposite it shows **(dir)** or **(nothing)** and its path. A commit's message,
stat or trees show the repository of the side it is of above both boxes. A
revision naming a commit or a tag compares its tree, and one naming a path, as
**HEAD:src**, the tree there. A revision naming a file, as **HEAD:README**, is
refused.

**binate** reads the repository itself, without running git: loose objects,
packs and alternates, in either object format, SHA-1 or SHA-256. As in Git, an
object in none of the packs read and not loose is looked for again in packs
added since, as after **git gc**. It reads a
repository of a format git reads, version 0 or 1, and refuses one with an
extension it doesn't know, or refs stored as reftable. Nothing in the
repository's configuration is acted on, so a repository owned by another user is
read as any other, where git refuses it unless **safe.directory** allows it.

A revision is written as for **git rev-parse**:

- a ref name, as **main**, **v1.0**, **origin/main** or **refs/heads/main**,
  looked up as git does, loose or packed, following symrefs; **HEAD**, or **@**
  for it;
- an object name, or 4 or more of its first hex digits, naming one object alone;
- then any of **^** or **^**_n_, a commit's first or _n_-th parent, **~** or
  **~**_n_, its first parent _n_ times, **^{}**, what a tag points to, and
  **^{commit}**, **^{tree}**, **^{blob}**, **^{tag}** or **^{object}**;
- then maybe **:**_path_, the entry at _path_ in the tree, relative to the
  current directory, or that of **-C**, if it starts with **./** or **../**. As
  in git, a path ending in **/**, or a relative one ending in **.** or **..**,
  must name a tree.

Reflogs and upstreams (**@{**...**}**), ranges (**..**, **^!**, **^@**),
searches (**:/**_text_, **^{/**_text_**}**) and the index (**:**_path_) are not
supported.

A tree's entries have the modes git records: a file is **0644** or, if
executable, **0755**; a directory **0755**; a symlink **0777**. They have no
times or owners, which are never compared, whatever **--compare** asks:
[Compare](#compare) shows them as not recorded. The panels show only what is
recorded, and paths as git names them, as **main:src/a.c**, and the commit that
last changed an entry, as **git log -1 --** _path_ finds it: from the revision's
commit, a parent with the entry the same is followed, the first such of a merge,
until a commit has none. That commit is looked for in at most 100000 commits.
A commit graph speeds up longer walks by supplying the object names of
parents and trees. Without a graph, a long walk may take a second in a
large repository. Shallow repositories always walk without a graph.
Changed-path filters skip tree reads when a file is unchanged from the
first parent. To write a graph with filters, run
**git commit-graph write --reachable --changed-paths**.
A revision naming a tree or a blob, not through a commit, has no history to
show. Files are compared by their blob's object name, without being read:
two files are the same exactly when their blobs are. Symlinks are compared by
their targets, and not followed. A submodule, a commit of another repository,
is not read: it shows as **sub**, and two are compared by their commits, as
contents, marked **|** where they differ.
Its panel shows the commit as its type, and no size or mode. An object missing
from the repository, as one a partial clone left out, is an error where it is
read: a tree on its entry, and a file only in the panels and when its
differences are shown, so that two revisions of a clone without blobs can be
compared. Files in a revision can't be edited.

An error resolving a revision is written before the screen is taken.

### Work tree

Give **.** on either side, as in **binate -g HEAD .** or **agitate . HEAD**.
With a single revision, as in **binate -g main**, the work tree is on the right.
With no arguments, **binate -g** and **agitate** compare **HEAD** with the work
tree. Before the first commit, **HEAD** or **@** opposite the work tree is an
empty tree named **(no commits)**, as **git status** then lists every file as
new. The comparison starts at the repository's top, even when run from one of
its subdirectories. The revision must name a full tree. The repository needs a
discovered work tree.

The work-tree side shows tracked paths that exist on disk and untracked paths
allowed by Git's ignore rules. Rules are read from each directory's
**.gitignore**, the common Git directory's **info/exclude**, and the user's
global ignore file, in decreasing precedence. A deeper **.gitignore** takes
precedence over an ancestor's. With **core.ignoreCase** set, patterns match
ASCII letters of either case, as in Git. The global path comes from
**core.excludesFile** set directly in the user's Git configuration or the
system's, **/etc/gitconfig** unless **GIT_CONFIG_NOSYSTEM** is set, or from
**$XDG_CONFIG_HOME/git/ignore** by default, with **~/.config** as the fallback
for **XDG_CONFIG_HOME**. A **core.excludesFile** in the repository's own
configuration takes precedence. The repository's file is read only within the
top, and one outside it gives no global rules. A symlink in place of a
**.gitignore** is ignored, while an excludes file is opened through symlinks
that stay within the top, as Git does. As in Git, an ignore file that can't be
read adds no rules. A tracked file in an ignored directory remains visible. Git
metadata and untracked directories with no visible files are left out.

The index determines which paths are tracked. Versions 2 through 4 in SHA-1 and
SHA-256 repositories are supported. A split or sparse index is reported as an
error. As in Git, a skip-worktree entry is compared as the index records it, and
a skip-worktree **.gitignore** missing from disk is read from the index. In a
sparse checkout, a skip-worktree file present on disk is read from disk. A file
marked with **git update-index --assume-unchanged** is compared as the index
records it too, as **git diff** compares it, even in a sparse checkout and where
deleted from disk. A **.gitignore** so marked and missing from disk adds no
rules, as in Git. A rescan reloads the index and the repository's ignore files
as part of the new scan, which shows an error reading them at the top. The
user's global file outside the repository is read when the comparison starts.

Files are compared by their on-disk bytes, without Git attribute filters.
Opposite a revision or the index, a file whose size, times, inode and owner
match those the index recorded when it was staged is unchanged since, as in Git,
and is compared by its staged blob's ID without being read. Its stat data is
read again for this just before it is compared. A file modified in the second
the index was last written, or later, is read, as its times may not show a later
change. As in Git, the second is whole. A file Git filters as it stages it, as
for line endings, is always read, as its blob may hold other bytes than the
disk. Its attributes tell, as Git assigns them from the repository's
**info/attributes**, the **.gitattributes** files in its directory and those
above it, on disk or else in the index, and the user's and the system's
attributes files, with macros. A **core.attributesFile** in the repository's own
configuration takes the place of the user's file. It is read only within the
top, as the repository's excludes file is. One outside the top can't be read,
and every file is then taken as filtered. Git filters a file where **text**, or
else **crlf**, is set, **auto** or **input**, or is not unset and **eol** or
**core.autoCRLF** is set. It also filters a file with **ident**,
**working-tree-encoding** or a **filter** whose driver is configured to clean.
Where an attributes file can't be read, every file is taken as filtered. A file
whose blob's size is not its own is read too, as filters that applied when it
was staged, and no longer, would change it. Nothing records which filters
applied when a file was staged, and a filter that keeps a file's size and no
longer applies can leave a difference unseen. **git status** misses it too,
unless the file was modified in the second the index was last written. A file's
executable bit is compared using the modes Git records. With **core.fileMode**
set to false, a file's executable bit comes from the index as in Git, and an
untracked file is not executable. Symlinks are compared by their targets. A
checked-out submodule is compared by its HEAD when that can be read within the
repository's top. The directory of an uninitialized submodule stands for its
commit in the index. A submodule with no commits stands for its commit in the
index, as in Git. An untracked directory whose **.git** is a valid git
directory, a symlink to one or a file naming one is shown as a submodule at its
HEAD, with **(no commits)** if it has none, as Git shows it as one entry. Its
HEAD is read in its own object format, which may differ from the repository's,
as for a SHA-256 repository in a SHA-1 work tree. Its refs may be stored as
reftable, which the repository compared can't. Both sides of the comparison are
read-only.

### Status

Each entry of the work tree shows its status before its type, in the two letters
**git status --short** gives it. The first says what is staged in the index
against **HEAD**, in green, and the second what changed on disk since and is not
staged, in red. A blank letter is no change.

- **A**: added.
- **M**: modified, in contents or executable bit.
- **D**: deleted.
- **T**: type changed, as a file replaced by a symlink.
- **??**: untracked, in red. An untracked directory has it, as does everything
  below it.
- **DD**, **AU**, **UD**, **UA**, **DU**, **AA**, **UU**: unmerged, in red, as
  Git names a conflict. **UU** is a file both sides of a merge modified.

An entry with no status, a tracked directory or an unchanged file, shows nothing
there. Where a side has no entry, its empty cell says the status in words,
dimmed. A file deleted from disk has **deleted, not staged** after its letters
on the work tree side where the **D** is second, and **deleted, staged** where
it is first. A file not in the revision has its status in words on the
revision's side, as **untracked** or **added, staged**. Below an untracked
directory, only the directory says it is untracked. The panel of the work tree
side says the status in words too, in a **git** row (see [Panels](#panels)). The
status bar counts the entries with something staged, not staged, untracked and
unmerged, and **?** lists the letters.

Statuses are always against **HEAD**, as **git status** gives them, whichever
revision is opposite the work tree. An entry with a status is shown even where
the two sides are the same, as a file whose deletion is staged while it is kept
on disk, along with the directories leading to it. It is not hidden as
identical, nor left out of a filter of differences. An entry on neither side, as
one in **HEAD** but not in an older revision, staged for deletion, or a file
added to the index and then deleted from disk, is shown for its status. Its type
and name are dimmed on the work tree's side, with no mark, and the other side
says its status in words. It counts only by its status in the status bar.
Entries recorded below a file staged in place of a directory are shown below it,
and the file can be expanded as a directory is.

A file whose size, times, inode and owner on disk match those the index recorded
when it was staged is not read, as in Git. Any other is hashed as Git would hash
it, without attribute filters, and compared with the index, and a rescan stops
the hashing. A file modified in the second the index was last written, or later,
is read, as it may have changed again since it was staged without its times
showing it. A file Git may filter as it stages it (see [Work tree](#work-tree))
is read only if modified at or after the moment, to the nanosecond, the index
was last written. Its unfiltered bytes can't show it unchanged where Git, which
filters it, would. A file in place of a tracked directory is untracked, and a
directory staged in place of a file in **HEAD** has the file's deletion.
**HEAD** and the index are read again on each rescan. Renames are not looked
for, and a renamed file is deleted at its old path and added at its new one, as
with **git status --no-renames**. A submodule's status follows its checked-out
commit, not changes inside it. As in Git, a file marked with **git update-index
--assume-unchanged** has no unstaged change, even where deleted from disk.

### Index

Give **:** on either side for the index, which holds what is staged for the
next commit. **agitate --cached**, or **binate -g HEAD :**, compares **HEAD**
with the index, as **git diff --cached** does. **agitate --cached main**
compares **main** with it instead. **agitate :** compares the index with the
work tree, as **git diff** does, along with untracked files. The revision
opposite the index must name a full tree, and the repository needs a
discovered work tree. Before the first commit, **HEAD** is an empty tree here
too.

The index's entries are named as Git names them, as **:src/a.c**, and are
compared by their blob IDs, as a revision's are. Their panels show the path,
type, size and mode. A file added with **git add -N** is left out, as Git
compares it as not staged. A path with a merge conflict has no version staged
and shows as **unmerged path**. A rescan reads the index again.

### Other repositories

A side may be in another repository than the one of the current directory. With
**-g**, or as **agitate**, an argument starting with **/**, **./**, **../** or
**~** names a repository's directory, then **@**, then what to compare there, as
**~/src/app@main**. Nothing after the **@** stands for **HEAD**, and **.** and
**:** for that repository's work tree and index, as in **~/src/app@.** and
**~/src/app@:**. No revision starts that way, and **.** alone is still the
current repository's work tree. The **@** taken is the last one with a directory
before it, which allows an **@** in the directory's name, as in
**~/pkg@1.2@main**, and in the revision, as in **./app@@~2**. A relative
directory is relative to **-C** if given. The repository is found from the
directory as from the current one, and a revision's path starting with **./**
or **../** is relative to the directory.

**agitate ~/src/app@main** compares **main** of that repository with the work
tree of the current one, as **agitate main** compares the current one's. With
both sides in other repositories, as in **agitate ~/a@main ~/b@main**, the
current directory is not searched for a repository. Without **-g**, the **git:**
backend names such a side, as in **binate git:~/src/app@v1.0 /mnt/app**, and
the other side may be a directory or **/dev/null** (see [Inputs](#inputs)). With
**-g**, **git:** starts a revision's path instead, as on a branch named **git**,
and a side that fails to resolve that way gets a hint to drop it. After
**git:**, any path names the directory, as in **git:app@main**. The shell
leaves the **~** after **git:** as it is, and **binate** expands a leading **~**
or **~/** itself.

Each side is read from its own repository. Files of two repositories of the same
object format are compared by their blobs' IDs without being read, and those of
repositories of different formats by their contents. Opposite a directory on
disk, a revision, index or work tree is compared as a revision is with a work
tree. Both sides have the modes Git records, a file **0644** or, if its owner
may run it, **0755**, a directory **0755** and a symlink **0777**, which the
directory's panels show too, and times and owners are not compared. The
directory's files can be edited. Opposite **/dev/null**, a revision's tree is
compared with an empty one.

Only one side can show statuses, and two work trees can't be compared. A work
tree or index can't be compared with itself, as the same repository's on both
sides, even where a symlink names its git directory by another path.

### Commits

Three actions of the Actions popup, **a**, act on the commit that last changed
the selected entry, the one its panel shows: **Commit message**, its key **m**,
shows it, **Commit diff**, **d**, compares the trees before and after it, and
**Commit stat**, **s**, counts the lines it changed. The diff view has them too:
there, **a** opens a popup listing these three alone. If the sides' commits
differ, a box like the one of [Editing](#editing), titled **Select side's commit
to show**, **to compare** or **to count**, lists each side's commit, abbreviated
to 12 digits, and its subject: **h** or **Left** takes the left one, **l** or
**Right** the right one, **Enter** or **Space** the selected one, and **Esc**
closes it. Going back from the side chosen opens the box again, that side
selected, to choose the other; closing it, or going back from a commit taken at
once, opens the Actions popup again, the action selected. If only one side has a
commit, as when the other is missing, it is taken at once, and the status bar
says which; with none, a red box says why. While the commits are still being
found, the status bar says so, and nothing is shown.

**Commit message** shows it as **git show --no-patch** does, in git's medium
format: **commit** and its object name, **Merge:** and its parents abbreviated
if it has more than one, **Author:** and **Date:**, the time authored in its own
zone, then the message, indented by four spaces, tabs expanded and spaces at the
ends of lines left out. The author is as recorded: a **.mailmap** is not read,
and signatures are neither shown nor checked. The text is in a box across the
screen, after a blank line, its lines broken to fit; **j**, **k**, **PgDn**,
**PgUp**, **gg**, **G** and their alternatives scroll it, and **Esc** goes back.

**Commit stat** shows the files the commit changed as
**git show --stat --summary --diff-merges=first-parent --no-renames** does,
without the message, after a blank line, at the width of the text, once counted
in the background: a line for each file it changed, from its first parent, or
from nothing if it has none: its name, quoted as git quotes it, the lines
changed and a graph of **+** and **-**, scaled to fit as git scales it, or for a
binary file **Bin** and its sizes before and after, then the totals, then the
files created and deleted and whose modes changed. A file renamed shows as one
deleted and one created. A file with a NUL in its first 8000 bytes is binary, as
one larger than 512 MiB; **.gitattributes** is not read. The fewest lines
changed are counted. For a large change git may count a few more, as it counts
lines found on both sides, such as blank ones, as changed among many others that
are. A commit changing no files, as an empty one, says **no files changed**.
**Esc** stops counting.

**Commit diff** opens the tree view on the commit's first parent's tree and its
own, named by the commit, abbreviated as **git rev-parse --short** does, with a
**^** for the parent's, as **1a2b3c4^** and **1a2b3c4**. The entry is selected
in them. A commit without parents is compared with an empty tree, named **(no
parent)**; for a merge, the status bar says it is compared with the first of its
parents. The trees are those of any other revisions: their entries have their
own last changes, and the commit actions and **Enter** work in them too.
**Esc**, once nothing else is left to close, goes back to the view they were
opened from, and the comparison is cancelled.

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
device numbers match. A file is checked to be regular before it is opened for
reading, through the same handle, so a device that takes a file's place during
the scan is never opened either. A directory is listed only if it still is one
when opened, so a FIFO, device or symlink put in its place is refused unopened.
Symlinks within the trees are compared by their stored targets without being
followed. A directory is compared by its entries. Files are reopened through
**/proc/self/fd**, and without /proc **binate** exits with an error at start.

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

The column between the two sides marks each pair:

- **=**: No difference found.

- **|**: The data differs: the size or contents of a file, the target of a
  symlink, or the number of a device.

- **~**: The attributes differ: the mode or owner.

- **#**: The modification time differs, if compared (see **--compare**).

- **\***: More than one of the above differs.

- **?**: Not verified: a side could not be read, or, in quick mode, the size is
  the same but the modification time is not.

- **-**, **+**: Present on the left, or the right, side only.

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
and block device), **sock** (socket), **sub** (a git submodule, see [Git](#git))
and **?** (one that could not be read), in the colours **ls** gives them by
default, an executable file in green. A directory has a **▸** or **▾** before
its name and a **/** after it. A symlink shows its target after **->**, and a
submodule the first 12 digits of its commit after **@**, or **(no commits)** for
a repository in the work tree with none yet. In a git work tree, the letters of
each entry's status come before the type (see [Status](#status)). The mark (see
[Marks](#marks)) is shown once per row, in a column of its own between the two
boxes. The bottom border of each box shows the selected row and the number of
rows shown, as **3/12**.

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
mtime]**, then **unverified** and **pending**, and for a git work tree
**staged**, **unstaged**, **untracked** and **unmerged** (see
[Status](#status)). The total counts each pair once, except directories present
on both sides and nonempty one-sided directories, whose contents are counted
instead. Each kind of difference counts the pairs that differ in it, so a pair
that differs in more than one counts under each, and a directory on both sides
counts under its own attributes. While a scan runs, a centered white-on-blue box
replaces the status bar. Beside a spinner, it shows the number of entries read
so far, the elapsed time, and the directory or file being read, relative to the
roots. Entries are counted as they are read. The total is unknown until the scan
finishes, so no percentage is shown. Short terminals leave out the path. Tree
rows and entry panels are drawn once the scan finishes, when the box disappears
and the status bar returns. Rescanning shows the box again and keeps the
selection and expansion by path.

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
  While a filter is applied, it switches the filter between all and diffs-only
  mode instead (see Filter).

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

- **a**: Run an action on the selected entry, as a checksum, or in git mode
  showing the commit that last changed it (see [Actions](#actions)).

- **r**: Scan again, comparing what is chosen. A running scan is cancelled.

- **Esc**: Close an action's result, or else end a search, or else clear an
  applied filter, or else, in a commit's trees, go back to the view they were
  opened from.

### Panels

Below a line across each box, a panel shows the entry on that side, one field
per row:

- **path**: the full path, from the root made absolute, with home as **~**,
  shortened in the middle when long, and escaped. It is a hyperlink (OSC 8) to
  the full path, for terminals that support them, quoted for the shell (see
  [Showing text safely](#showing-text-safely)) rather than as a **file://** URL,
  as **'/home/al/it'\''s here'**, to paste into a command line. A side without
  the entry has its path dimmed, and no link. A side in a git revision has the
  path git names it by, as **main:src/a.c**: the part after the colon links to
  the entry's path from the top of the repository, as **'src/a.c'**, as git
  commands take it. When shortened, the revision is kept whole. A revision
  naming a tree or blob not through a commit, as **main^{tree}**, whose place in
  the repository isn't known, has no link.
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

A work tree compared with a git revision shows the path, type, size, mode and
mtime, leaving out the owner, links, inode and other times, which Git neither
records nor compares. After the path, a **git** row says the entry's status in
words, as **modified, not staged** or **added, staged, then modified, not
staged**, and **modified, partly staged** for **MM** (see [Status](#status)). A
side in a git revision shows only the path, type, size and mode, which is all
git records, and then, if the revision names a commit, the commit that last
changed the entry, found in the background:

- **commit**: its object name.
- **author**: who made the change, by name and email, as **git show** shows it.
- **authored**, **committed**: when its author made the change, and when it was
  committed, in local time to the second.

Where they differ from the other side's, or the other side has none, they take
the colour of the entry in the tree, as **|** for a file whose contents differ,
or **-** for one on the left only.

Fields that differ between two sides that could be read are marked between the
panels with the tree's marks, and their values take the mark's colour on both
sides: **|** for a regular file's size, or a symlink's target or a device's
number in the type, **~** for the mode and owner, **#** for the mtime, and **!**
for one type opposite another, which then leaves the size unmarked. A
directory's number of entries, and a symlink's size, which is its target's
length, are not marked. A difference in entry type is always marked. Other
fields are marked only when chosen for comparison (see **--compare** and
[Compare](#compare)). The diff view's panels follow the tree's comparison
settings. The mtime is compared to the
nanosecond, as in the tree: two that differ within the same second show as much
of the fraction as tells them apart, as **14:10:00.4** and **14:10:00.5**. The
more recent one is marked **(newer)**, unless the other side is in a git
revision, which records no mtime, or the sides are compared as git compares
them, as a work tree with a directory, where the tree compares no mtimes. The
inode and the other times differ between almost any two trees, and are not
marked. A side with no entry says **missing**, and one that can't be read says
why. An entry the scan could not read fully starts with an **error** row, as
**error │ permission denied**, and the other side's rows move down with it, so
the fields stay side by side.

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

    [x]  left      entries on the left only (-)
    [x]  right     entries on the right only (+)
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
mode, **o** owner and **t** mtime. **\*** turns them all off if any is on, and
all on if none is, as the popup's last line says. **Esc** closes the popup.
Other keys are ignored. Started with **-q**, the **content** row shows **[q]**.
Changing it leaves quick mode for good, reading the contents in full, as quick
mode only saves reading at startup.

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

In git mode, the commit actions follow (see [Commits](#commits)), dimmed for an
entry with no commit on either side:

    m  Commit message  commit that last changed it, as git show
    d  Commit diff     trees before and after that commit
    s  Commit stat     files that commit changed, as git show --stat

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
while unverified entries remain shown. **\*** switches the applied filter
between the two modes. An empty query lists every remaining entry. While a
filter is applied, the status bar names it, as **[filter (diffs): name]**.

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
changed line, and **-** or **+** for a line on one side only. As in the tree,
the marks have their colours. A marked line has a faint background by side, as
in **git diff**, red on the left and green on the right, and the cell opposite a
line on one side only has none. Changed text inside a changed line has a
stronger one. It opens on **Enter** in the tree.

It shows UTF-8 encoded text of up to 10 MiB per side. A side that is not valid
UTF-8, such as a binary file, or is larger, does not open: the tree stays, and a
red box titled **Unable to open** says why, as
**Only UTF-8 encoded text files can be opened (the left side is not).**, wrapped
at 80 columns, or narrower on a narrower terminal, until a key closes it. When
the sides fail differently, the box lists each side's reason on its own line,
starting with **-**. Tree entries open only if each side is a regular file or
missing. Symlinks and special entries in the tree don't open. Once open, as
after an edit, each side that can't be shown says why in its own box instead,
in red, as **Unable to show data that is not UTF-8 encoded.**, and the
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
[Panels](#panels)), read again after an edit.

Text is shown escaped (see [Showing text safely](#showing-text-safely)), with
tabs expanded to 4 columns. The status bar counts the lines changed, and on the
left or right only, as the tree's (**[1 changed] [2 right]**), then the scroll
position. A file that uses CR+LF throughout is marked **CRLF** in the status
bar. If only the line ends differ, the status bar says so. A last line without a
line feed differs from the same line with one.

By default, only changes are shown, with 3 lines of context (see **-U**). Each
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

- **a**: In git mode, show the commit that last changed a side, or compare the
  trees before and after it, from a popup like the tree's Actions (see
  [Commits](#commits)).

- **Esc**: Go back to the tree.

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
if it is an existing regular file. Directories, missing sides, symlinks and
**/dev/null** cannot be edited. If only one side can be edited, **e** opens it
directly and the status bar says which. If neither can, or neither **VISUAL**
nor **EDITOR** is set, a red box titled **Unable to edit** says why. If editing
fails, as when the editor can't be started or exits with an error status, one
titled **Failed to edit** does. Any key closes it, without acting on the screen
behind. **Ctrl+c** closes it and asks to quit.

The editor is run by the unrestricted parent (see [Process
structure](#process-structure)), attached to the terminal, and runs without
Landlock. The child sends the parent which side to edit, and the path relative
to that side's root. The parent opens that path beneath the
root, with **openat2**(2) or, where it is unavailable, one name at a time,
without following symlinks or **..** out of it, and refuses anything but a
regular file. The editor starts in the file's directory, the very one the parent
opened, and is given only the file's name, as **./name**.

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
  **changed=33:leftonly=31:dir=1;34:select=48;5;238**. The values are SGR
  parameters as the terminal takes them, so 16-colour, 256-colour (**38;5;**_N_)
  and truecolour (**38;2;**_R_**;**_G_**;**_B_) values all work. A value may
  contain only digits and **;**, so the variable can't add other escape
  sequences. A reset (**0**, or an empty parameter) is left out, with what comes
  before it, as a style is drawn over others. An unknown key or an invalid value
  prints a warning before the screen opens, and the default stays. See
  [Colours](#colours).

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
- **leftinline**, **rightinline**: the background of changed text inside a
  changed line in the diff view, on the left and on the right, a stronger red
  and green.
- **dir** (1;34): the **dir** type.
- **link** (1;36): the **link** type.
- **pipe** (33): the **pipe** type.
- **device** (1;33): the **char** and **bdev** types.
- **socket** (1;35): the **sock** type.
- **exec** (1;32): the **file** type of a file with an execute bit.
- **leftonly** (31): the **-** mark.
- **rightonly** (32): the **+** mark.
- **unverified** (90): the **?** mark, grey.
- **type** (1;35): the **!** mark. If it is in reverse video, so is the name of
  an entry marked **!**, whose escapes are then underlined.
- **error** (1;31): why a file can't be shown in the diff view or the preview,
  why a checksum failed, and panel fields that could not be read. A directory
  that could not be listed is a note in the tree, in plain text.
- **staged** (32): the first letter of a git status, what is staged.
- **unstaged** (31): the second letter of a git status, what is not staged, and
  both letters of an untracked or unmerged entry.
- **leftbg**, **rightbg**: the background of a marked line in the diff view, on
  the left and on the right, dark red and green. Tree rows have no background
  by state.
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

    leftbg=48;2;46;24;26:rightbg=48;2;22;40;28:leftinline=48;2;104;38;42:
    rightinline=48;2;36;92;48:select=48;2;52;54;60

and in 256 colours:

    leftbg=48;5;52:rightbg=48;5;22:leftinline=48;5;88:rightinline=48;5;28:
    select=48;5;238

On a light terminal, set light ones, such as:

    leftbg=48;2;255;235;233:rightbg=48;2;230;255;236:
    leftinline=48;2;255;192;192:rightinline=48;2;172;242;189:
    select=48;2;214;214;214

(each as one line). **leftinline** and **rightinline** are background colours
and **escape** reverse video, so they never look alike.

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
  them, including files created later, as by an editor saving by renaming a new
  file over the old one.
- Rules are placed on resolved paths: a directory given through a symlink gets
  its rule where the symlink leads, since that is where Landlock looks.
- Nothing may be written, no program run, no TCP connection made or port bound,
  no UDP port bound or datagram sent, and no process outside the child
  signalled, nor an abstract unix socket outside it reached, as far as the
  kernel's Landlock ABI restricts each (see below).
- **/etc/passwd** and **/etc/group**, which name the owners in the panels, are
  read before the restriction when a disk side is shown. A work-tree
  comparison also reads the user's global Git ignore configuration and rules
  before restricting access.
- With **-g** or a **git:** side, before applying the sandbox, the child
  resolves symlinks along each repository's starting directory, possibly
  reading links outside the repository, then finds each repository's top by
  names without reading regular files. It restricts itself to reading beneath
  the tops, and what is compared on disk, before reading repository files, and
  then to their Git directories, common directories and object directories
  before reading their configuration, refs and objects. The second layer
  includes a top when comparing its work tree. Work-tree paths are opened
  relative to that directory without following symlinks.

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

See also [**binate**(1)](binate.1.md), **diff**(1), **sdiff**(1) and
**git-rev-parse**(1).
