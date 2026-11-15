<!--
SPDX-FileCopyrightText: 2026 Philip Eklöf

SPDX-License-Identifier: MIT
-->

# binate

binate is a read-only terminal viewer for differences between files or
directory trees. It never writes, and the process that scans and
draws is sandboxed with Landlock so it can't write or run programs, and,
as far as the kernel's Landlock allows, can't open network connections.
The only way a file changes is through your own editor.

```
$ binate ~/src/old ~/src/new
$ binate -q /mnt/backup ~/photos
$ git difftool --no-prompt -x binate
```

binate is Linux-only. The Landlock protection depends on the kernel, and
binate applies what the kernel has without failing: truncating a file
is denied only from Landlock ABI 3 (Linux 6.2), TCP from ABI 4
(Linux 6.7), UDP only from ABI 10, and other network protocols not at
all, so even on a recent kernel the network protection is partial. On a
kernel without Landlock binate runs unprotected. `-v` tells which
Landlock ABI is in effect (see
[binate(1)](doc/binate.1.md#landlock)).


## Usage

See [binate(1)](doc/binate.1.md) for options, keys, marks, colours and how
the sandbox works. The column between the sides marks each pair with
`=` (no difference), `|` (data differs), `~` (mode or owner differs),
`#` (mtime differs), `*` (several kinds differ), `?` (not verified),
`<` / `>` (one side only) or `!` (different types).
Enter opens a pair of files. `n` and `N` jump between differences,
`F` filters, `/` searches, `e` edits in your editor and `a` runs actions,
such as SHA-256, SHA-384 or SHA-512 checksums of both sides.
`-c` or `--compare` chooses what the tree compares. `binate --help` lists
the options, and `?` in binate lists the keys of the current screen.


## Building and testing

```
make            # build/binate
make test       # unit and end-to-end tests
make vet        # go vet
make man        # the man page in build/man/, requires go-md2man
make fuzz       # all fuzz targets, FUZZTIME=10s by default
staticcheck ./...
```

The build targets x86-64-v3 CPUs (roughly 2013 and later) by default.
`make GOAMD64=v1` can be used for older CPUs.

The man page is written in [doc/binate.1.md](doc/binate.1.md). Install
[go-md2man](https://github.com/cpuguy83/go-md2man) with your package
manager or `go install github.com/cpuguy83/go-md2man/v2@v2.0.7`, and read
the result with `man -l build/man/binate.1`. Its footer date defaults to
the last commit date of the Markdown source. Set `MAN_DATE=YYYY-MM-DD` when
building without Git metadata.

binate has no TUI library: the terminal layer is written on
`golang.org/x/term` and `golang.org/x/sys`. `go-landlock` provides the
sandbox. The line diff is Go's `internal/diff`, changed to return line
operations (BSD-3-Clause, see [LICENSES](LICENSES)).

See [doc/CONTRIBUTING.md](doc/CONTRIBUTING.md) for conventions and
[doc/testing.md](doc/testing.md) for what the tests cover.

## Acknowledgements

Inspired by [duodiff](https://github.com/akunzai/duodiff), a similar tool that
allows merging and synchronization of files and directories, while binate
focuses on sandboxed read-only comparison.
