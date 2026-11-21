// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
)

// inputKind is what a command-line argument names.
type inputKind int

const (
	inputDir      inputKind = iota // a directory, compared as a tree
	inputNull                      // /dev/null, nothing: an empty tree
	inputGit                       // a git revision, resolved by the child
	inputWorkTree                  // the repository's work tree, resolved by the child
	inputIndex                     // the repository's index, resolved by the child
)

// input is one side as given on the command line.
type input struct {
	path string
	kind inputKind
}

// inputs holds the resolved arguments.
type inputs struct {
	left, right input
}

// gitInputs returns the inputs of --git. The child resolves them in the
// repository it discovers.
func gitInputs(left, right string) inputs {
	side := func(arg string) input {
		switch arg {
		case ".":
			return input{path: arg, kind: inputWorkTree}
		case ":":
			return input{path: arg, kind: inputIndex}
		}
		return input{path: arg, kind: inputGit}
	}
	return inputs{left: side(left), right: side(right)}
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
		return input{path: path, kind: inputDir}, nil
	case isNull(info):
		return input{path: path, kind: inputNull}, nil
	case info.Mode().IsRegular():
		return input{}, fmt.Errorf("%s: a file, and binate compares directories: compare two files with diff", path)
	}
	return input{}, fmt.Errorf("%s: not a directory, which binate compares, or /dev/null for an empty one", path)
}

// isNull reports whether info is of the null device, character device 1:3
// on Linux.
func isNull(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode()&fs.ModeCharDevice != 0 && info.Mode()&fs.ModeDevice != 0 &&
		unix.Major(st.Rdev) == 1 && unix.Minor(st.Rdev) == 3
}

// inputCodes are the letters encode gives each kind of input.
const inputCodes = "dngwi"

// encode returns the kinds of both sides, for the child, which takes the
// inputs as the parent resolved them rather than look again: what it
// would find could differ, as a directory replaced since.
func (in inputs) encode() string {
	return string([]byte{inputCodes[in.left.kind], inputCodes[in.right.kind]})
}

// decodeInputs returns the inputs for the arguments left and right, of the
// kinds encode returned.
func decodeInputs(left, right, code string) (inputs, error) {
	if len(code) != 2 {
		return inputs{}, fmt.Errorf("invalid input kinds %q", code)
	}
	sides := [2]input{{path: left}, {path: right}}
	for i := range sides {
		k := strings.IndexByte(inputCodes, code[i])
		if k < 0 {
			return inputs{}, fmt.Errorf("invalid input kinds %q", code)
		}
		sides[i].kind = inputKind(k)
	}
	return inputs{left: sides[0], right: sides[1]}, nil
}
