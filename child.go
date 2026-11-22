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
	"path/filepath"
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
	"pxy.se/go/binate/internal/seccomp"
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
	// The kinds, last, hold commas of their own.
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
	// locs are the repositories of the sides of git, found before the
	// process is restricted, the same for sides found from the same
	// directory, and nil for a side on disk.
	locs   [2]*git.Location
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
	in, err := decodeInputs(o.left, o.right, spec.kinds, o.git)
	if err == nil && o.cached {
		in, err = in.withCached()
	}
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
	p := c.diskPolicy()
	for i, side := range c.in.sides() {
		if loc := c.locs[i]; loc != nil {
			p.Dirs = append(p.Dirs, loc.ReadDirs()...)
			if side.kind == inputWorkTree {
				p.Dirs = append(p.Dirs, loc.WorkTree)
			}
		}
	}
	return p
}

// diskPolicy returns what the child may read of the sides on disk.
func (c *child) diskPolicy() landlock.Policy {
	var p landlock.Policy
	for _, side := range c.in.sides() {
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
	// The terminal, which Landlock leaves alone, can't be made to type
	// into the shell, before anything is read.
	if err := seccomp.Restrict(c.log); err != nil {
		return fail("restricting the process: %v", err)
	}
	t, err := term.Open(os.Stdin, os.Stdout)
	if err != nil {
		return fail("opening the terminal: %v", err)
	}
	a := app.New(c.log, t, c.client)
	if c.in.hasDisk() || c.in.hasWorkTree() {
		a.Names = stat.LoadNames()
	}
	if c.in.hasWorkTree() {
		c.ignore, err = git.ReadGlobalIgnore(os.Getenv)
		if err != nil {
			return fail("reading global git ignore rules: %v", err)
		}
	}
	if c.in.hasGit() {
		if err := c.discover(); err != nil {
			return fail("%v", err)
		}
	}
	if err := c.restrict(); err != nil {
		return fail("restricting the process: %v", err)
	}
	var roots [2]source.Root
	if c.in.hasGit() {
		if roots, err = c.gitRoots(); err != nil {
			return fail("%v", err)
		}
		a.Header = c.header()
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

// repoDir returns the directory the repository of side, of git, is found
// from: its own, relative to -C, or else -C or the current directory.
func (c *child) repoDir(side input) string {
	if side.repo == "" {
		return cmp.Or(c.opts.directory, ".")
	}
	if !filepath.IsAbs(side.repo) && c.opts.directory != "" {
		// Not cleaned, for .. to follow a symlink as Git does.
		return c.opts.directory + string(filepath.Separator) + side.repo
	}
	return side.repo
}

// discover finds the repositories of the sides of git. Before restricting
// the process, it resolves the symlinks of each one's starting directory,
// possibly reading links outside the repository, and finds its top by
// names. Repository files are then read under a first layer allowing only
// the tops and the sides on disk, before the next allows the git
// directories and, where compared, the work trees.
func (c *child) discover() error {
	// Each repository is found from the directory its sides start in.
	tops := map[string]string{}
	var starts []string
	for _, side := range c.in.sides() {
		if !side.isGit() {
			continue
		}
		s := c.repoDir(side)
		if _, ok := tops[s]; ok {
			continue
		}
		top, err := git.FindTop(s, os.Getenv)
		if err != nil {
			return err
		}
		tops[s] = top
		starts = append(starts, s)
	}
	p := c.diskPolicy()
	for _, s := range starts {
		p.Dirs = append(p.Dirs, tops[s])
	}
	rules, err := p.Rules()
	if err != nil {
		return err
	}
	if err := landlock.Restrict(c.log, rules...); err != nil {
		return fmt.Errorf("restricting the process: %w", err)
	}
	locs := map[string]*git.Location{}
	for _, s := range starts {
		if locs[s], err = git.Discover(tops[s], s); err != nil {
			return err
		}
	}
	sides := c.in.sides()
	for i, side := range sides {
		if !side.isGit() {
			continue
		}
		loc := locs[c.repoDir(side)]
		c.locs[i] = loc
		if (side.kind == inputWorkTree || side.kind == inputIndex) && loc.WorkTree == "" {
			if side.repo != "" {
				return fmt.Errorf("%s: the repository has no work tree", side.repo)
			}
			return errors.New("the repository has no work tree")
		}
	}
	if l, r := c.locs[0], c.locs[1]; l != nil && r != nil && sides[0].kind == sides[1].kind && sameDir(l.GitDir, r.GitDir) {
		switch sides[0].kind {
		case inputWorkTree:
			return errors.New("a work tree must be compared with a revision or the index")
		case inputIndex:
			return errors.New("the index must be compared with a revision or the work tree")
		}
	}
	return nil
}

// sameDir reports whether the directories a and b are the same, as where
// a symlink to one is the other.
func sameDir(a, b string) bool {
	if a == b {
		return true
	}
	ai, aerr := os.Stat(a)
	bi, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(ai, bi)
}

// gitRoots resolves the sides of git, each in its repository, to trees,
// including a work tree or an index, and returns them with the sides on
// disk.
func (c *child) gitRoots() ([2]source.Root, error) {
	repos := map[*git.Location]*git.Repository{}
	var roots [2]source.Root
	sides := c.in.sides()
	// Opposite a work tree or an index, a revision is of a whole
	// repository.
	local := c.in.hasWorkTree() || c.in.hasIndex()
	for i, side := range sides {
		loc := c.locs[i]
		if loc == nil {
			continue
		}
		repo := repos[loc]
		if repo == nil {
			var err error
			if repo, err = git.Open(loc); err != nil {
				return [2]source.Root{}, err
			}
			repos[loc] = repo
		}
		switch side.kind {
		case inputWorkTree:
			tree, err := repo.OpenWorkTree(c.ignore)
			if err != nil {
				return [2]source.Root{}, err
			}
			roots[i] = tree
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
			return [2]source.Root{}, gitPrefixHint(side, err)
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
	for i, side := range sides {
		switch {
		case roots[i] != nil:
		case side.kind == inputNull:
			roots[i] = source.Nothing(side.path)
		default:
			// A directory opposite git has the modes git records.
			roots[i] = source.GitModes{Disk: source.Disk(side.path)}
		}
	}
	return roots, nil
}

// gitPrefixHint returns err, of resolving the revision of side, with a
// hint where it looks like a side in another repository given with git:,
// as without -g. With -g, git: starts a revision's path, as on a branch
// git, and the side is given without it, starting with ./ if relative, as
// app@main would be a revision.
func gitPrefixHint(side input, err error) error {
	rest, ok := strings.CutPrefix(side.path, "git:")
	if side.repo != "" || !ok || !strings.Contains(rest, "@") {
		return err
	}
	if !inOtherRepo(rest) {
		rest = "./" + rest
	}
	return fmt.Errorf("%w (with -g, give another repository's side without git:, as %s)", err, rest)
}

// diskRoot returns the root of side, on disk: a directory, or nothing.
func diskRoot(side input) source.Root {
	if side.kind == inputNull {
		return source.Nothing(side.path)
	}
	return source.Disk(side.path)
}

// header returns what each side is: a repository's work tree, or its git
// directory if bare, or a path on disk.
func (c *child) header() view.Header {
	h := view.Header{Home: os.Getenv("HOME")}
	for i, side := range c.in.sides() {
		s := &h.Sides[i]
		if loc := c.locs[i]; loc != nil {
			s.Label, s.Path = "(git)", cmp.Or(loc.WorkTree, loc.GitDir)
			continue
		}
		s.Path = side.path
		if abs, err := filepath.Abs(side.path); err == nil {
			s.Path = abs
		}
		s.Label = "(dir)"
		if side.kind == inputNull {
			s.Label = "(nothing)"
		}
	}
	return h
}
