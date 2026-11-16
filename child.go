// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/app"
	"pxy.se/go/binate/internal/cli"
	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/git"
	"pxy.se/go/binate/internal/landlock"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/stat"
	"pxy.se/go/binate/internal/supervise"
	"pxy.se/go/binate/internal/term"
)

// childSpec is what the parent passes in childEnv: the socket's file
// descriptor, those of the left and right streams, or -1, and the kinds of
// the inputs (see inputs.encode).
type childSpec struct {
	sock        int
	left, right int
	kinds       string
}

func parseChildSpec(s string) (childSpec, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return childSpec{}, fmt.Errorf("invalid %s %q", childEnv, s)
	}
	var fds [3]int
	for i, p := range parts[:3] {
		n, err := strconv.Atoi(p)
		if err != nil || n < -1 || (i == 0 && n < 3) {
			return childSpec{}, fmt.Errorf("invalid %s %q", childEnv, s)
		}
		fds[i] = n
	}
	return childSpec{sock: fds[0], left: fds[1], right: fds[2], kinds: parts[3]}, nil
}

// child is the restricted process's setup, before the screen opens.
type child struct {
	log     *slog.Logger
	opts    options
	in      inputs
	client  *supervise.Client
	streams [2]*os.File
	// repo is the repository of --git, found before the process is
	// restricted, and gitFiles the revisions' files, if files.
	repo     *git.Location
	gitFiles [2]*git.Root
}

func runChild(specText string) int {
	// The child runs no programs, but don't leave the marker around.
	_ = os.Unsetenv(childEnv)

	_, o, err := parseOptions(os.Args[1:])
	if err != nil {
		return fail("usage: %v", err)
	}
	log := cli.NewLogger(os.Stderr, o.verbose)
	spec, err := parseChildSpec(specText)
	if err != nil {
		return fail("%v", err)
	}
	sock, err := socketFile(spec.sock)
	if err != nil {
		return fail("%v", err)
	}
	in, err := decodeInputs(o.left, o.right, spec.kinds)
	if err != nil {
		return fail("%v", err)
	}
	c := &child{log: log, opts: o, in: in, client: supervise.NewClient(sock)}
	for i, fd := range []int{spec.left, spec.right} {
		if fd >= 0 {
			c.streams[i] = os.NewFile(uintptr(fd), "stream")
		}
	}

	// The terminal's signals reach the whole foreground process group,
	// this process too. Ctrl+c and Ctrl+\ are keys in raw mode; ignore
	// them while the terminal is not in raw mode, as while editing.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGINT, syscall.SIGQUIT)

	return c.run()
}

// socketFile checks that fd is the socket from the parent.
func socketFile(fd int) (*os.File, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return nil, errors.New("not started by binate: no socket to the parent")
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "parent"), nil
}

// policy returns what the child may read (see doc/manual.md#landlock).
func (c *child) policy() landlock.Policy {
	var p landlock.Policy
	if c.repo != nil {
		p.Dirs = append(p.Dirs, c.repo.ReadDirs()...)
	}
	for _, side := range []input{c.in.left, c.in.right} {
		switch side.kind {
		case inputDir:
			p.Dirs = append(p.Dirs, side.path)
		case inputFile:
			p.Files = append(p.Files, side.path)
		}
	}
	return p
}

// restrict applies Landlock. Everything the child needs that is not
// beneath the policy's paths must be open by now: the terminal and the
// streams.
func (c *child) restrict() error {
	rules, err := c.policy().Rules()
	if err != nil {
		return err
	}
	return landlock.Restrict(c.log, rules...)
}

// run takes the terminal, restricts the process and runs the screens.
// What can fail before the screens run, as resolving git revisions, is
// done before the terminal is taken, so that its error is plain to see.
func (c *child) run() (code int) {
	t, err := term.Open(os.Stdin, os.Stdout)
	if err != nil {
		return fail("opening the terminal: %v", err)
	}
	a := app.New(c.log, t, c.client)
	if c.opts.git {
		// Before restricting the process, resolve the starting directory's
		// symlinks, possibly reading links outside the repository, and
		// find the top by names. Repository files are then read under a
		// first layer allowing only the top, before the next allows only
		// the repository. A git revision has no owners to name.
		dir := cmp.Or(c.opts.directory, ".")
		top, err := git.FindTop(dir, os.Getenv)
		if err != nil {
			return fail("%v", err)
		}
		rules, err := landlock.Policy{Dirs: []string{top}}.Rules()
		if err != nil {
			return fail("%v", err)
		}
		if err := landlock.Restrict(c.log, rules...); err != nil {
			return fail("restricting the process: %v", err)
		}
		if c.repo, err = git.Discover(top, dir, os.Getenv); err != nil {
			return fail("%v", err)
		}
	} else {
		a.Names = stat.LoadNames()
	}
	if err := c.restrict(); err != nil {
		return fail("restricting the process: %v", err)
	}
	var roots [2]source.Root
	if c.opts.git {
		if roots, err = c.gitRoots(); err != nil {
			return fail("%v", err)
		}
	} else if c.in.dirMode() {
		roots = [2]source.Root{source.Disk(c.in.left.path), source.Disk(c.in.right.path)}
	}

	if err := a.Enter(); err != nil {
		return fail("entering raw mode: %v", err)
	}
	// Leave the screen before a panic's message is written.
	defer func() {
		if r := recover(); r != nil {
			_ = a.Leave()
			panic(r)
		}
	}()
	a.HasEditor = len(editorCommand()) > 0
	styles := colors.Parse(colors.FromEnv(os.Getenv)).Styles
	a.Styles = styles
	if roots[0] != nil {
		a.Main = app.NewTreeScreen(a, app.TreeConfig{
			Styles: styles,
			Home:   os.Getenv("HOME"),
			Left:   roots[0],
			Right:  roots[1],
			Options: scan.Options{
				Compare:  c.opts.compare,
				Quick:    c.opts.quick,
				Excludes: c.opts.excludes,
			},
			Context: c.opts.context,
		})
	} else {
		d := app.NewDiffScreen(a, app.DiffConfig{
			Styles:  styles,
			Home:    os.Getenv("HOME"),
			Context: c.opts.context,
			Left:    c.source(0),
			Right:   c.source(1),
			Compare: c.opts.compare,
		})
		d.Start()
		a.Main = d
	}
	code = a.Run()
	_ = a.Leave()
	return code
}

// gitRoots opens the repository and resolves the revisions: two trees,
// returned to compare as such, or two files, kept for source to return.
func (c *child) gitRoots() ([2]source.Root, error) {
	repo, err := git.Open(c.repo)
	if err != nil {
		return [2]source.Root{}, err
	}
	for i, side := range []input{c.in.left, c.in.right} {
		target, err := repo.Resolve(side.path)
		if err != nil {
			return [2]source.Root{}, err
		}
		c.gitFiles[i] = git.NewTargetRoot(repo.Repo, target, side.path)
	}
	l, r := c.gitFiles[0], c.gitFiles[1]
	switch {
	case l.IsFile() != r.IsFile():
		return [2]source.Root{}, fmt.Errorf("unable to compare a tree with a file: %s and %s", c.in.left.path, c.in.right.path)
	case l.IsFile():
		return [2]source.Root{}, nil
	}
	return [2]source.Root{l, r}, nil
}

// source returns side i (0 left, 1 right) for the diff view. A stream is read
// once, by the diff view, in the background.
func (c *child) source(i int) app.Source {
	in := []input{c.in.left, c.in.right}[i]
	if f := c.gitFiles[i]; f != nil {
		return app.Source{Root: f}
	}
	src := app.Source{Root: source.Disk(in.path)}
	if f := c.streams[i]; f != nil {
		src.Stream, src.Pipe = true, f
		src.Stat, src.StatErr = stat.ReadFile(f)
	}
	return src
}
