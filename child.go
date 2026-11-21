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
	"pxy.se/go/binate/internal/view"
)

// childSpec is what the parent passes in childEnv: the socket's file
// descriptor and the kinds of the inputs (see inputs.encode).
type childSpec struct {
	sock  int
	kinds string
}

func parseChildSpec(s string) (childSpec, error) {
	sock, kinds, ok := strings.Cut(s, ",")
	n, err := strconv.Atoi(sock)
	if !ok || err != nil || n < 3 {
		return childSpec{}, fmt.Errorf("invalid %s %q", childEnv, s)
	}
	return childSpec{sock: n, kinds: kinds}, nil
}

// child is the restricted process's setup, before the screen opens.
type child struct {
	log    *slog.Logger
	opts   options
	in     inputs
	client *supervise.Client
	// repo is the repository of --git, found before the process is
	// restricted.
	repo   *git.Location
	ignore git.GlobalIgnore
}

func runChild(specText string) int {
	// The child runs no programs, but don't leave the marker around.
	_ = os.Unsetenv(childEnv)

	_, o, err := parseOptions(programName(), os.Args[1:])
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
		if c.in.hasWorkTree() {
			p.Dirs = append(p.Dirs, c.repo.WorkTree)
		}
	}
	for _, side := range []input{c.in.left, c.in.right} {
		if side.kind == inputDir {
			p.Dirs = append(p.Dirs, side.path)
		}
	}
	return p
}

// restrict applies Landlock. Everything the child needs that is not
// beneath the policy's paths must be open by now, as the terminal.
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
		// first layer allowing only the top, before the next allows the
		// git directories and, when compared, the work tree.
		dir := cmp.Or(c.opts.directory, ".")
		top, err := git.FindTop(dir, os.Getenv)
		if err != nil {
			return fail("%v", err)
		}
		if c.in.hasWorkTree() {
			c.ignore, err = git.ReadGlobalIgnore(os.Getenv)
			if err != nil {
				return fail("reading global git ignore rules: %v", err)
			}
			a.Names = stat.LoadNames()
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
		if (c.in.hasWorkTree() || c.in.hasIndex()) && c.repo.WorkTree == "" {
			return fail("the repository has no work tree")
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
		// The revisions are of the repository at the top: its work tree,
		// or the git directory of a bare one.
		a.Header = view.Header{Label: "(git)", Path: cmp.Or(c.repo.WorkTree, c.repo.GitDir), Home: os.Getenv("HOME")}
	} else {
		roots = [2]source.Root{diskRoot(c.in.left), diskRoot(c.in.right)}
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
	code = a.Run()
	_ = a.Leave()
	return code
}

// gitRoots resolves two trees, including a work tree or an index.
func (c *child) gitRoots() ([2]source.Root, error) {
	repo, err := git.Open(c.repo)
	if err != nil {
		return [2]source.Root{}, err
	}
	var roots [2]source.Root
	work := -1
	// Opposite the work tree or the index, a revision is of the whole
	// repository.
	local := c.in.hasWorkTree() || c.in.hasIndex()
	for i, side := range []input{c.in.left, c.in.right} {
		switch side.kind {
		case inputWorkTree:
			work = i
			continue
		case inputIndex:
			index, err := repo.OpenIndex()
			if err != nil {
				return [2]source.Root{}, err
			}
			roots[i] = index
			continue
		}
		target, err := repo.Resolve(side.path)
		if err != nil && local && (side.path == "HEAD" || side.path == "@") {
			// Before the first commit, compare an empty tree, as git
			// status lists every file as new.
			if unborn, uerr := repo.Unborn(); uerr == nil && unborn {
				roots[i] = git.NewTargetRoot(repo.Repo, git.Target{Mode: git.ModeTree}, "(no commits)")
				continue
			}
		}
		if err != nil {
			return [2]source.Root{}, err
		}
		switch {
		case !target.IsTree():
			return [2]source.Root{}, fmt.Errorf("unable to compare %s: the revision names a file, and binate compares trees: compare two files with git diff", side.path)
		case local && target.Path != "":
			other := "the work tree"
			if !c.in.hasWorkTree() {
				other = "the index"
			}
			return [2]source.Root{}, fmt.Errorf("unable to compare %s with %s: the revision must name a full tree", side.path, other)
		}
		roots[i] = git.NewTargetRoot(repo.Repo, target, side.path)
	}
	if work >= 0 {
		tree, err := repo.OpenWorkTree(c.ignore)
		if err != nil {
			return [2]source.Root{}, err
		}
		roots[work] = tree
	}
	return roots, nil
}

// diskRoot returns the root of side, on disk: a directory, or nothing.
func diskRoot(side input) source.Root {
	if side.kind == inputNull {
		return source.Nothing(side.path)
	}
	return source.Disk(side.path)
}
