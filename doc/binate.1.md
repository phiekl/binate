<!--
SPDX-FileCopyrightText: 2026 Philip Eklöf

SPDX-License-Identifier: MIT
-->

# binate(1)

Inspect file and directory differences in a read-only terminal view.

```
binate [options] left right
binate -g [-C directory] [options] [left [right]]
agitate [-C directory] [options] [left [right]]
```

**binate** shows two directory trees, or two files, side by side, and marks
where they differ. With **-g**, or run as **agitate**, a symlink to it, they are
trees or files of two git revisions, or a revision and the work tree. **binate**
never changes files itself. **e** opens eligible files in your editor. Scanning,
comparing and drawing run in a child process. Where available, Landlock
restricts it from writing and running programs, within the kernel's limits.

*left* and *right* may be directories, files, or one of each. When one input is
a directory, its entry matching the other input's filename is compared, as with
**diff**(1). **/dev/null**, pipes and process substitutions are read as streams,
so **binate** works as **git difftool --no-prompt -x binate**.

See [**binate-manual**(7)](manual.md) for the full reference.

## Options

- **-c**, **--compare** *list*: What to compare, comma-separated, from **left**,
  **right** (entries on one side only), **type**, **content**, **target** (of
  symlinks), **device**, **mode**, **owner** and **mtime**. All but **mtime** by
  default. Items prefixed with **+** or **-** add to or take from the default,
  as **--compare -owner,+mtime**; **none** compares nothing. Leaving out
  **left**, **right** or **type** hides such entries.
- **-U**, **--unified** *n*: Lines of context around changes in the diff view
  (default 3), as **diff -U** does.
- **-x**, **--exclude** *pattern*: Skip entries whose name matches the
  shell-style *pattern*. Repeatable.
- **-q**, **--quick**: Compare files by size and modification time instead of
  contents. Not with **-g**.
- **-g**, **--git**: Compare *left* and *right* as revisions of the git
  repository of the current directory, or of **-C**, such as **main**,
  **HEAD~2** or **v1.0:src**. Use **.** opposite a full-tree revision to compare
  it with the work tree at the repository's top, including untracked files
  allowed by Git's ignore rules. With **-g** or **agitate**, a missing *right*
  defaults to **.**, and a missing *left* defaults to **HEAD**.
- **-C**, **--directory** *directory*: With **-g**, find the repository from
  *directory*, as **git -C** does.
- **-h**, **--help**: Show the options and exit.
- **-v**, **--verbose**: Write debug messages to stderr, including the Landlock
  ABI in effect. Repeat for more.
- **-V**, **--version**: Show the version and exit.

## Marks

The column between the sides marks each pair:

- **=**: No difference.
- **|**: Size, contents, symlink target or device number differ.
- **~**: Mode or owner differ.
- **#**: Modification time differs.
- **\***: Several of these differ.
- **?**: Not verified: a side could not be read, or **-q** could not tell.
- **<**, **>**: On the left, or the right, side only.
- **!**: Different types.

## Keys

**?** lists the keys of the current screen.

### Tree view

- **j**: Move down.
- **k**: Move up.
- **l**: Expand a directory.
- **h**: Collapse a directory, or go to the parent.
- **L**: Expand a directory and everything below it.
- **H**: Collapse a directory and everything below it.
- **+**: Expand every directory.
- **-**: Collapse every directory.
- **n**: Go to the next difference.
- **N**: Go to the previous difference.
- **\***: Show or hide identical entries; while a filter is applied, switch it
  between all and diffs-only mode.
- **Enter**: Open a pair of files in the diff view.
- **F**: Filter by name.
- **/**: Search names.
- **c**: Choose what to compare.
- **a**: Run an action, such as a SHA-256 checksum, or in git mode, showing the
  commit that last changed an entry.
- **r**: Scan again.
- **e**: Edit the selected file.
- **Esc**: Close an action's result, end a search, clear a filter, or leave a
  commit's trees.

### Diff view

- **j**: Scroll down.
- **k**: Scroll up.
- **n**: Go to the next change.
- **N**: Go to the previous change.
- **f**: Show the full files, or only the changes.
- **w**: Wrap long lines, or not.
- **e**: Edit a side.
- **a**: In git mode, show the commit that last changed a side, or its changes.
- **Esc**: Go back to the tree.

### Everywhere

- **Ctrl+c**: Quit, once confirmed.
- **Ctrl+z**: Suspend.

## Environment

- **BINATE_COLORS**: Colours as *key*=*SGR* pairs separated by colons, in the
  style of **LS_COLORS**, such as **changed=33:select=48;5;238**.
- **NO_COLOR**: If set and not empty, turns colour off.
- **COLORTERM**, **TERM**: Whether to use truecolour backgrounds.
- **VISUAL**, **EDITOR**: The editor **e** runs.

## Exit status

**0** after a normal quit, **2** on errors. The status does not say whether the
inputs differ.

See also [**binate-manual**(7)](manual.md), **diff**(1), **sdiff**(1) and **git-rev-parse**(1).
