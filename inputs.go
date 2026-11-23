// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/formats/archive"
)

// inputKind is what a command-line argument names.
type inputKind int

const (
	inputDir      inputKind = iota // a directory, compared as a tree
	inputNull                      // /dev/null, nothing: an empty tree
	inputGit                       // a git revision, resolved by the child
	inputWorkTree                  // the repository's work tree, resolved by the child
	inputIndex                     // the repository's index, resolved by the child
	inputArchive                   // an archive, a file or pipe, read by the child
)

// input is one side as given on the command line.
type input struct {
	// path is a path on disk, of an archive's file for an archive, or of a
	// side of git, what to compare in the repository: a revision, "." or
	// ":".
	path string
	kind inputKind
	// format is an archive's format, its backend, as "tar".
	format string
	// repo is the directory a side of git in another repository than the
	// current one is found from, and at where the argument's @ before
	// what to compare is, else -1.
	repo string
	at   int
}

// isGit reports whether the side is of git: a revision, a work tree or an
// index.
func (i input) isGit() bool {
	return i.kind == inputGit || i.kind == inputWorkTree || i.kind == inputIndex
}

// inputs holds the resolved arguments.
type inputs struct {
	left, right input
}

// gitBackend is the backend of git sides, given as "git:" without --git.
const gitBackend = "git"

// archiveFormats are the formats of archives read, by the backends that
// name them, given as "tar:" without --git, and their readers.
var archiveFormats = map[string]archive.Reader{}

// archiveBackends returns the backends of archives, sorted.
func archiveBackends() []string {
	return slices.Sorted(maps.Keys(archiveFormats))
}

// backendList returns the backends, of git and archives, as "git, tar
// or zip".
func backendList() string {
	names := append([]string{gitBackend}, archiveBackends()...)
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// archiveUsage returns the sides of archives as the usage lists them,
// each followed by a comma, as "tar:file, ".
func archiveUsage() string {
	var b strings.Builder
	for _, name := range archiveBackends() {
		fmt.Fprintf(&b, "%s:file, ", name)
	}
	return b.String()
}

// isArchive reports whether the side is an archive, read by the child.
func (i input) isArchive() bool {
	return i.kind == inputArchive
}

// backendOf returns the backend an argument names a side of, before a
// colon, as a URL's scheme: a letter, then letters, digits, "+", "-" and
// ".". Without one, the argument is a path.
func backendOf(arg string) (string, bool) {
	name, _, ok := strings.Cut(arg, ":")
	if !ok || name == "" {
		return "", false
	}
	for i, r := range name {
		letter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !letter && (i == 0 || !(r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.')) {
			return "", false
		}
	}
	return name, true
}

// inOtherRepo reports whether arg, given with --git, names a side in
// another repository, by a path, as no revision starts: "/", "~", or "."
// but for "." alone, the work tree.
func inOtherRepo(arg string) bool {
	return arg != "." && (strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~") || strings.HasPrefix(arg, "."))
}

// repoStart returns where the directory of a side in another repository
// starts in arg: after the backend's name and colon without --git.
func repoStart(arg string, git bool) int {
	if git {
		return 0
	}
	name, _ := backendOf(arg)
	return len(name) + 1
}

// expandHome expands a leading "~" or "~/" in p to $HOME, as the shell
// leaves it after "git:".
func expandHome(p string) string {
	if home := os.Getenv("HOME"); home != "" && (p == "~" || strings.HasPrefix(p, "~/")) {
		return home + p[1:]
	}
	return p
}

// splitRepo returns where the @ after the directory of a side in another
// repository is in arg, the directory starting at start: the last @ with a
// directory before it, as found from base if relative and base is set.
func splitRepo(arg string, start int, base string) (int, error) {
	for at := strings.LastIndexByte(arg, '@'); at >= start; at = strings.LastIndexByte(arg[:at], '@') {
		dir := expandHome(arg[start:at])
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) && base != "" {
			dir = base + string(filepath.Separator) + dir
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return at, nil
		}
	}
	return -1, fmt.Errorf("%s: no directory before an @ to find a repository from, as in ~/src/app@main", arg)
}

// gitSide returns the side of git arg names, in the repository of the
// directory up to at, if at isn't -1, else in the current one.
func gitSide(arg string, at int, git bool) input {
	side := input{path: arg, at: at}
	if at >= 0 {
		side.repo = expandHome(arg[repoStart(arg, git):at])
		side.path = cmp.Or(arg[at+1:], "HEAD")
	}
	switch side.path {
	case ".":
		side.kind = inputWorkTree
	case ":":
		side.kind = inputIndex
	default:
		side.kind = inputGit
	}
	return side
}

// parseInputs returns the sides left and right name. With git, they are
// sides of git, of the current repository or, by their paths, others,
// found from base if relative. Without, they are paths, or sides of a
// backend: git, or an archive's (see archiveFormats).
func parseInputs(left, right string, git bool, base string) (inputs, error) {
	var sides [2]input
	var onDisk [2]bool
	for i, arg := range []string{left, right} {
		switch name, ok := backendOf(arg); {
		case git && !inOtherRepo(arg):
			sides[i] = gitSide(arg, -1, git)
			continue
		case !git && !ok:
			onDisk[i] = true
			continue
		case !git && archiveFormats[name] != nil:
			side, err := statArchive(arg, name)
			if err != nil {
				return inputs{}, err
			}
			sides[i] = side
			continue
		case !git && name != gitBackend:
			return inputs{}, fmt.Errorf("%s: unknown backend %q, not %s: give a path with a colon as ./%s, or a revision with --git", arg, name, backendList(), arg)
		}
		at, err := splitRepo(arg, repoStart(arg, git), base)
		if err != nil {
			return inputs{}, err
		}
		sides[i] = gitSide(arg, at, git)
	}
	switch {
	case onDisk[0] && onDisk[1]:
		return resolveInputs(left, right)
	case sides[0].kind == inputWorkTree && sides[1].kind == inputWorkTree:
		return inputs{}, errors.New("unable to compare two work trees: only one side can show statuses")
	}
	for i, arg := range []string{left, right} {
		if onDisk[i] {
			side, err := statInput(arg)
			if err != nil {
				return inputs{}, err
			}
			sides[i] = side
		}
	}
	return inputs{left: sides[0], right: sides[1]}, nil
}

// withCached returns in as --cached makes it: left a revision, compared
// with the index of its repository, which right, given as ":", is made.
// It takes left's repository as found, by the parent, and never looks for
// it again, so that the child's is the parent's.
func (in inputs) withCached() (inputs, error) {
	if in.left.kind != inputGit {
		arg := in.left.path
		if in.left.at >= 0 {
			arg = in.left.repo + "@" + arg
		}
		return inputs{}, fmt.Errorf("--cached compares a revision with the index: %s is not one", arg)
	}
	in.right.repo = in.left.repo
	return in, nil
}

func (in inputs) sides() [2]input {
	return [2]input{in.left, in.right}
}

// hasGit reports whether a side is of git.
func (in inputs) hasGit() bool {
	return in.left.isGit() || in.right.isGit()
}

// hasDisk reports whether a side is on disk.
func (in inputs) hasDisk() bool {
	return !in.left.isGit() || !in.right.isGit()
}

func (in inputs) hasWorkTree() bool {
	return in.left.kind == inputWorkTree || in.right.kind == inputWorkTree
}

func (in inputs) hasIndex() bool {
	return in.left.kind == inputIndex || in.right.kind == inputIndex
}

func resolveInputs(left, right string) (inputs, error) {
	l, err := statInput(left)
	if err != nil {
		return inputs{}, err
	}
	r, err := statInput(right)
	if err != nil {
		return inputs{}, err
	}
	if l.kind == inputNull && r.kind == inputNull {
		return inputs{}, errors.New("unable to compare /dev/null with itself: give a directory")
	}
	return inputs{left: l, right: r}, nil
}

// statInput returns the side path names, following symlinks: a directory,
// or /dev/null, the null device by any name, as a symlink to it, which is
// checked without opening it. Anything else is refused, a file too.
func statInput(path string) (input, error) {
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return input{}, fmt.Errorf("%s: %w", path, files.UnwrapPath(err))
	case info.IsDir():
		return input{path: path, kind: inputDir, at: -1}, nil
	case isNull(info):
		return input{path: path, kind: inputNull, at: -1}, nil
	case info.Mode().IsRegular():
		return input{}, fmt.Errorf("%s: a file, and binate compares directories: compare two files with diff", path)
	}
	return input{}, fmt.Errorf("%s: not a directory, which binate compares, or /dev/null for an empty one", path)
}

// archivePath returns the path of the archive arg, of backend, as tar:,
// names, with a leading "~" expanded, as the shell leaves it after "tar:".
func archivePath(arg, backend string) string {
	return expandHome(arg[len(backend)+1:])
}

// statArchive returns the side of backend, an archive's, arg names: a file,
// or a pipe, as from a process substitution, following symlinks. A
// directory, a device or a socket is refused.
func statArchive(arg, backend string) (input, error) {
	path := archivePath(arg, backend)
	if path == "" {
		return input{}, fmt.Errorf("%s: no archive after %s:, as %s:old.%s", arg, backend, backend, backend)
	}
	info, err := os.Stat(path)
	switch {
	case err != nil:
		return input{}, fmt.Errorf("%s: %w", path, files.UnwrapPath(err))
	case info.Mode().IsRegular() || info.Mode()&fs.ModeNamedPipe != 0:
		return input{path: path, kind: inputArchive, format: backend, at: -1}, nil
	case info.IsDir():
		return input{}, fmt.Errorf("%s: a directory: give it without the %s: prefix", path, backend)
	}
	return input{}, fmt.Errorf("%s: not a file or a pipe, which %s: reads an archive from", path, backend)
}

// isNull reports whether info is of the null device, character device 1:3
// on Linux.
func isNull(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&fs.ModeCharDevice == 0 || info.Mode()&fs.ModeDevice == 0 {
		return false
	}
	rdev := uint64(st.Rdev) // uint32 on mips64
	return unix.Major(rdev) == 1 && unix.Minor(rdev) == 3
}

// inputCodes are the letters encode gives each kind of input.
const inputCodes = "dngwia"

// encode returns the kinds of both sides, and where each side of git in
// another repository has the @ after its directory, or -1, for the child,
// which takes the inputs as the parent resolved them rather than look
// again: what it would find could differ, as a directory replaced since.
func (in inputs) encode() string {
	return fmt.Sprintf("%c%c,%d,%d", inputCodes[in.left.kind], inputCodes[in.right.kind], in.left.at, in.right.at)
}

// decodeInputs returns the inputs for the arguments left and right, as
// encode gave them in code, with git as given.
func decodeInputs(left, right, code string, git bool) (inputs, error) {
	kinds, ats, _ := strings.Cut(code, ",")
	atL, atR, _ := strings.Cut(ats, ",")
	if len(kinds) != 2 {
		return inputs{}, fmt.Errorf("invalid input kinds %q", code)
	}
	args := [2]string{left, right}
	var sides [2]input
	for i, text := range []string{atL, atR} {
		k := strings.IndexByte(inputCodes, kinds[i])
		at, err := strconv.Atoi(text)
		if k < 0 || err != nil || at < -1 || at >= len(args[i]) || at >= 0 && (args[i][at] != '@' || at < repoStart(args[i], git)) {
			return inputs{}, fmt.Errorf("invalid input kinds %q", code)
		}
		kind := inputKind(k)
		switch {
		case kind == inputGit || kind == inputWorkTree || kind == inputIndex:
			sides[i] = gitSide(args[i], at, git)
			name, _ := backendOf(args[i])
			if sides[i].kind != kind || !git && (at < 0 || name != gitBackend) {
				return inputs{}, fmt.Errorf("invalid input kinds %q", code)
			}
			continue
		case at >= 0:
			return inputs{}, fmt.Errorf("invalid input kinds %q", code)
		case kind == inputArchive:
			name, ok := backendOf(args[i])
			if git || !ok || archiveFormats[name] == nil {
				return inputs{}, fmt.Errorf("invalid input kinds %q", code)
			}
			sides[i] = input{path: archivePath(args[i], name), kind: kind, format: name, at: -1}
			continue
		}
		sides[i] = input{path: args[i], kind: kind, at: -1}
	}
	return inputs{left: sides[0], right: sides[1]}, nil
}
