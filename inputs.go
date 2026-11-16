// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pxy.se/go/binate/internal/files"
)

// inputKind is what a command-line argument names.
type inputKind int

const (
	inputDir    inputKind = iota // a directory, compared as a tree
	inputFile                    // a regular file, read by path
	inputStream                  // anything else, such as /dev/null or a pipe, read once
	inputGit                     // a git revision, resolved by the child
)

// input is one side as given on the command line.
type input struct {
	path string
	kind inputKind
	// inDir is set for a file looked up in the directory given, by the
	// name of the file given opposite it.
	inDir bool
}

// inputs holds the resolved arguments. As with diff, a lone directory
// argument is searched for an entry matching the other side's basename.
type inputs struct {
	left, right input
}

// gitInputs returns the inputs of --git: two revisions, which only the
// child, restricted, reads the repository to resolve.
func gitInputs(left, right string) inputs {
	return inputs{left: input{path: left, kind: inputGit}, right: input{path: right, kind: inputGit}}
}

// dirMode reports whether two directories are compared.
func (in inputs) dirMode() bool {
	return in.left.kind == inputDir && in.right.kind == inputDir
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
	switch {
	case l.kind == inputDir && r.kind != inputDir:
		l, err = statInput(filepath.Join(left, filepath.Base(right)))
		l.inDir = true
	case r.kind == inputDir && l.kind != inputDir:
		r, err = statInput(filepath.Join(right, filepath.Base(left)))
		r.inDir = true
	}
	if err != nil {
		return inputs{}, err
	}
	if l.kind == inputDir && r.kind != inputDir || r.kind == inputDir && l.kind != inputDir {
		return inputs{}, errors.New("unable to compare a directory with a file of the same name that is a directory")
	}
	return inputs{left: l, right: r}, nil
}

func statInput(path string) (input, error) {
	info, err := os.Stat(path)
	if err != nil {
		return input{}, fmt.Errorf("%s: %w", path, files.UnwrapPath(err))
	}
	switch {
	case info.IsDir():
		return input{path: path, kind: inputDir}, nil
	case info.Mode().IsRegular():
		return input{path: path, kind: inputFile}, nil
	}
	return input{path: path, kind: inputStream}, nil
}

// inputCodes are the letters encode gives each kind of input, in capitals
// for a file looked up in the directory given.
const inputCodes = "dfsg"

// encode returns the kinds of both sides, for the child, which takes the
// inputs as the parent resolved them rather than look again: what it
// would find could differ, as by then a pipe's /dev/fd name is the
// parent's.
func (in inputs) encode() string {
	var b strings.Builder
	for _, side := range []input{in.left, in.right} {
		c := inputCodes[side.kind]
		if side.inDir {
			c -= 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// decodeInputs returns the inputs for the arguments left and right, of the
// kinds encode returned.
func decodeInputs(left, right, code string) (inputs, error) {
	if len(code) != 2 {
		return inputs{}, fmt.Errorf("invalid input kinds %q", code)
	}
	sides := [2]input{{path: left}, {path: right}}
	for i := range sides {
		c := code[i]
		lower := c | ('a' - 'A')
		k := strings.IndexByte(inputCodes, lower)
		if k < 0 {
			return inputs{}, fmt.Errorf("invalid input kinds %q", code)
		}
		sides[i].kind, sides[i].inDir = inputKind(k), c != lower
		if sides[i].inDir && sides[i].kind == inputDir {
			return inputs{}, fmt.Errorf("invalid input kinds %q", code)
		}
	}
	l, r := &sides[0], &sides[1]
	switch {
	case l.inDir && r.inDir:
		return inputs{}, fmt.Errorf("invalid input kinds %q", code)
	case l.inDir:
		l.path = filepath.Join(left, filepath.Base(right))
	case r.inDir:
		r.path = filepath.Join(right, filepath.Base(left))
	}
	return inputs{left: *l, right: *r}, nil
}
