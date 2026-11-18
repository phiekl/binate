// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Command binate displays file and directory differences in a read-only
// terminal interface. It runs as two processes: this unrestricted parent,
// which only starts the child, runs the editor and stops the job for
// Ctrl+z, and the child, restricted with Landlock, which does everything
// else (see child.go).
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"pxy.se/go/binate/internal/args"
	"pxy.se/go/binate/internal/cli"
	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/display"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/supervise"
)

// version is set at build time by make.
var version string

// childEnv marks the child and lists its file descriptors (see childSpec).
const childEnv = "BINATE_INTERNAL_CHILD"

// Terminal sequences the parent writes if the child dies holding the
// screen: reset attributes, show the cursor, leave the alternate screen.
const releaseScreen = "\x1b[0m\x1b[?25h\x1b[?1049l"

type options struct {
	compare scan.Attrs
	quick   bool
	git     bool
	// cached compares a revision with the index, as git diff --cached.
	cached bool
	// directory is where the repository of --git is found from.
	directory string
	excludes  []string
	context   int
	verbose   int
	version   bool
	left      string
	right     string
}

// gitName is the name, as of a symlink to binate, run by which it compares
// git revisions without --git.
const gitName = "agitate"

// programName is the name binate goes by: gitName if run by it, and
// binate by any other.
func programName() string {
	if path.Base(os.Args[0]) == gitName {
		return gitName
	}
	return "binate"
}

// parseOptions parses argv of binate run as name, which with gitName
// implies --git.
func parseOptions(name string, argv []string) (*args.Set, options, error) {
	var o options
	var contextLines string
	compare := scan.DefaultAttrs.String()
	gitOnly := name == gitName
	s := args.NewSet(name)
	s.Count(&o.verbose, "verbose", "v", "write debug messages (repeat for more)")
	s.Bool(&o.version, "version", "V", "show version and exit")
	s.Global("verbose", "version")
	sides, directory := "%s directory or file, or revision, . or : with --git", "with --git, find the repository from directory, as git -C"
	cached := "with --git, compare left, or HEAD, with the index"
	if gitOnly {
		sides, directory = "%s revision, . (work tree) or : (index)", "find the repository from directory, as git -C"
		cached = "compare left, or HEAD, with the index, as git diff --cached"
	}
	s.Arg(&o.left, "left", fmt.Sprintf(sides, "left"))
	s.Arg(&o.right, "right", fmt.Sprintf(sides, "right"))
	s.ArgDefaults(func() bool { return gitOnly || o.git }, "HEAD", ".")
	s.Func("compare", "c", "attributes to compare (see binate(1))", func(value string) {
		compare = value
	})
	s.Bool(&o.quick, "quick", "q", "compare size and modification time")
	s.Bool(&o.git, "git", "g", "compare in git mode (left defaults to HEAD, right to .)")
	s.String(&o.directory, "directory", "C", "", directory)
	s.Bool(&o.cached, "cached", "", cached)
	s.Func("exclude", "x", "exclude names matching a pattern (repeatable)", func(p string) {
		o.excludes = append(o.excludes, p)
	})
	s.String(&contextLines, "unified", "U", "3", "context lines around changes")
	s.DenyEmpty("left")
	s.DenyEmpty("right")
	if gitOnly {
		// Implied, and refused with it.
		s.Hide("git", "quick")
	}

	err := s.Parse(argv)
	if err == args.ErrUsage && o.version {
		return s, o, nil
	}
	if err != nil {
		return s, o, err
	}
	o.git = o.git || gitOnly
	if o.directory != "" && !o.git {
		return s, o, errors.New("--directory without --git: it names where to find the repository (for context lines, use -U)")
	}
	if o.git && o.quick {
		return s, o, errors.New("--quick with --git: revisions don't record modification times")
	}
	if o.cached {
		switch {
		case !o.git:
			return s, o, errors.New("--cached without --git: it compares a revision with the git index")
		case s.Given("right") || o.left == "." || o.left == ":":
			return s, o, errors.New("--cached compares one revision with the index: give at most one")
		}
		o.right = ":"
	}
	switch {
	case o.git && o.left == "." && o.right == ".":
		return s, o, errors.New("a work tree must be compared with a revision or the index")
	case o.git && o.left == ":" && o.right == ":":
		return s, o, errors.New("the index must be compared with a revision or the work tree")
	}
	n, err := strconv.Atoi(contextLines)
	if err != nil || n < 0 {
		return s, o, fmt.Errorf("invalid context lines: %s", contextLines)
	}
	o.context = n
	if o.compare, err = scan.ParseAttrs(compare); err != nil {
		return s, o, fmt.Errorf("invalid --compare: %w", err)
	}
	for _, p := range o.excludes {
		if _, err := path.Match(p, ""); err != nil {
			return s, o, fmt.Errorf("invalid exclude pattern %q: %w", p, err)
		}
	}
	return s, o, nil
}

func main() {
	if spec, ok := os.LookupEnv(childEnv); ok {
		os.Exit(runChild(spec))
	}
	os.Exit(runParent())
}

func runParent() int {
	set, o, err := parseOptions(programName(), os.Args[1:])
	switch {
	case err == args.ErrHelp:
		if err := writeHelp(os.Stdout, set); err != nil {
			return fail("failed writing help: %v", err)
		}
		return 0
	case err == args.ErrUsage:
		_ = writeHelp(os.Stderr, set)
		return 2
	case err != nil:
		return fail("usage: %v", err)
	case o.version:
		fmt.Println("binate", programVersion())
		return 0
	}
	log := cli.NewLogger(os.Stderr, o.verbose)
	if err := checkEditorEnv(os.Getenv); err != nil {
		return fail("%v", err)
	}

	in := gitInputs(o.left, o.right)
	if !o.git {
		if in, err = resolveInputs(o.left, o.right); err != nil {
			return fail("%v", err)
		}
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return fail("stdin and stdout must be a terminal")
	}
	// Warn about BINATE_COLORS before the screen opens. The child parses
	// it again, quietly.
	colorEnv := colors.FromEnv(os.Getenv)
	for _, w := range colors.Parse(colorEnv).Warnings {
		cli.Warn(log, w)
	}
	editor := editorCommand()
	cli.Debug(log, cli.LevelDebug1, "starting", "left", in.left.path, "right", in.right.path,
		"directories", in.dirMode(), "compare", o.compare.String(), "editor", strings.Join(editor, " "),
		"truecolor", colorEnv.TrueColor)

	saved, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil {
		return fail("reading the terminal settings: %v", err)
	}
	p := &parent{log: log, saved: saved, editor: editor}
	return p.run(in)
}

// parent holds what the parent needs while the child runs.
type parent struct {
	log    *slog.Logger
	saved  *term.State
	editor []string

	mu     sync.Mutex
	screen bool // the child holds the terminal
}

func (p *parent) run(in inputs) int {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fail("creating the socket pair: %v", err)
	}
	ours := os.NewFile(uintptr(fds[0]), "child")
	theirs := os.NewFile(uintptr(fds[1]), "parent")
	defer func() { _ = ours.Close() }()

	exe, err := os.Executable()
	if err != nil {
		return fail("finding the executable: %v", err)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	// The child is run by the same name, which may imply --git.
	cmd.Args[0] = os.Args[0]
	cmd.Stdin, cmd.Stdout = os.Stdin, os.Stdout
	// The child's stderr is held while it holds the screen, where messages
	// would be lost, and written once the terminal is restored.
	var held bytes.Buffer
	stderrTerminal := term.IsTerminal(int(os.Stderr.Fd()))
	if stderrTerminal {
		cmd.Stderr = &held
	} else {
		cmd.Stderr = os.Stderr
	}

	// Pass the socket, and streams, such as pipes from <(cmd), opened here,
	// where their /dev/fd names are this process's, on as descriptors 3 and
	// up, around those this process inherited, which keep their numbers.
	var files childFiles
	defer files.close()
	sockFD, err := files.add(theirs)
	if err != nil {
		return fail("passing the socket: %v", err)
	}
	streamFDs := []int{-1, -1}
	for i, side := range []input{in.left, in.right} {
		if side.kind != inputStream {
			continue
		}
		f, err := os.Open(side.path)
		if err != nil {
			return fail("%v", err)
		}
		if streamFDs[i], err = files.add(f); err != nil {
			return fail("passing %s: %v", side.path, err)
		}
	}
	cmd.ExtraFiles = files.files
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%d,%d,%d,%s", childEnv, sockFD, streamFDs[0], streamFDs[1], in.encode()))

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT)
	defer signal.Stop(signals)

	if err := cmd.Start(); err != nil {
		return fail("starting the child: %v", err)
	}
	// The child has its copies; the socket's end must be closed here for
	// serving to end when the child exits.
	files.close()
	go func() {
		for sig := range signals {
			// SIGINT and SIGQUIT come from the terminal, and reach the
			// child too; the child reads Ctrl+c as a key in raw mode.
			if sig == syscall.SIGTERM || sig == syscall.SIGHUP {
				_ = cmd.Process.Signal(sig)
			}
		}
	}()

	sup := &supervise.Parent{
		Left:    target(in.left, in.dirMode()),
		Right:   target(in.right, in.dirMode()),
		Screen:  p.setScreen,
		Edit:    p.edit,
		Suspend: p.suspend,
	}
	served := make(chan error, 1)
	go func() { served <- sup.Serve(ours) }()

	err = cmd.Wait()
	p.restore()
	// The restored cursor may be partway across a line. Start diagnostics
	// and the shell's next prompt at the beginning of a fresh, empty one.
	_, _ = io.WriteString(os.Stdout, "\r\n\x1b[2K")
	if stderrTerminal {
		_, _ = os.Stderr.Write(held.Bytes())
	}
	// The child's end is closed now that it exited, and the parent holds
	// no copy, so serving ends soon, once any editor it runs exits.
	if serr := <-served; serr != nil {
		cli.Warn(p.log, "invalid request from the child", "error", serr)
	}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr):
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			fmt.Fprintf(os.Stderr, "error: the child process was killed by %s\n", unix.SignalName(status.Signal()))
			return 128 + int(status.Signal())
		}
		return exitErr.ExitCode()
	}
	return fail("waiting for the child: %v", err)
}

// childFiles are the files the child gets as descriptors 3 and up. A
// descriptor this process inherited, as from "3<file", keeps its number
// in the child, so that an input named by it, as /dev/fd/3, names the
// same file there; ours, all close-on-exec, are passed only through
// childFiles, at the numbers left free.
type childFiles struct {
	files []*os.File
}

// add passes f, which childFiles then owns, and returns its descriptor in
// the child.
func (c *childFiles) add(f *os.File) (int, error) {
	for {
		fd := 3 + len(c.files)
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC != 0 {
			c.files = append(c.files, f)
			return fd, nil
		}
		// Inherited: pass a copy at the same number, closed here later.
		dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			_ = f.Close()
			return -1, fmt.Errorf("copying descriptor %d (%w)", fd, err)
		}
		c.files = append(c.files, os.NewFile(uintptr(dup), fmt.Sprintf("inherited %d", fd)))
	}
}

// close closes this process's copies of the files.
func (c *childFiles) close() {
	for _, f := range c.files {
		_ = f.Close()
	}
	c.files = nil
}

func target(i input, dirMode bool) supervise.Target {
	return supervise.Target{Path: i.path, Root: dirMode, File: !dirMode && i.kind == inputFile}
}

func (p *parent) setScreen(on bool) {
	p.mu.Lock()
	p.screen = on
	p.mu.Unlock()
}

// restore puts back the terminal settings saved at startup, and leaves the
// alternate screen if the child died holding it.
func (p *parent) restore() {
	p.mu.Lock()
	screen := p.screen
	p.screen = false
	p.mu.Unlock()
	if screen {
		_, _ = io.WriteString(os.Stdout, releaseScreen)
	}
	_ = term.Restore(int(os.Stdin.Fd()), p.saved)
}

// edit runs the editor on f, with the terminal as it was at startup.
func (p *parent) edit(f supervise.EditFile) error {
	if len(p.editor) == 0 {
		return errors.New("environment variable VISUAL or EDITOR must be set")
	}
	cmd, err := f.Command(p.editor)
	if err != nil {
		return fmt.Errorf("failed starting the editor \"%s\" (%w)", p.editor[0], err)
	}
	_ = term.Restore(int(os.Stdin.Fd()), p.saved)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if f.Dir != nil {
		cli.Debug(p.log, cli.LevelDebug1, "running the editor", "dir", f.Dir.Name(), "file", f.Name)
	} else {
		cli.Debug(p.log, cli.LevelDebug1, "running the editor", "path", f.Name)
	}
	err = cmd.Run()
	var exitErr *exec.ExitError
	var pathErr *os.PathError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() >= 0:
		return fmt.Errorf("the editor exited with status %d", exitErr.ExitCode())
	case errors.As(err, &exitErr):
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return fmt.Errorf("the editor was killed by %s", unix.SignalName(status.Signal()))
		}
		return errors.New("the editor was killed")
	case errors.Is(err, exec.ErrNotFound):
		return fmt.Errorf("the editor \"%s\" was not found", p.editor[0])
	case errors.As(err, &pathErr):
		return fmt.Errorf("failed starting the editor \"%s\" (%v)", p.editor[0], pathErr.Err)
	}
	return fmt.Errorf("failed starting the editor \"%s\" (%w)", p.editor[0], err)
}

// suspend stops the whole process group, as the shell expects of a job,
// and returns once the shell continues it. The child can't do so itself:
// Landlock keeps it from signalling processes outside its domain.
//
// kill(2) may return before this process has stopped, as another thread
// may take the signal, so wait for SIGCONT before letting the child take
// the terminal back.
func (p *parent) suspend() error {
	_ = term.Restore(int(os.Stdin.Fd()), p.saved)
	cont := make(chan os.Signal, 1)
	signal.Notify(cont, syscall.SIGCONT)
	defer signal.Stop(cont)
	if err := unix.Kill(0, unix.SIGSTOP); err != nil {
		return fmt.Errorf("failed stopping the job (%w)", err)
	}
	<-cont
	return nil
}

// editorChars are the characters VISUAL and EDITOR may contain besides
// ASCII letters, digits and spaces: those of ordinary commands, paths and
// options.
const editorChars = "-_./+=,:@%~"

// editorEnv returns the name and value of the variable naming the editor,
// read with getenv: VISUAL, or else EDITOR, the first that holds more than
// spaces; or "" for neither.
func editorEnv(getenv func(string) string) (name, value string) {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if v := getenv(name); strings.TrimSpace(v) != "" {
			return name, v
		}
	}
	return "", ""
}

// checkEditorEnv returns an error if the variable naming the editor, read
// with getenv, is set to anything but an ordinary command: ASCII letters,
// digits, spaces and editorChars. The other one, unused, may hold
// anything.
func checkEditorEnv(getenv func(string) string) error {
	name, v := editorEnv(getenv)
	for _, r := range v {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == ' ' || strings.ContainsRune(editorChars, r)
		if !ok {
			return fmt.Errorf("environment variable %s may contain only letters, digits, spaces and %s, but is \"%s\"",
				name, editorChars, v)
		}
	}
	return nil
}

// editorCommand returns $VISUAL, or else $EDITOR, split on spaces.
func editorCommand() []string {
	_, v := editorEnv(os.Getenv)
	return strings.Fields(v)
}

func programVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "unknown"
}

// writeHelp uses the width of the stream receiving the help. Redirected
// output, or a terminal whose size can't be read, remains unwrapped.
func writeHelp(file *os.File, s *args.Set) error {
	columns, _, err := term.GetSize(int(file.Fd()))
	if err != nil {
		columns = 0
	}
	_, err = io.WriteString(file, s.Help(columns))
	return err
}

// fail writes an error, escaped since it may contain paths, and returns the
// exit status for errors.
func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "error: %s\n", display.Stderr(fmt.Sprintf(format, a...)))
	return 2
}
