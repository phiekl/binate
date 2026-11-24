// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	llsyscall "github.com/landlock-lsm/go-landlock/landlock/syscall"
	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/args"
	"pxy.se/go/binate/internal/ptytest"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/term"
	"pxy.se/go/binate/internal/view"
)

// asBinateEnv makes the test binary run main, so tests can start binate as
// a program, and binate can start itself again as the child.
const asBinateEnv = "BINATE_TEST_AS_BINATE"

func TestMain(m *testing.M) {
	if os.Getenv(asBinateEnv) == "1" {
		main()
		return
	}
	clearGitEnv()
	os.Exit(m.Run())
}

// clearGitEnv unsets Git's variables, as a Git hook running the tests
// sets GIT_DIR and GIT_INDEX_FILE, which would point the git the tests
// run at another repository than their fixture's. binate is given the
// ones a test sets.
func clearGitEnv() {
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "GIT_") {
			_ = os.Unsetenv(name)
		}
	}
}

// binate returns a command running binate with args.
func binate(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), asBinateEnv+"=1", "NO_COLOR=", "BINATE_COLORS=", "VISUAL=", "EDITOR=")
	return cmd
}

// binateWithin returns binate run with args, killed if it hasn't exited
// within d, as one that should refuse its arguments at once could instead
// block, as on a FIFO's open, and the output then given up on.
func binateWithin(t *testing.T, d time.Duration, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), d)
	t.Cleanup(cancel)
	cmd := binate(t, args...)
	killed := exec.CommandContext(ctx, cmd.Path, args...)
	killed.Env, killed.WaitDelay = cmd.Env, time.Second
	return killed
}

// enterScreen and leaveScreen are written when binate takes and releases
// the terminal.
const (
	enterScreen = "\x1b[?1049h"
	leaveScreen = "\x1b[?1049l"
)

// startInTerminal runs binate in a new pseudo-terminal and waits until it
// has taken the screen.
func startInTerminal(t *testing.T, args ...string) (*ptytest.Term, int) {
	t.Helper()
	pt := ptytest.Open(t, 100, 30)
	pt.Start(binate(t, args...))
	return pt, pt.Wait(0, enterScreen)
}

// childOf returns the pid of the parent's child process.
func childOf(t *testing.T, pid int) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if children := ptytest.Children(pid); len(children) == 1 {
			return children[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no single child process of %d", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func checkCooked(t *testing.T, pt *ptytest.Term) {
	t.Helper()
	tios := pt.Termios()
	if tios.Lflag&(unix.ICANON|unix.ECHO|unix.ISIG) != unix.ICANON|unix.ECHO|unix.ISIG {
		t.Errorf("terminal left in raw mode: lflag %#x", tios.Lflag)
	}
}

func checkFreshLine(t *testing.T, pt *ptytest.Term, offset int) {
	t.Helper()
	end := pt.Wait(offset, leaveScreen)
	pt.Wait(end, "\n")
	out := pt.Output()[offset:]
	end = strings.LastIndex(out, leaveScreen) + len(leaveScreen)
	// ONLCR may translate the explicit CR LF to CR CR LF after the
	// original terminal settings are restored. Both start a fresh line.
	tail := strings.TrimPrefix(out[end:], "\x1b[?25h")
	if !strings.HasPrefix(tail, "\r\n") && !strings.HasPrefix(tail, "\r\r\n") {
		t.Errorf("output after releasing the screen %q, want a fresh line", tail)
	}
	if tail = strings.TrimLeft(tail, "\r\n"); !strings.HasPrefix(tail, "\x1b[2K") {
		t.Errorf("output on the prompt line %q, want it cleared", tail)
	}
}

func TestRefusesWithoutTerminal(t *testing.T) {
	dir := t.TempDir()
	cmd := binate(t, dir, dir)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	if code := cmd.ProcessState.ExitCode(); code != 2 {
		t.Fatalf("exit status %d (%v), want 2; output %q", code, err, out)
	}
	if !strings.Contains(string(out), "must be a terminal") {
		t.Errorf("output %q, want a terminal error", out)
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	for _, tt := range []struct {
		args []string
		want string
	}{
		{nil, "usage: binate"},
		{[]string{dir}, "missing argument: right"},
		{[]string{"-U", "-1", dir, dir}, "invalid context lines: -1"},
		// -C was the context lines' before it was git's.
		{[]string{"-C", "5", dir, dir}, "--directory without --git: it names where to find the repository (for context lines, use -U)"},
		{[]string{"--context", "5", dir, dir}, "unknown flag: --context"},
		{[]string{"-x", "[", dir, dir}, "invalid exclude pattern"},
		{[]string{"--compare", "+size", dir, dir}, `invalid --compare: unknown attribute "size"`},
		{[]string{"-c", "+size", dir, dir}, `invalid --compare: unknown attribute "size"`},
		{[]string{"--compare", "-owner,mtime", dir, dir}, `invalid --compare: "mtime": give every attribute a + or -, or none`},
		{[]string{dir + "/missing", dir}, "missing: no such file or directory"},
		{[]string{"--bogus", dir, dir}, "unknown flag: --bogus"},
		{[]string{"-g", "-q", "HEAD~", "HEAD"}, "--quick with --git"},
		{[]string{"agitate", "-q", "HEAD~", "HEAD"}, "--quick with --git"},
		{[]string{"agitate"}, "stdin and stdout must be a terminal"},
		{[]string{"-g"}, "stdin and stdout must be a terminal"},
		{[]string{"-g", ""}, "flag/argument is empty: left"},
		{[]string{"-g", "."}, "a work tree must be compared with a revision"},
		{[]string{"-g", ":", ":"}, "the index must be compared with a revision or the work tree"},
		{[]string{"--cached", dir, dir}, "--cached without --git"},
		{[]string{"-g", "--cached", "HEAD", "HEAD"}, "--cached compares one revision with the index"},
		{[]string{"agitate", "--cached", "HEAD", ":"}, "--cached compares one revision with the index"},
		{[]string{"agitate", "--cached", "."}, "--cached compares a revision with the index: . is not one"},
		{[]string{"agitate", "--cached", ":"}, "--cached compares a revision with the index: : is not one"},
		{[]string{"-g", "--cached", "./@."}, "--cached compares a revision with the index: ./@. is not one"},
		{[]string{"-g", "--cached", "./@:"}, "--cached compares a revision with the index: ./@: is not one"},
	} {
		var cmd *exec.Cmd
		if len(tt.args) > 0 && tt.args[0] == "agitate" {
			cmd = agitate(t, "", tt.args[1:]...)
		} else {
			cmd = binate(t, tt.args...)
		}
		out, _ := cmd.CombinedOutput()
		if code := cmd.ProcessState.ExitCode(); code != 2 {
			t.Errorf("%q: exit status %d, want 2", tt.args, code)
		}
		if !strings.Contains(string(out), tt.want) {
			t.Errorf("%q: output %q, want %q", tt.args, out, tt.want)
		}
	}
}

func TestEditorEnv(t *testing.T) {
	for _, tt := range []struct {
		visual, editor string
		ok             bool
	}{
		{"", "", true},
		{"vim", "", true},
		{"", "code --wait", true},
		{"/usr/local/bin/emacsclient -t -a=", "nano", true},
		{"~/bin/my_editor.sh +1 --opt=a,b:c@d%e", "", true},
		{"vi\x1b]0;x\a", "", false},
		{"vim", "vi\nrm", true},
		{"vi\nrm", "vim", false},
		{" ", "vi;rm", false},
		{"vim -c 'set x'", "", false},
		{"vim\t-n", "", false},
		{"vïm", "", false},
		{"vi;rm", "", false},
	} {
		env := map[string]string{"VISUAL": tt.visual, "EDITOR": tt.editor}
		if err := checkEditorEnv(func(k string) string { return env[k] }); (err == nil) != tt.ok {
			t.Errorf("VISUAL %q, EDITOR %q: %v", tt.visual, tt.editor, err)
		}
	}

	// binate exits before starting, saying which variable and escaping
	// its value.
	dir := t.TempDir()
	cmd := binate(t, dir, dir)
	cmd.Env = append(cmd.Env, "EDITOR=vi\x1b]0;x\a")
	out, _ := cmd.CombinedOutput()
	if code := cmd.ProcessState.ExitCode(); code != 2 {
		t.Errorf("exit status %d, want 2", code)
	}
	if !strings.Contains(string(out), "environment variable EDITOR may contain only") || strings.ContainsRune(string(out), 0x1b) {
		t.Errorf("output %q", out)
	}
}

func TestHelpAndVersion(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "-V", "--version"} {
		cmd := binate(t, arg)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v", arg, err)
		}
		want := "usage: binate [option]... left right"
		if arg == "-V" || arg == "--version" {
			want = "binate "
		}
		if !strings.HasPrefix(string(out), want) {
			t.Errorf("%s: output %q, want prefix %q", arg, out, want)
		}
	}
}

func TestCompareOption(t *testing.T) {
	for _, tt := range []struct {
		flags []string
		want  scan.Attrs
	}{
		{nil, scan.DefaultAttrs},
		{[]string{"--compare", "mode"}, scan.Mode},
		{[]string{"-c", "mode"}, scan.Mode},
		{[]string{"-cmode"}, scan.Mode},
		{[]string{"-c", "+mtime"}, scan.AllAttrs},
		{[]string{"-c", "-owner,+mtime"}, scan.DefaultAttrs&^scan.Owner | scan.Mtime},
		{[]string{"-c", "none"}, scan.None},
		{[]string{"--compare", "mode", "-c", "owner"}, scan.Owner},
		{[]string{"-c", "owner", "--compare", "mode"}, scan.Mode},
	} {
		_, got, err := parseOptions("binate", append(tt.flags, "left", "right"))
		if err != nil {
			t.Errorf("%q: %v", tt.flags, err)
		} else if got.compare != tt.want {
			t.Errorf("%q: compare = %v, want %v", tt.flags, got.compare, tt.want)
		}
	}
}

// Run as agitate, from any directory, binate takes --git without it.
func TestGitName(t *testing.T) {
	for _, tc := range []struct {
		name string
		git  bool
	}{
		{"binate", false},
		{"agitate", true},
		{"agitated", false},
	} {
		_, o, err := parseOptions(tc.name, []string{"-C", "dir", "HEAD~", "HEAD"})
		switch {
		case tc.git && (err != nil || !o.git || o.directory != "dir"):
			t.Errorf("%s: %+v, %v", tc.name, o, err)
		case !tc.git && err == nil:
			t.Errorf("%s: -C without --git accepted", tc.name)
		}
	}
	if _, o, err := parseOptions("agitate", []string{"-g", "HEAD~", "HEAD"}); err != nil || !o.git {
		t.Errorf("agitate -g: %+v, %v", o, err)
	}
	// Its help names revisions, and leaves out --git, implied, and
	// --quick, refused with it.
	s, _, _ := parseOptions("agitate", []string{"--help"})
	help := s.Help(80)
	for _, want := range []string{
		"usage: agitate [option]... [left [right]]",
		"left [dir@]rev, . (work tree) or : (index) (default \"HEAD\")",
		"right [dir@]rev, . (work tree) or : (index) (default \".\")",
		"-C, --directory   find the repository",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("agitate's help without %q:\n%s", want, help)
		}
	}
	for _, unwanted := range []string{"--git", "--quick"} {
		if strings.Contains(help, unwanted) {
			t.Errorf("agitate's help with %q:\n%s", unwanted, help)
		}
	}
}

func TestHelpFits80Columns(t *testing.T) {
	s, _, err := parseOptions("binate", []string{"--help"})
	if err != args.ErrHelp {
		t.Fatal(err)
	}
	help := s.Help(0)
	for _, name := range []string{"binate", gitName} {
		s, _, _ := parseOptions(name, []string{"--help"})
		all := s.Help(0)
		if got := s.Help(80); got != all {
			t.Errorf("%s's help wraps at 80 columns:\n%s", name, got)
		}
		for _, line := range strings.Split(all, "\n") {
			if len(line) > 80 {
				t.Errorf("%s's help line exceeds 80 columns (%d): %s", name, len(line), line)
			}
		}
	}
	for _, alias := range []string{"-c, --compare", "-V, --version"} {
		if !strings.Contains(help, alias) {
			t.Errorf("help does not show %q", alias)
		}
	}
}

// quitPrompt is the quit prompt's question, shown on Ctrl+C.
const quitPrompt = "Are you sure you want to exit?"

// quit presses Ctrl+C, waits for the quit prompt on the 100x30 screen
// drawn since offset, and answers it with keys.
func quit(t *testing.T, pt *ptytest.Term, offset int, keys string) {
	t.Helper()
	pt.Write("\x03")
	pt.WaitScreen(100, 30, offset, quitPrompt)
	pt.Write(keys)
}

// pressEsc presses Esc on its own, waiting long enough that the next key
// isn't taken as part of an escape sequence.
func pressEsc(pt *ptytest.Term) {
	pt.Write("\x1b")
	time.Sleep(4 * term.EscTimeout)
}

func TestQuitRestoresTerminal(t *testing.T) {
	for _, keys := range []struct {
		name, keys string
	}{{"Ctrl+c twice", "\x03"}, {"x", "x"}, {"Left and Enter", "\x1b[D\r"}, {"Tab and Space", "\t "}, {"Exit and back", "\x1b[D\x1b[C\x1b[D\r"}} {
		t.Run(keys.name, func(t *testing.T) {
			dir := t.TempDir()
			pt, offset := startInTerminal(t, dir, dir)
			pt.WaitScreen(100, 30, offset, "no differences shown")
			quit(t, pt, offset, keys.keys)
			if code := pt.Exit(); code != 0 {
				t.Errorf("exit status %d, want 0", code)
			}
			checkCooked(t, pt)
			checkFreshLine(t, pt, offset)
			// Screen ignores alternate-buffer switches, as an embedded
			// terminal may do. The last UI frame must still be erased.
			if scr := pt.Screen(100, 30, offset); strings.TrimSpace(scr) != "" {
				t.Errorf("UI remains after quitting:\n%s", scr)
			}
		})
	}
}

// Cancel, Esc and other keys close the quit prompt, and neither q nor Esc
// quits.
func TestQuitPromptCloses(t *testing.T) {
	dir := t.TempDir()
	pt, offset := startInTerminal(t, dir, dir)
	pt.WaitScreen(100, 30, offset, "no differences shown")
	pt.Write("q")
	pressEsc(pt)
	// Cancel is focused, and Enter presses it.
	quit(t, pt, offset, "\r")
	// Still running, with the prompt gone: Help opens.
	pt.Write("?")
	if scr := pt.WaitScreen(100, 30, offset, "show or hide identical entries"); strings.Contains(scr, quitPrompt) {
		t.Errorf("quit prompt still shown:\n%s", scr)
	}
	for _, key := range []string{"\x1b", "j"} {
		pressEsc(pt)
		quit(t, pt, offset, "")
		if key == "\x1b" {
			pressEsc(pt)
		} else {
			pt.Write(key)
		}
		// The key closed the prompt, and was not taken by the tree.
		pt.Write("?")
		if scr := pt.WaitScreen(100, 30, offset, "show or hide identical entries"); strings.Contains(scr, quitPrompt) {
			t.Errorf("%q left the quit prompt:\n%s", key, scr)
		}
	}
	pressEsc(pt)
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
}

func TestKilledChildRestoresTerminal(t *testing.T) {
	dir := t.TempDir()
	pt, offset := startInTerminal(t, dir, dir)
	child := childOf(t, pt.Cmd.Process.Pid)
	if err := unix.Kill(child, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if code := pt.Exit(); code != 128+int(syscall.SIGKILL) {
		t.Errorf("exit status %d, want %d", code, 128+int(syscall.SIGKILL))
	}
	pt.Wait(pt.Wait(offset, leaveScreen), "error: the child process was killed by SIGKILL")
	checkCooked(t, pt)
	checkFreshLine(t, pt, offset)
}

func TestSignalsReachTheChild(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			dir := t.TempDir()
			pt, offset := startInTerminal(t, dir, dir)
			child := childOf(t, pt.Cmd.Process.Pid)
			// As kill(1) sends it: to the parent only.
			if err := pt.Cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if code := pt.Exit(); code != 128+int(sig) {
				t.Errorf("exit status %d, want %d", code, 128+int(sig))
			}
			pt.Wait(offset, leaveScreen)
			checkCooked(t, pt)
			checkFreshLine(t, pt, offset)
			if err := unix.Kill(child, 0); err == nil {
				t.Errorf("child %d still running", child)
			}
		})
	}
}

// processState returns the state letter of pid from /proc, such as R, S or
// T for stopped.
func processState(t *testing.T, pid int) string {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	// The state follows the command name in parentheses.
	s := string(b)
	return strings.Fields(s[strings.LastIndex(s, ")")+1:])[0]
}

func waitState(t *testing.T, pid int, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for processState(t, pid) != want {
		if time.Now().After(deadline) {
			t.Fatalf("process %d in state %s, want %s", pid, processState(t, pid), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSuspendStopsTheJob(t *testing.T) {
	dir := t.TempDir()
	pt, offset := startInTerminal(t, dir, dir)
	parent := pt.Cmd.Process.Pid
	child := childOf(t, parent)
	pt.Write("\x1a")
	offset = pt.Wait(offset, leaveScreen)
	waitState(t, parent, "T")
	waitState(t, child, "T")
	// Nothing may take the screen back until the job is continued.
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(pt.Output()[offset:], enterScreen) {
		t.Errorf("the screen was taken back while stopped")
	}
	checkCooked(t, pt)

	// As the shell's fg does: continue the whole group.
	if err := unix.Kill(-parent, unix.SIGCONT); err != nil {
		t.Fatal(err)
	}
	offset = pt.Wait(offset, enterScreen)
	quit(t, pt, offset, "x")
	pt.Wait(offset, leaveScreen)
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
}

func TestTooSmall(t *testing.T) {
	dir := t.TempDir()
	pt, offset := startInTerminal(t, dir, dir)
	pt.Resize(30, 4)
	_ = unix.Kill(-pt.Cmd.Process.Pid, unix.SIGWINCH)
	offset = pt.Wait(offset, "terminal too small (30x4")
	pt.Resize(100, 30)
	_ = unix.Kill(-pt.Cmd.Process.Pid, unix.SIGWINCH)
	// The tree itself, not just a redraw: another frame of the message
	// clears the screen too.
	offset = pt.Wait(offset, "no differences shown")
	quit(t, pt, offset, "\x03")
	pt.Wait(offset, leaveScreen)
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, contents := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestTreeView(t *testing.T) {
	left := writeFiles(t, map[string]string{"dir/changed": "a", "same": "x"})
	right := writeFiles(t, map[string]string{"dir/changed": "b", "same": "x", "new": "y"})
	pt, offset := startInTerminal(t, left, right)
	screen := func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
	screen("[1 identical] [1 right] [1 contents]")
	pt.Write("*")
	screen("  same")
	pt.Write("n")
	scr := screen("/dir/changed")
	if !strings.Contains(scr, "▾ dir/") {
		t.Errorf("jumping didn't expand dir:\n%s", scr)
	}
	// The panels start with each side's full path, linked to it, quoted
	// for the shell.
	if !regexp.MustCompile(`│ path │ /\S*/dir/changed *│   │ path │ /\S*/dir/changed *│`).MatchString(scr) {
		t.Errorf("panels without the paths:\n%s", scr)
	}
	for _, root := range []string{left, right} {
		if link := "\x1b]8;;'" + filepath.Join(root, "dir", "changed") + "'\x1b\\"; !strings.Contains(pt.Output(), link) {
			t.Errorf("no link %q", link)
		}
	}
	pt.Write("?")
	screen("show or hide identical entries")
	pressEsc(pt)
	quit(t, pt, offset, "x")
	pt.Wait(offset, leaveScreen)
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
}

// TestPlainIgnoresNothing checks that two directories compared without
// -g show every file, as Git's ignore rules are for a work tree alone:
// neither a .gitignore, a repository's exclude file nor the user's global
// one hides any. With git installed, the sides are repositories, and git
// confirms that each rule would hide its file in a work tree.
func TestPlainIgnoresNothing(t *testing.T) {
	user := t.TempDir()
	global := filepath.Join(user, "git", "ignore")
	for name, data := range map[string]string{
		global:                        "global-only\n",
		filepath.Join(user, "config"): "[core]\nexcludesFile = " + global + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"HOME=" + user, "XDG_CONFIG_HOME=" + user, "GIT_CONFIG_GLOBAL=" + filepath.Join(user, "config"), "GIT_CONFIG_NOSYSTEM=1"}
	_, err := exec.LookPath("git")
	hasGit := err == nil
	sides := [2]string{}
	for i, contents := range []string{"left\n", "right\n"} {
		sides[i] = writeFiles(t, map[string]string{
			".gitignore":        ".gitignore\nignored\n",
			".git/info/exclude": "repo-only\n",
			"ignored":           contents,
			"repo-only":         contents,
			"global-only":       contents,
		})
		if !hasGit {
			continue
		}
		for _, args := range [][]string{{"init", "-q"}, {"check-ignore", "ignored", "repo-only", "global-only"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir, cmd.Env = sides[i], append(os.Environ(), env...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
			if args[0] == "check-ignore" && string(out) != "ignored\nrepo-only\nglobal-only\n" {
				t.Fatalf("git ignores %q, want all three", out)
			}
		}
	}
	cmd := binate(t, sides[0], sides[1])
	cmd.Env = append(cmd.Env, env...)
	pt := ptytest.Open(t, 100, 30)
	pt.Start(cmd)
	offset := pt.Wait(0, enterScreen)
	scr := pt.WaitScreen(100, 30, offset, "[3 contents]")
	for _, name := range []string{"ignored", "repo-only", "global-only"} {
		if !strings.Contains(scr, " "+name) {
			t.Errorf("%s hidden:\n%s", name, scr)
		}
	}
	quit(t, pt, offset, "x")
	pt.Wait(offset, leaveScreen)
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
}

// TestRefusesFiles checks that a file is refused, opposite a file or a
// directory, before the screen is taken.
func TestRefusesFiles(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\n", "b.txt": "two\n", "d/a.txt": "one\n"})
	a := filepath.Join(dir, "a.txt")
	for _, args := range [][2]string{{a, filepath.Join(dir, "b.txt")}, {a, filepath.Join(dir, "d")}, {filepath.Join(dir, "d"), a}} {
		cmd := binate(t, args[0], args[1])
		out, _ := cmd.CombinedOutput()
		if code := cmd.ProcessState.ExitCode(); code != 2 || !strings.Contains(string(out), a+": a file, and binate compares directories") {
			t.Errorf("%q: exit status %d, output %q", args, code, out)
		}
	}
}

// TestRefusesOtherInputs checks that only directories are compared, and
// /dev/null taken for an empty one: a pipe, a FIFO without a writer,
// which is not waited for, or another device is refused.
func TestRefusesOtherInputs(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	for _, other := range []string{"/dev/fd/3", fifo, "/dev/zero"} {
		// Opening the FIFO, which has no writer, would block.
		cmd := binateWithin(t, 10*time.Second, dir, other)
		cmd.ExtraFiles = []*os.File{r}
		out, err := cmd.CombinedOutput()
		if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			t.Errorf("%s: %v, output %q", other, err, out)
			continue
		}
		if code := cmd.ProcessState.ExitCode(); code != 2 || !strings.Contains(string(out), other+": not a directory, which binate compares") {
			t.Errorf("%s: exit status %d, output %q", other, code, out)
		}
	}
}

// TestDescriptorInputs checks that inputs named by inherited descriptors,
// as /dev/fd/3 from "3<dir", name the same directories in the child,
// though the socket to it is passed as a descriptor too.
func TestDescriptorInputs(t *testing.T) {
	dir := writeFiles(t, map[string]string{"l/f": "a", "r/f": "b"})
	var files []*os.File
	for _, name := range []string{"l", "r"} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		files = append(files, f)
	}
	pt := ptytest.Open(t, 100, 30)
	cmd := binate(t, "/dev/fd/3", "/dev/fd/4")
	cmd.ExtraFiles = files
	pt.Start(cmd)
	offset := pt.Wait(0, enterScreen)
	pt.WaitScreen(100, 30, offset, "[1 contents]")
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

// TestNullIsNothing checks that /dev/null, by any name, is nothing:
// opposite a directory or a revision's tree an empty tree, everything on
// the other side added or removed.
func TestNullIsNothing(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a": "one\n", "sub/b": "two\n"})
	null := filepath.Join(t.TempDir(), "null")
	if err := os.Symlink("/dev/null", null); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"left", []string{"/dev/null", dir}, "[2 right]"},
		{"right by a symlink", []string{dir, null}, "[2 left]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pt, offset := startInTerminal(t, tc.args...)
			scr := pt.WaitScreen(100, 30, offset, tc.want)
			if !strings.Contains(scr, "sub") {
				t.Errorf("screen:\n%s", scr)
			}
			quit(t, pt, offset, "x")
			if code := pt.Exit(); code != 0 {
				t.Errorf("exit status %d", code)
			}
		})
	}
	if _, err := exec.LookPath("git"); err != nil {
		return
	}
	repo := gitRepo(t)
	git, nothing := [2]string{"(git)", repo}, [2]string{"(nothing)", "/dev/null"}
	for _, tc := range []struct {
		name   string
		args   []string
		want   string
		header [2][2]string
	}{
		{"revision's tree", []string{"git:" + repo + "@HEAD", "/dev/null"}, "[3 left]", [2][2]string{git, nothing}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pt := startIn(t, dir, tc.args...)
			offset := pt.Wait(0, enterScreen)
			scr := pt.WaitScreen(100, 30, offset, tc.want)
			checkHeader(t, pt, offset, scr, tc.header[0], tc.header[1])
			pt.Write("\x03x")
			if code := pt.Exit(); code != 0 {
				t.Errorf("exit status %d", code)
			}
		})
	}
}

func TestDiffViewFromTree(t *testing.T) {
	left := writeFiles(t, map[string]string{"f.txt": "a\nb\n", "bin": "\xff", "only.txt": "x\ny\n"})
	right := writeFiles(t, map[string]string{"f.txt": "a\nB\n", "bin": "\xfe"})
	pt, offset := startInTerminal(t, left, right)
	screen := func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
	screen("[1 left] [2 contents]")
	// The rows: bin, f.txt, only.txt.
	pt.Write("\r")
	screen("Unable to open")
	screen("Only UTF-8 encoded text files can be opened")
	screen("(neither side is).")
	// Any key closes the popup, and goes no further.
	pt.Write(" ")
	pt.Write("j\r")
	scr := screen("| │2 B")
	// The diff view's panels start with the paths too.
	if !regexp.MustCompile(`│ path │ /\S*/f\.txt *│   │ path │ /\S*/f\.txt *│`).MatchString(scr) {
		t.Errorf("panels without the paths:\n%s", scr)
	}
	pt.Write("\x1b")
	screen("[1 left] [2 contents]")
	pt.Write("j\r")
	screen("[2 left] [100%]")
	pt.Write("\x1b")
	screen("[1 left] [2 contents]")
	quit(t, pt, offset, "x")
	pt.Wait(offset, leaveScreen)
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

// TestRestrictedSession runs a session under Landlock with a root given
// through a symlink elsewhere: rescanning after a file was replaced by
// rename, and opening the diff view on it, must work while restricted.
func TestRestrictedSession(t *testing.T) {
	left := writeFiles(t, map[string]string{"dir/f.txt": "same\n"})
	real := writeFiles(t, map[string]string{"dir/f.txt": "same\n"})
	links := t.TempDir()
	right := filepath.Join(links, "right")
	if err := os.Symlink(real, right); err != nil {
		t.Fatal(err)
	}

	pt, offset := startInTerminal(t, "-v", left, right)
	screen := func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
	screen("[1 identical]")

	tmp := filepath.Join(real, "dir", ".f.txt.new")
	if err := os.WriteFile(tmp, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(real, "dir", "f.txt")); err != nil {
		t.Fatal(err)
	}
	pt.Write("r")
	screen("[1 contents]")
	// The panels show the selected entry: owner names were read before
	// the child was restricted.
	if u, err := user.LookupId(strconv.Itoa(os.Getuid())); err == nil {
		screen("owner │ " + u.Username + ":")
	}
	pt.Write("n\r")
	screen("| │1 changed")
	pt.Write("\x1b")
	screen("[1 contents]")
	quit(t, pt, offset, "x")
	pt.Wait(offset, leaveScreen)
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
	// Debug messages are held while binate holds the screen.
	out := pt.Output()
	if abi, err := llsyscall.LandlockGetABIVersion(); err == nil && abi >= 1 {
		if !strings.Contains(out[offset:], "debug1: landlock: enabled") {
			t.Errorf("no report that Landlock is enabled in %q", out[offset:])
		}
	}
	// Without seccomp filters in the kernel, binate goes on without one,
	// and says so instead.
	switch {
	case strings.Contains(out[offset:], "debug1: seccomp: enabled"):
	case strings.Contains(out[offset:], "debug1: seccomp: unavailable"):
		t.Log("seccomp filters unavailable")
	default:
		t.Errorf("no report of the seccomp filter in %q", out[offset:])
	}
}

func TestColorWarnings(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a": "x"})
	pt := ptytest.Open(t, 100, 30)
	cmd := binate(t, dir, dir)
	cmd.Env = append(cmd.Env, "BINATE_COLORS=bogus=1:dir=1;34m\x1b[31")
	pt.Start(cmd)
	offset := pt.Wait(0, enterScreen)
	before := pt.Output()[:offset]
	for _, want := range []string{`warning: BINATE_COLORS: unknown key "bogus"`, `invalid value "1;34m\x1b[31"`} {
		if !strings.Contains(before, want) {
			t.Errorf("before the screen %q, want %q", before, want)
		}
	}
	quit(t, pt, offset, "x")
	pt.Exit()
}

func TestNoColor(t *testing.T) {
	left := writeFiles(t, map[string]string{"a": "x", "dir/b": "y"})
	right := writeFiles(t, map[string]string{"a": "y", "c\x1bd": "z"})
	pt := ptytest.Open(t, 100, 30)
	cmd := binate(t, left, right)
	cmd.Env = append(cmd.Env, "NO_COLOR=1")
	pt.Start(cmd)
	offset := pt.Wait(0, enterScreen)
	pt.WaitScreen(100, 30, offset, "[1 contents]")
	quit(t, pt, offset, "x")
	pt.Exit()
	// Only attributes: reset, bold, dim, underline, reverse and its end.
	allowed := map[string]bool{"0": true, "1": true, "2": true, "4": true, "7": true, "27": true}
	out := pt.Output()[offset:]
	for _, seq := range strings.Split(out, "\x1b[")[1:] {
		end := strings.IndexFunc(seq, func(r rune) bool { return r >= 0x40 && r <= 0x7e })
		if end < 0 || seq[end] != 'm' {
			continue
		}
		for _, p := range strings.Split(seq[:end], ";") {
			if !allowed[p] {
				t.Errorf("colour parameter %q under NO_COLOR", p)
			}
		}
	}
}

// fakeEditor writes an editor script that logs the path it was given and
// does what the file "action" next to it says: "write:TEXT" writes TEXT,
// "rename:TEXT" writes it to a new file renamed over the old one, as vim
// saves, and "delete" removes the file. TEXT is as for printf %b.
type fakeEditor struct {
	t   *testing.T
	dir string
}

func newFakeEditor(t *testing.T) *fakeEditor {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh for the fake editor")
	}
	e := &fakeEditor{t: t, dir: t.TempDir()}
	script := `#!/bin/sh
dir=$(dirname "$0")
case "$1" in
/*) printf '%s\n' "$1" ;;
*) printf '%s/%s\n' "$(pwd -P)" "${1#./}" ;;
esac >> "$dir/log"
action=$(cat "$dir/action")
case "$action" in
write:*) printf '%b' "${action#write:}" > "$1" ;;
rename:*) printf '%b' "${action#rename:}" > "$1.new" && mv "$1.new" "$1" ;;
delete) rm "$1" ;;
fail) exit 3 ;;
esac
`
	if err := os.WriteFile(filepath.Join(e.dir, "edit"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e.do("none")
	return e
}

func (e *fakeEditor) do(action string) {
	if err := os.WriteFile(filepath.Join(e.dir, "action"), []byte(action), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

// log returns the files the editor was given, as absolute paths: a
// relative one joined to the directory it was started in.
func (e *fakeEditor) log() []string {
	b, _ := os.ReadFile(filepath.Join(e.dir, "log"))
	return strings.Fields(string(b))
}

// start runs binate with the fake editor as VISUAL.
func (e *fakeEditor) start(args ...string) (*ptytest.Term, int) {
	pt := ptytest.Open(e.t, 100, 30)
	cmd := binate(e.t, args...)
	cmd.Env = append(cmd.Env, "VISUAL="+filepath.Join(e.dir, "edit"))
	pt.Start(cmd)
	return pt, pt.Wait(0, enterScreen)
}

func TestEditInTree(t *testing.T) {
	e := newFakeEditor(t)
	left := writeFiles(t, map[string]string{"f.txt": "a\n", "other.txt": "x\n", "sub/g": "x"})
	right := writeFiles(t, map[string]string{"f.txt": "b\n", "sub/g": "y"})
	pt, offset := e.start(left, right)
	screen := func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
	screen("[1 left] [2 contents]")

	// Rows: sub, sub/g, f.txt, other.txt. A directory can't be edited: a
	// popup says so, which any key closes without reaching the tree.
	pt.Write("e")
	if scr := screen("Neither side is a regular file."); !strings.Contains(scr, "Unable to edit") {
		t.Errorf("error popup:\n%s", scr)
	}
	pt.Write("j")
	time.Sleep(100 * time.Millisecond)
	if scr := pt.Screen(100, 30, offset); strings.Contains(scr, "Unable to edit") {
		t.Errorf("error popup still open:\n%s", scr)
	}

	// The popup shows both paths; other keys are ignored; Esc closes it.
	pt.Write("jj")
	pt.Write("e")
	scr := screen("Select side to edit")
	if !strings.Contains(scr, "h ←  "+filepath.Join(left, "f.txt")) || !strings.Contains(scr, "l →  "+filepath.Join(right, "f.txt")) {
		t.Errorf("popup:\n%s", scr)
	}
	pt.Write("x")
	pt.Write("\x1b")
	time.Sleep(100 * time.Millisecond)
	if scr := pt.Screen(100, 30, offset); strings.Contains(scr, "Select side to edit") {
		t.Errorf("popup still open:\n%s", scr)
	}
	// e closes it too.
	pt.Write("e")
	screen("Select side to edit")
	pt.Write("e")
	time.Sleep(100 * time.Millisecond)
	if scr := pt.Screen(100, 30, offset); strings.Contains(scr, "Select side to edit") {
		t.Errorf("popup still open:\n%s", scr)
	}

	// Edited to match, chosen with Down and Enter: the mark and the
	// counts follow.
	e.do(`write:a\n`)
	pt.Write("e")
	screen("Select side to edit")
	pt.Write("\x1b[B\r")
	screen("[1 identical] [1 left] [1 contents]")
	realRight, err := filepath.EvalSymlinks(right)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.log(); len(got) != 1 || got[0] != filepath.Join(realRight, "f.txt") {
		t.Errorf("editor given %q", got)
	}

	// One side only: the popup lists it alone, and l doesn't edit the
	// other. Deleted: gone.
	pt.Write("*")
	screen("  other.txt")
	pt.Write("\x1b[F")
	e.do("delete")
	pt.Write("e")
	if scr := screen("The right side is missing."); !strings.Contains(scr, "h ←") || strings.Contains(scr, "l →") {
		t.Errorf("popup for the left side:\n%s", scr)
	}
	pt.Write("l\r")
	pt.Write("k")
	screen("[1 identical] [1 contents]")
	if _, err := os.Stat(filepath.Join(left, "other.txt")); !os.IsNotExist(err) {
		t.Errorf("other.txt: %v", err)
	}

	// An editor that fails says so in the error popup, not the status bar.
	e.do("fail")
	pt.Write("\x1b[F")
	pt.Write("e")
	screen("Select side to edit")
	pt.Write("h")
	if scr := screen("The editor exited with status 3."); !strings.Contains(scr, "Failed to edit") {
		t.Errorf("error popup:\n%s", scr)
	}
	pt.Write(" ")
	screen("[1 identical] [1 contents]")
	quit(t, pt, offset, "x")
	pt.Wait(offset, leaveScreen)
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

func TestEditInDiffView(t *testing.T) {
	e := newFakeEditor(t)
	left := writeFiles(t, map[string]string{"f.txt": "a\nb\n"})
	right := writeFiles(t, map[string]string{"f.txt": "a\nc\n"})
	pt, offset := e.start(left, right)
	screen := func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
	screen("[1 contents]")
	pt.Write("\r")
	screen("| │2 c")

	// Replaced by rename, as vim saves: the diff view reads it again.
	e.do(`rename:a\nX\n`)
	pt.Write("e")
	screen("Select side to edit")
	pt.Write("h")
	scr := screen("2 X")
	if !strings.Contains(scr, "| │2 c") {
		t.Errorf("after the edit:\n%s", scr)
	}
	// Edited to match: no differences, and the tree's mark follows.
	e.do(`rename:a\nc\n`)
	pt.Write("e")
	screen("Select side to edit")
	pt.Write("\x1b[D")
	screen("no differences")
	pt.Write("\x1b")
	screen("no differences shown")
	pt.Write("*")
	// The note replaces the counts until the next key.
	scr = screen("showing identical entries")
	if !strings.Contains(scr, "f.txt") || !strings.Contains(scr, " = ") {
		t.Errorf("identical entries shown:\n%s", scr)
	}
	pt.Write("j")
	screen("[1 identical]")

	// Deleted: the diff view says so instead of showing old contents,
	// broken into lines in the left box, and the right box says which
	// side failed.
	pt.Write("\r")
	screen("no differences")
	e.do("delete")
	pt.Write("e")
	screen("Select side to edit")
	pt.Write("h")
	scr = screen("│Failed reading this file (no such file or     │   │Unable to compare files due to an error on the │")
	if !strings.Contains(scr, "│directory).                                   │   │left side.") {
		t.Errorf("message:\n%s", scr)
	}
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

// TestEditOppositeNothing checks that /dev/null, nothing, can't be
// edited: the popup lists only the file opposite it.
func TestEditOppositeNothing(t *testing.T) {
	e := newFakeEditor(t)
	dir := writeFiles(t, map[string]string{"b.txt": "two\n"})
	e.do(`write:changed\n`)
	pt, offset := e.start("/dev/null", dir)
	screen := func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
	screen("[1 right]")
	pt.Write("e")
	if scr := screen("Select side to edit"); strings.Contains(scr, "h ←") || !strings.Contains(scr, "l →") {
		t.Errorf("popup for the right side:\n%s", scr)
	}
	// Enter edits it, and the next opens the diff view.
	pt.Write("\r\r")
	screen("+ │1 changed")
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

func TestEditWithoutEditor(t *testing.T) {
	left := writeFiles(t, map[string]string{"f.txt": "one\n"})
	right := writeFiles(t, map[string]string{"f.txt": "two\n"})
	pt, offset := startInTerminal(t, left, right)
	pt.WaitScreen(100, 30, offset, "[1 contents]")
	pt.Write("e")
	pt.WaitScreen(100, 30, offset, "Environment variable VISUAL or EDITOR must be set.")
	// Ctrl+C closes the popup, and asks to quit.
	quit(t, pt, offset, "x")
	pt.Exit()
}

// An editor that can't be started says so in the error popup, its name
// quoted.
func TestEditorNotStarted(t *testing.T) {
	left := writeFiles(t, map[string]string{"f.txt": "a\n"})
	right := writeFiles(t, map[string]string{"f.txt": "b\n"})
	for _, tt := range []struct{ visual, want, more string }{
		{"binate-no-such-editor", `The editor "binate-no-such-editor" was not found.`, ""},
		{"/nonexistent/editor -n", `Failed starting the editor "/nonexistent/editor"`, "(no such file or directory)."},
	} {
		pt := ptytest.Open(t, 100, 30)
		cmd := binate(t, left, right)
		cmd.Env = append(cmd.Env, "VISUAL="+tt.visual)
		pt.Start(cmd)
		offset := pt.Wait(0, enterScreen)
		pt.WaitScreen(100, 30, offset, "[1 contents]")
		pt.Write("e")
		pt.WaitScreen(100, 30, offset, "Select side to edit")
		pt.Write("h")
		if scr := pt.WaitScreen(100, 30, offset, tt.want); !strings.Contains(scr, "Failed to edit") || !strings.Contains(scr, tt.more) {
			t.Errorf("%s: popup:\n%s", tt.visual, scr)
		}
		pt.Write(" ")
		quit(t, pt, offset, "x")
		pt.Wait(offset, leaveScreen)
		if code := pt.Exit(); code != 0 {
			t.Errorf("%s: exit status %d", tt.visual, code)
		}
	}
}

// TestResolveInputs checks that a side on disk must be a directory, or
// /dev/null, by any name, but not on both sides.
func TestResolveInputs(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/dev/null", filepath.Join(dir, "null")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"fifo": "not a directory", "file": "a file"} {
		if _, err := resolveInputs(dir, filepath.Join(dir, name)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, args := range [][2]string{{"/dev/null", dir}, {dir, filepath.Join(dir, "null")}} {
		in, err := resolveInputs(args[0], args[1])
		if err != nil || in.left.kind == in.right.kind {
			t.Errorf("%q: %+v, %v", args, in, err)
		}
	}
	if _, err := resolveInputs("/dev/null", filepath.Join(dir, "null")); err == nil || !strings.Contains(err.Error(), "/dev/null with itself") {
		t.Errorf("/dev/null twice: %v", err)
	}
}

// The child takes the inputs as the parent resolved them, from their kinds,
// without looking again.
func TestInputsEncode(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	for _, args := range [][2]string{{dir, other}, {dir, "/dev/null"}, {"/dev/null", dir}} {
		in, err := parseInputs(args[0], args[1], false, "")
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeInputs(args[0], args[1], in.encode(), false)
		if err != nil || got != in {
			t.Errorf("%q: decoded %+v, %v, want %+v", args, got, err, in)
		}
	}
	gitIn := func(left, right string, git bool) inputs {
		t.Helper()
		in, err := parseInputs(left, right, git, "")
		if err != nil {
			t.Fatalf("%q %q: %v", left, right, err)
		}
		got, err := decodeInputs(left, right, in.encode(), git)
		if err != nil || got != in {
			t.Errorf("%q %q: decoded %+v, %v, want %+v", left, right, got, err, in)
		}
		return in
	}
	if in := gitIn("HEAD~", "main:dir", true); in.encode() != "gg,-1,-1" {
		t.Errorf("git: encoded %q", in.encode())
	}
	for _, args := range [][2]string{{"HEAD", "."}, {".", "HEAD"}} {
		if in := gitIn(args[0], args[1], true); !in.hasWorkTree() {
			t.Errorf("%q: no work tree", args)
		}
	}
	// Sides in other repositories, and on disk opposite them.
	in := gitIn(dir+"@main", "git:"+other+"@.", true)
	if in.left.repo != dir || in.left.path != "main" || in.left.kind != inputGit {
		t.Errorf("other repository: %+v", in.left)
	}
	// With --git, git: is a revision's path, as of a branch git.
	if in.right.repo != "" || in.right.kind != inputGit {
		t.Errorf("git: with --git: %+v", in.right)
	}
	in = gitIn("git:"+dir+"@", other, false)
	if in.left.repo != dir || in.left.path != "HEAD" || in.right.kind != inputDir || in.right.repo != "" {
		t.Errorf("git side and directory: %+v", in)
	}
	// A side of tar is its archive's path, without tar:, which the child
	// takes only as given with the backend of an archive, as its format,
	// and without --git.
	archive := filepath.Join(dir, "a.tar")
	if err := os.WriteFile(archive, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	in = gitIn("tar:"+archive, other, false)
	if in.left.kind != inputArchive || in.left.format != "tar" || in.left.path != archive || in.encode() != "ad,-1,-1" {
		t.Errorf("tar side: %+v, encoded %q", in.left, in.encode())
	}
	for _, tc := range []struct {
		left string
		git  bool
	}{{archive, false}, {"git:" + archive, false}, {"tar:" + archive, true}, {"foo:" + archive, false}} {
		if _, err := decodeInputs(tc.left, other, "ad,-1,-1", tc.git); err == nil {
			t.Errorf("decoded %q as a side of an archive, with --git %v", tc.left, tc.git)
		}
	}
	// So is a side of zip, as given with zip:.
	in = gitIn("zip:"+archive, other, false)
	if in.left.kind != inputArchive || in.left.format != "zip" || in.left.path != archive || in.encode() != "ad,-1,-1" {
		t.Errorf("zip side: %+v, encoded %q", in.left, in.encode())
	}
	for _, code := range []string{"", "d", "ddd", "Dd", "ff", "xd", "gW", "dd,0,-1", "gg,1,-1", "gd,-2,-1", "dd,x,-1"} {
		if _, err := decodeInputs("a@b", "b", code, false); err == nil {
			t.Errorf("decodeInputs(%q) succeeded", code)
		}
	}
}

// TestParseInputs checks which arguments name sides of git in other
// repositories, and where their directories end.
func TestParseInputs(t *testing.T) {
	base := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, d := range []string{"repo", "pkg@1.2", "pkg", "deep/sub"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(home, "h"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	for _, tc := range []struct {
		arg        string
		git        bool
		repo, path string
		kind       inputKind
	}{
		{"main", true, "", "main", inputGit},
		{".", true, "", ".", inputWorkTree},
		{":", true, "", ":", inputIndex},
		{"HEAD:README", true, "", "HEAD:README", inputGit},
		{"./repo@main", true, "./repo", "main", inputGit},
		{base + "/repo@", true, base + "/repo", "HEAD", inputGit},
		{"./repo@.", true, "./repo", ".", inputWorkTree},
		{"./repo@:", true, "./repo", ":", inputIndex},
		{"./repo@@~2", true, "./repo", "@~2", inputGit},
		{"./repo@main:a@b", true, "./repo", "main:a@b", inputGit},
		{"./pkg@1.2@main", true, "./pkg@1.2", "main", inputGit},
		{"./deep/sub@origin/main:x", true, "./deep/sub", "origin/main:x", inputGit},
		{".@main", true, ".", "main", inputGit},
		{"~/h@main", true, home + "/h", "main", inputGit},
		{"git:repo@v1", false, "repo", "v1", inputGit},
		{"git:~/h@", false, home + "/h", "HEAD", inputGit},
		{"git:./pkg@1.2@.", false, "./pkg@1.2", ".", inputWorkTree},
	} {
		in, err := parseInputs(tc.arg, "/dev/null", tc.git, "")
		if tc.git {
			in, err = parseInputs(tc.arg, "HEAD", tc.git, "")
		}
		if err != nil || in.left.repo != tc.repo || in.left.path != tc.path || in.left.kind != tc.kind {
			t.Errorf("%q: %+v, %v", tc.arg, in.left, err)
		}
	}
	// A relative directory is found from -C.
	if in, err := parseInputs("sub@main", "HEAD", true, filepath.Join(base, "deep")); err != nil || in.left.repo != "" || in.left.path != "sub@main" {
		t.Errorf("not path-like, a revision: %+v, %v", in.left, err)
	}
	if in, err := parseInputs("./sub@main", "HEAD", true, filepath.Join(base, "deep")); err != nil || in.left.repo != "./sub" {
		t.Errorf("relative to -C: %+v, %v", in.left, err)
	}
	for _, tc := range []struct {
		left, right string
		git         bool
		want        string
	}{
		{"./repo", "HEAD", true, "no directory before an @"},
		{"./missing@main", "HEAD", true, "no directory before an @"},
		{"git:nowhere@main", "repo", false, "no directory before an @"},
		{"hg:repo@main", "repo", false, `unknown backend "hg"`},
		// As git's syntax without --git.
		{"HEAD:README", "repo", false, "as ./HEAD:README, or a revision with --git"},
		{"./repo@.", "~/h@.", true, "two work trees"},
	} {
		if _, err := parseInputs(tc.left, tc.right, tc.git, ""); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q %q: %v, want %q", tc.left, tc.right, err, tc.want)
		}
	}
	// Without --git, other names before a colon are paths, as are those
	// given as such.
	for _, name := range []string{"1:x", "a b:c", "_x:y", "./hg:x"} {
		if err := os.Mkdir(filepath.Join(base, filepath.Base(name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if in, err := parseInputs(name, "/dev/null", false, ""); err != nil || in.left.kind != inputDir {
			t.Errorf("%q: %+v, %v", name, in.left, err)
		}
	}
	for _, arg := range []string{"git:repo@main", "./repo@main"} {
		if _, _, err := parseOptions("binate", []string{"-q", arg, "x"}); arg[0] == 'g' && err == nil {
			t.Errorf("-q with %q accepted", arg)
		}
	}
	// A side's directory starts after its backend's name, whatever it is.
	for arg, want := range map[string]int{"git:repo@main": 4, "hg+ssh:repo@main": 7} {
		if got := repoStart(arg, false); got != want {
			t.Errorf("directory of %q at %d, want %d", arg, got, want)
		}
	}
}

// gitRepo makes a repository with two commits, in a temporary directory,
// running git, or skips the test if git is not installed.
// The times of gitRepo's commits: when each was authored, and committed.
var (
	gitAuthored  = [2]time.Time{time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)}
	gitCommitted = time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
)

// gitRev returns the commit rev names in the repository dir.
func gitRev(t *testing.T, dir, rev string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", rev)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", rev, err)
	}
	return strings.TrimSpace(string(out))
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.org", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.org",
			"GIT_COMMITTER_DATE="+gitCommitted.Format(time.RFC3339))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(files map[string]string) {
		t.Helper()
		for name, data := range files {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	run("init", "-q", "-b", "main")
	write(map[string]string{"dir/changed": "one\ntwo\n", "same": "x\n", "gone": "g\n"})
	run("add", "-A")
	run("commit", "-q", "-m", "first", "--date="+gitAuthored[0].Format(time.RFC3339))
	if err := os.Remove(filepath.Join(dir, "gone")); err != nil {
		t.Fatal(err)
	}
	write(map[string]string{"dir/changed": "one\nTWO\n", "new": "n\n"})
	run("add", "-A")
	run("commit", "-q", "-m", "second", "--date="+gitAuthored[1].Format(time.RFC3339))
	run("repack", "-adq")
	return dir
}

// checkHeader checks that the top row of scr, a screen 100 columns wide,
// drawn from offset on, says what each side is, its label above its box,
// and that each side's path is linked to in full, in its own half.
func checkHeader(t *testing.T, pt *ptytest.Term, offset int, scr string, left, right [2]string) {
	t.Helper()
	line := []rune(strings.Split(scr, "\n")[0])
	p := view.Layout(100)
	halves := [2]string{string(line[:min(p.RightBox, len(line))]), string(line[min(p.RightBox, len(line)):])}
	links := pt.Links(100, 30, offset)[0]
	for i, side := range [2][2]string{left, right} {
		if !strings.HasPrefix(halves[i], " "+side[0]+" ") {
			t.Errorf("side %d not %s in the header:\n%s", i, side[0], scr)
		}
		from, to := 0, p.RightBox
		if i == 1 {
			from, to = p.RightBox, len(links)
		}
		var got []string
		for _, l := range links[from:to] {
			if l != "" && !slices.Contains(got, l) {
				got = append(got, l)
			}
		}
		if want := "'" + side[1] + "'"; len(got) != 1 || got[0] != want {
			t.Errorf("side %d linked to %q, want %q", i, got, want)
		}
	}
}

// startIn runs binate in dir, in a new pseudo-terminal.
func startIn(t *testing.T, dir string, args ...string) *ptytest.Term {
	t.Helper()
	pt := ptytest.Open(t, 100, 30)
	cmd := binate(t, args...)
	cmd.Dir = dir
	pt.Start(cmd)
	return pt
}

func TestGitTrees(t *testing.T) {
	repo := gitRepo(t)
	// From a subdirectory, the repository found above it.
	pt := startIn(t, filepath.Join(repo, "dir"), "-v", "-g", "HEAD~", "main")
	offset := pt.Wait(0, enterScreen)
	screen := func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
	scr := screen("[1 identical] [1 left] [1 right] [1 contents]")
	// The repository is named at the top, above each box.
	checkHeader(t, pt, offset, scr, [2]string{"(git)", repo}, [2]string{"(git)", repo})
	if lines := strings.Split(scr, "\n"); !strings.HasPrefix(lines[1], "┌") {
		t.Errorf("no boxes below the header:\n%s", scr)
	}
	// The panels show the revisions' paths, as git names them.
	pt.Write("n")
	scr = screen("main:dir/changed")
	if !regexp.MustCompile(`│ +path │ HEAD~:dir/changed *│   │ +path │ main:dir/changed *│`).MatchString(scr) {
		t.Errorf("panels without the paths:\n%s", scr)
	}
	// They show only what git records.
	scr = screen("mode │ 0644")
	for _, label := range []string{"owner │", "inode │", "mtime │"} {
		if strings.Contains(scr, label) {
			t.Errorf("panels with %q:\n%s", label, scr)
		}
	}
	// The path after a revision and its colon is linked to the entry's
	// path in the repository, as git takes it; the revision is not.
	if strings.Contains(pt.Output(), "\x1b]8;;'HEAD~") {
		t.Error("a revision is linked")
	}
	if link := "HEAD~:\x1b]8;;'dir/changed'\x1b\\dir/changed"; !strings.Contains(pt.Output(), link) {
		t.Errorf("no link %q", link)
	}

	// And the commit that last changed each side, from git's history.
	scr = screen("committed │")
	local := func(t time.Time) string { return t.Local().Format("2006-01-02 15:04:05") }
	for _, re := range []string{
		`│ +commit │ ` + gitRev(t, repo, "HEAD~")[:12] + `[0-9a-f]*…?│   │ +commit │ ` + gitRev(t, repo, "HEAD")[:12],
		`│ +author │ a <a@example\.org> *│   │ +author │ a <a@example\.org> *│`,
		`│ +authored │ ` + local(gitAuthored[0]) + ` *│   │ +authored │ ` + local(gitAuthored[1]),
		`│ *committed │ ` + local(gitCommitted) + ` *│   │ *committed │ ` + local(gitCommitted),
	} {
		if !regexp.MustCompile(re).MatchString(scr) {
			t.Errorf("panels without %q:\n%s", re, scr)
		}
	}
	pt.Write("c")
	screen("[-]  owner     not recorded in git revisions")
	pressEsc(pt)
	pt.Write("e")
	screen("Files in git revisions can't be edited.")
	pt.Write("j")

	// The Actions popup's m shows a side's commit, chosen as the sides'
	// differ, as git show does.
	first, second := gitRev(t, repo, "HEAD~"), gitRev(t, repo, "HEAD")
	pt.Write("a")
	scr = screen("trees before and after that commit")
	if !regexp.MustCompile(`m  Commit message +commit that last changed it`).MatchString(scr) || !strings.Contains(scr, "d  Commit diff") {
		t.Errorf("Actions without the commit actions:\n%s", scr)
	}
	pt.Write("m")
	scr = screen("Select side's commit to show")
	if !strings.Contains(scr, first[:12]+" first") || !strings.Contains(scr, second[:12]+" second") {
		t.Errorf("popup without the commits:\n%s", scr)
	}
	pt.Write("l")
	scr = screen("commit " + second)
	for _, want := range []string{"(commit) " + second[:12] + " second", "Author: a <a@example.org>", "Date:   " + gitAuthored[1].Format("Mon Jan 2 15:04:05 2006 -0700"), "    second"} {
		if !strings.Contains(scr, want) {
			t.Errorf("message without %q:\n%s", want, scr)
		}
	}
	// Esc goes back to the popup, the side chosen selected, to choose the
	// other, and closes it then.
	pressEsc(pt)
	scr = screen("Select side's commit to show")
	if !regexp.MustCompile(`l → +` + second[:12]).MatchString(scr) {
		t.Errorf("popup not shown again:\n%s", scr)
	}
	pt.Write("h")
	screen("commit " + first)
	pressEsc(pt)
	screen("Select side's commit to show")
	// Then to the Actions popup, the action selected.
	pressEsc(pt)
	actions := "Enter or a key runs, Esc closes"
	scr = screen(actions)
	if strings.Contains(scr, "Select side's commit") {
		t.Errorf("the side's popup is still open:\n%s", scr)
	}
	pressEsc(pt)
	screen("[1 identical] [1 left] [1 right] [1 contents]")
	// s counts the lines it changed, as git show --stat --summary does,
	// without the message.
	pt.Write("as")
	screen("Select side's commit to count")
	pt.Write("l")
	scr = screen("3 files changed")
	if strings.Contains(scr, "Author:") {
		t.Errorf("stat with the message:\n%s", scr)
	}
	for _, want := range []string{
		"(commit) " + second[:12] + " second", "│  dir/changed | 2 +-", "│  gone        | 1 -", "│  new         | 1 +",
		"│  3 files changed, 2 insertions(+), 2 deletions(-)", "│  delete mode 100644 gone", "│  create mode 100644 new",
	} {
		if !strings.Contains(scr, want) {
			t.Errorf("stat without %q:\n%s", want, scr)
		}
	}
	pressEsc(pt)
	screen("Select side's commit to count")
	pressEsc(pt)
	screen(actions)
	pressEsc(pt)
	screen("[1 identical] [1 left] [1 right] [1 contents]")
	// d compares the trees before and after it: the first commit's with
	// none, the entry selected.
	pt.Write("ad")
	screen("Select side's commit to compare")
	pt.Write("h")
	// The panels are read after the trees are drawn: wait for both.
	short := first[:7]
	screen("[3 right]")
	scr = screen("path │ " + short + ":dir/changed")
	if !strings.Contains(scr, "(no parent)") || !strings.Contains(scr, "[3 right]") {
		t.Errorf("the first commit's trees:\n%s", scr)
	}
	pressEsc(pt)
	screen("Select side's commit to compare")
	pressEsc(pt)
	screen(actions)
	pressEsc(pt)
	screen("[1 identical] [1 left] [1 right] [1 contents]")

	pt.Write("\r")
	screen("| │2 TWO")
	// From the diff view too, back to it.
	pt.Write("ad")
	pt.Write("l")
	screen("[1 left] [1 right] [1 contents]")
	scr = screen("path │ " + second[:7] + "^:dir/changed")
	if !strings.Contains(scr, "[1 left] [1 right] [1 contents]") {
		t.Errorf("the second commit's trees:\n%s", scr)
	}
	pressEsc(pt)
	screen("Select side's commit to compare")
	pressEsc(pt)
	screen(actions)
	pressEsc(pt)
	screen("| │2 TWO")
	pt.Write("\x1b")
	screen("[1 contents]")
	quit(t, pt, offset, "x")
	pt.Wait(offset, leaveScreen)
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
	if abi, err := llsyscall.LandlockGetABIVersion(); err == nil && abi >= 1 {
		if out := pt.Output(); !strings.Contains(out[offset:], "debug1: landlock: enabled") {
			t.Errorf("no report that Landlock is enabled in %q", out[offset:])
		}
	}
}

// TestGitSubtrees compares trees a revision's path names, found from -C
// run from elsewhere, which "./" is relative to, as with git -C.
func TestGitSubtrees(t *testing.T) {
	repo := gitRepo(t)
	pt := startIn(t, t.TempDir(), "--git", "-C", filepath.Join(repo, "dir"), "HEAD~:./", "HEAD:dir")
	offset := pt.Wait(0, enterScreen)
	scr := pt.WaitScreen(100, 30, offset, "[1 contents]")
	// The repository is the one -C names.
	checkHeader(t, pt, offset, scr, [2]string{"(git)", repo}, [2]string{"(git)", repo})
	pt.Write("n")
	// The entries have the history of their commits, and the paths of
	// their revisions.
	scr = pt.WaitScreen(100, 30, offset, "committed │")
	if !regexp.MustCompile(`│ +path │ HEAD~:\./changed *│   │ +path │ HEAD:dir/changed *│`).MatchString(scr) {
		t.Errorf("screen:\n%s", scr)
	}
	quit(t, pt, offset, "x")
	pt.Wait(offset, leaveScreen)
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

// Revisions that can't be compared are reported before the screen is
// taken.
// agitate returns a command running binate as agitate, in dir if given.
func agitate(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := binate(t, args...)
	cmd.Args[0] = "/usr/local/bin/agitate"
	cmd.Dir = dir
	return cmd
}

// withEnv returns cmd with the environment variables env set too.
func withEnv(cmd *exec.Cmd, env ...string) *exec.Cmd {
	cmd.Env = append(cmd.Env, env...)
	return cmd
}

// Run as agitate, binate compares revisions, its child too.
func TestGitNameSession(t *testing.T) {
	repo := gitRepo(t)
	pt := ptytest.Open(t, 100, 30)
	pt.Start(agitate(t, repo, "HEAD~", "main"))
	offset := pt.Wait(0, enterScreen)
	scr := pt.WaitScreen(100, 30, offset, "[1 contents]")
	checkHeader(t, pt, offset, scr, [2]string{"(git)", repo}, [2]string{"(git)", repo})
	pt.Write("\x03x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

func TestGitWorkTreeDefaults(t *testing.T) {
	repo := gitRepo(t)
	sub := filepath.Join(repo, "dir")
	home := t.TempDir()
	// The work tree as a shell's $PWD names it, through a symlinked parent.
	link := filepath.Join(home, "p")
	if err := os.Symlink(filepath.Dir(repo), link); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(link, filepath.Base(repo))
	for _, tc := range []struct {
		name string
		cmd  *exec.Cmd
		dir  string
		want string
	}{
		{"one revision", binate(t, "HEAD~", "-g"), sub, "[1 identical] [1 left] [1 right] [1 contents]"},
		{"no revision", binate(t, "-g", "-C", sub), home, "[3 identical]"},
		{"agitate", agitate(t, sub), sub, "[3 identical]"},
		{"symlinked parent", agitate(t, linked), linked, "[3 identical]"},
		{"symlinked parent below the top", agitate(t, filepath.Join(linked, "dir")), filepath.Join(linked, "dir"), "[3 identical]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cmd.Dir = tc.dir
			tc.cmd.Env = append(tc.cmd.Env, "HOME="+home, "XDG_CONFIG_HOME="+home, "GIT_CONFIG_GLOBAL=/dev/null", "PWD="+tc.dir)
			pt := ptytest.Open(t, 100, 30)
			pt.Start(tc.cmd)
			offset := pt.Wait(0, enterScreen)
			scr := pt.WaitScreen(100, 30, offset, tc.want)
			checkHeader(t, pt, offset, scr, [2]string{"(git)", repo}, [2]string{"(git)", repo})
			pt.Write("\x03x")
			if code := pt.Exit(); code != 0 {
				t.Errorf("exit status %d", code)
			}
		})
	}
}

func TestGitWorkTreeSession(t *testing.T) {
	repo := gitRepo(t)
	user := t.TempDir()
	global := filepath.Join(user, "ignore")
	config := filepath.Join(user, "config")
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(global, "global-only\n")
	write(config, "[core]\nexcludesFile = "+global+"\n")
	write(filepath.Join(repo, ".git", "info", "exclude"), "repo-only\n")
	ignore := filepath.Join(repo, ".gitignore")
	const patterns = ".gitignore\nignored\nsame\n"
	write(ignore, patterns)
	for _, name := range []string{"global-only", "repo-only", "ignored", "local-new"} {
		write(filepath.Join(repo, name), "untracked\n")
	}
	write(filepath.Join(repo, "dir", "changed"), "staged\n")
	stage := exec.Command("git", "add", "dir/changed")
	stage.Dir = repo
	if out, err := stage.CombinedOutput(); err != nil {
		t.Fatalf("staging file: %v\n%s", err, out)
	}
	write(filepath.Join(repo, "dir", "changed"), "on disk\n")
	if err := os.Chmod(filepath.Join(repo, "same"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repo, "new")); err != nil {
		t.Fatal(err)
	}

	sub := filepath.Join(repo, "dir")
	for _, tc := range []struct {
		name string
		cmd  *exec.Cmd
	}{
		{"binate", binate(t, "-C", sub, "-g", "HEAD", ".")},
		{"agitate", agitate(t, sub, ".", "HEAD")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cmd.Env = append(tc.cmd.Env, "HOME="+user, "XDG_CONFIG_HOME="+user, "GIT_CONFIG_GLOBAL="+config)
			pt := ptytest.Open(t, 100, 30)
			pt.Start(tc.cmd)
			offset := pt.Wait(0, enterScreen)
			screen := func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
			scr := screen("[1 identical] [1 left] [1 right] [1 contents]")
			if !strings.Contains(scr, "local-new") {
				t.Errorf("work-tree entries missing:\n%s", scr)
			}
			checkHeader(t, pt, offset, scr, [2]string{"(git)", repo}, [2]string{"(git)", repo})
			pt.Write("n")
			screen("HEAD:dir/changed")
			pt.Write("\r")
			scr = screen("on disk")
			if strings.Contains(scr, "staged") {
				t.Errorf("diff shows staged contents:\n%s", scr)
			}
			if tc.name == "binate" {
				pt.Write("\x1b")
				screen("[1 identical] [1 left] [1 right] [1 contents]")
				write(ignore, patterns+"local-new\n")
				pt.Write("r")
				scr = screen("[1 identical] [1 left] [1 contents]")
				if strings.Contains(scr, "local-new") {
					t.Errorf("rescanned tree still shows an ignored file:\n%s", scr)
				}
				write(ignore, patterns)
			}
			pt.Write("\x03x")
			if code := pt.Exit(); code != 0 {
				t.Errorf("exit status %d", code)
			}
		})
	}
}

func TestGitErrors(t *testing.T) {
	repo := gitRepo(t)
	for _, tc := range []struct {
		dir  string
		args []string
		want string
	}{
		{repo, []string{"-g", "nope", "HEAD"}, "error: revision nope: unknown revision"},
		{repo, []string{"-g", "HEAD@{1}", "HEAD"}, "not supported"},
		{repo, []string{"-g", "HEAD", "HEAD:same"}, "unable to compare HEAD:same: the revision names a file"},
		{repo, []string{"-g", "HEAD~:same", "HEAD:same"}, "unable to compare HEAD~:same: the revision names a file"},
		{repo, []string{"-g", "HEAD:same", "."}, "the revision names a file"},
		{repo, []string{"-g", ".", "HEAD:dir"}, "the revision must name a full tree"},
		{repo, []string{"-g", ".", "."}, "a work tree must be compared with a revision"},
		{filepath.Join(repo, ".git"), []string{"-g", "HEAD", "."}, "the repository has no work tree"},
		{t.TempDir(), []string{"-g", "HEAD", "HEAD"}, "not a git repository"},
		{repo, []string{"-g", "-C", "missing", "HEAD", "HEAD"}, "missing: no such file or directory"},
		{t.TempDir(), []string{"-g", "-C", t.TempDir(), "HEAD", "HEAD"}, "not a git repository"},
	} {
		pt := startIn(t, tc.dir, tc.args...)
		pt.Wait(0, tc.want)
		if code := pt.Exit(); code != 2 {
			t.Errorf("%q: exit status %d", tc.args, code)
		}
		out := pt.Output()
		if !strings.Contains(out, tc.want) || strings.Contains(out, enterScreen) {
			t.Errorf("%q: output %q, want %q before any screen", tc.args, out, tc.want)
		}
	}
}

func TestGitNoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	gitInit := exec.Command("git", "init", "-q", "-b", "main")
	gitInit.Dir = repo
	gitInit.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "first"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Staged, for the index to have it too.
	add := exec.Command("git", "add", "first")
	add.Dir = repo
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	user := t.TempDir()
	// Before the first commit, HEAD is an empty tree beside the work tree.
	for _, args := range [][]string{{"-g"}, {"-g", "@", "."}, {"-g", "--cached"}} {
		cmd := binate(t, args...)
		cmd.Dir = repo
		cmd.Env = append(cmd.Env, "HOME="+user, "XDG_CONFIG_HOME="+user, "GIT_CONFIG_GLOBAL=/dev/null")
		pt := ptytest.Open(t, 100, 30)
		pt.Start(cmd)
		offset := pt.Wait(0, enterScreen)
		scr := pt.WaitScreen(100, 30, offset, "[1 right]")
		if !strings.Contains(scr, "(no commits)") || !strings.Contains(scr, "first") {
			t.Errorf("%q: screen:\n%s", args, scr)
		}
		pt.Write("\x03x")
		if code := pt.Exit(); code != 0 {
			t.Errorf("%q: exit status %d", args, code)
		}
	}
	// Without the work tree, or naming another revision, HEAD is unknown.
	for _, args := range [][]string{{"-g", "HEAD", "HEAD"}, {"-g", "main", "."}} {
		pt := startIn(t, repo, args...)
		pt.Wait(0, "unknown revision")
		if code := pt.Exit(); code != 2 {
			t.Errorf("%q: exit status %d", args, code)
		}
	}
}

func TestCachedOptions(t *testing.T) {
	for _, tc := range []struct {
		name        string
		argv        []string
		left, right string
		// repo is the index's repository, "" for the current one.
		repo string
	}{
		{"agitate", []string{"--cached"}, "HEAD", ":", ""},
		{"agitate", []string{"--cached", "main"}, "main", ":", ""},
		{"binate", []string{"-g", "--cached"}, "HEAD", ":", ""},
		{"agitate", []string{":"}, ":", ".", ""},
		{"agitate", []string{"HEAD", ":"}, "HEAD", ":", ""},
		// The index of the revision's repository.
		{"agitate", []string{"--cached", "./@main"}, "./@main", ":", "./"},
		{"agitate", []string{"--cached", "./@"}, "./@", ":", "./"},
	} {
		_, o, err := parseOptions(tc.name, tc.argv)
		if err != nil || o.left != tc.left || o.right != tc.right {
			t.Errorf("%s %q: %q %q, %v; want %q %q", tc.name, tc.argv, o.left, o.right, err, tc.left, tc.right)
		}
		in, err := parseInputs(o.left, o.right, true, "")
		if err == nil && o.cached {
			in, err = in.withCached()
		}
		if err != nil || !in.hasIndex() || in.right.repo != tc.repo {
			t.Errorf("%s %q: index side %+v, %v; want of %q", tc.name, tc.argv, in.right, err, tc.repo)
		}
		got, err := decodeInputs(o.left, o.right, in.encode(), true)
		if err == nil && o.cached {
			got, err = got.withCached()
		}
		if err != nil || got != in {
			t.Errorf("%s %q: decoded %+v, %v", tc.name, tc.argv, got, err)
		}
	}
}

// TestCachedNotRevision checks that --cached refuses a work tree or an
// index for left, as named in the current repository or another.
func TestCachedNotRevision(t *testing.T) {
	for _, left := range []string{".", ":", "./@.", "./@:"} {
		_, o, err := parseOptions("agitate", []string{"--cached", left})
		if err == nil {
			var in inputs
			if in, err = parseInputs(o.left, o.right, true, ""); err == nil {
				_, err = in.withCached()
			}
		}
		if want := "--cached compares a revision with the index: " + left + " is not one"; err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", left, err, want)
		}
	}
}

// TestCachedRepoAsFound checks that the child takes the repository of
// --cached's index as the parent found it, though a directory that would
// be found instead appeared since.
func TestCachedRepoAsFound(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, o, err := parseOptions("agitate", []string{"-C", base, "--cached", "./a@b@main"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := parseInputs(o.left, o.right, true, base)
	if err == nil {
		in, err = in.withCached()
	}
	if err != nil || in.right.repo != "./a" {
		t.Fatalf("index of %q, %v; want of ./a", in.right.repo, err)
	}
	// Found last, it would be found by the child if it looked again.
	if err := os.Mkdir(filepath.Join(base, "a@b"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, child, err := parseOptions("agitate", []string{"-C", base, "--cached", "./a@b@main"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeInputs(child.left, child.right, in.encode(), true)
	if err == nil {
		got, err = got.withCached()
	}
	if err != nil || got != in {
		t.Errorf("child's inputs %+v, %v; want %+v", got, err, in)
	}
}

func TestGitIndex(t *testing.T) {
	repo := gitRepo(t)
	user := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// Staged and then changed again, changed only on disk, staged only,
	// and untracked.
	write("dir/changed", "staged\n")
	write("added", "a\n")
	gitIn("add", "dir/changed", "added")
	write("dir/changed", "on disk\n")
	write("same", "unstaged\n")
	write("loose", "untracked\n")

	for _, tc := range []struct {
		name, want string
		cmd        *exec.Cmd
	}{
		// What git diff --cached shows: the staged change and addition.
		{"cached", "[2 identical] [1 right] [1 contents]", agitate(t, repo, "--cached")},
		{"revision and index", "[2 identical] [1 right] [1 contents]", binate(t, "-g", "HEAD", ":")},
		// What git diff shows, with the untracked file: what isn't staged.
		{"index and work tree", "[2 identical] [1 right] [2 contents]", agitate(t, repo, ":")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cmd.Dir = repo
			tc.cmd.Env = append(tc.cmd.Env, "HOME="+user, "XDG_CONFIG_HOME="+user, "GIT_CONFIG_GLOBAL=/dev/null")
			pt := ptytest.Open(t, 100, 30)
			pt.Start(tc.cmd)
			offset := pt.Wait(0, enterScreen)
			scr := pt.WaitScreen(100, 30, offset, tc.want)
			if !strings.Contains(scr, "(index)") {
				t.Errorf("no index side:\n%s", scr)
			}
			pt.Write("\x03x")
			if code := pt.Exit(); code != 0 {
				t.Errorf("exit status %d", code)
			}
		})
	}
}

// gitRepoSHA256 makes a repository in the SHA-256 object format with one
// commit of the files of gitRepo's last, and sha256.
func gitRepoSHA256(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range map[string]string{"dir/changed": "one\nTWO\n", "same": "x\n", "new": "n\n", "sha256": "s\n"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main", "--object-format=sha256"}, {"add", "-A"}, {"commit", "-q", "-m", "only"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.org", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.org")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// countsRE matches each of the tree's counts, as "[3 identical]".
var countsRE = regexp.MustCompile(`\[\d+ [a-z]+\]`)

// gitOtherRepo makes a repository of gitRepo's commits and a third,
// adding other, so that a side read from the wrong one of the two shows.
func gitOtherRepo(t *testing.T) string {
	t.Helper()
	dir := gitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "other"), []byte("o\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "third"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.org", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.org")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// Sides in other repositories than the current one, each read from its
// own, with the header saying which. The repositories differ at each
// revision compared, so that a side read from another shows: HEAD~ of
// other is HEAD of repo, and its HEAD and work tree have other too.
func TestGitOtherRepositories(t *testing.T) {
	repo, other := gitRepo(t), gitOtherRepo(t)
	sha256 := gitRepoSHA256(t)
	nowhere := t.TempDir()
	for _, tc := range []struct {
		name        string
		cmd         *exec.Cmd
		want        string
		left, right [2]string
	}{
		// Another repository's revision opposite the current work tree.
		{"with the work tree", agitate(t, repo, other+"@HEAD~"), "[3 identical]",
			[2]string{"(git)", other}, [2]string{"(git)", repo}},
		// Both in other repositories: the current directory, in none, is
		// not searched.
		{"two repositories", agitate(t, nowhere, repo+"@HEAD~", other+"@"), "[1 identical] [1 left] [2 right] [1 contents]",
			[2]string{"(git)", repo}, [2]string{"(git)", other}},
		{"relative to -C", agitate(t, nowhere, "-C", filepath.Dir(repo), "./"+filepath.Base(repo)+"@HEAD~", "./"+filepath.Base(other)+"@main"),
			"[1 identical] [1 left] [2 right] [1 contents]", [2]string{"(git)", repo}, [2]string{"(git)", other}},
		// Files of different object formats compared by their contents.
		{"object formats", agitate(t, nowhere, repo+"@", sha256+"@main"), "[3 identical] [1 right]",
			[2]string{"(git)", repo}, [2]string{"(git)", sha256}},
		{"git side without -g", binate(t, "git:"+repo+"@HEAD~", "git:"+other+"@."), "[1 identical] [1 left] [2 right] [1 contents]",
			[2]string{"(git)", repo}, [2]string{"(git)", other}},
		// GIT_DIR is ignored: the current repository is found from the
		// current directory.
		{"GIT_DIR ignored", withEnv(agitate(t, repo, other+"@HEAD~"), "GIT_DIR="+filepath.Join(other, ".git")),
			"[3 identical]", [2]string{"(git)", other}, [2]string{"(git)", repo}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cmd.Dir == "" {
				tc.cmd.Dir = nowhere
			}
			home := t.TempDir()
			tc.cmd.Env = append(tc.cmd.Env, "HOME="+home, "XDG_CONFIG_HOME="+home, "GIT_CONFIG_GLOBAL=/dev/null", "PWD="+tc.cmd.Dir)
			pt := ptytest.Open(t, 100, 30)
			pt.Start(tc.cmd)
			offset := pt.Wait(0, enterScreen)
			scr := pt.WaitScreen(100, 30, offset, tc.want)
			// All the counts, not only some.
			if got := strings.Join(countsRE.FindAllString(scr, -1), " "); got != tc.want {
				t.Errorf("counts %q, want %q", got, tc.want)
			}
			checkHeader(t, pt, offset, scr, tc.left, tc.right)
			pt.Write("\x03x")
			if code := pt.Exit(); code != 0 {
				t.Errorf("exit status %d", code)
			}
		})
	}
}

// A revision compared with a directory on disk, without -g: the
// directory has the modes git records, and can be edited.
func TestGitSideAndDirectory(t *testing.T) {
	repo := gitRepo(t)
	dir := writeFiles(t, map[string]string{"dir/changed": "one\nTWO\n", "same": "x\n", "new": "n\n"})
	for name, mode := range map[string]os.FileMode{"same": 0o600, "new": 0o664, "dir": 0o700} {
		if err := os.Chmod(filepath.Join(dir, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	e := newFakeEditor(t)
	pt, offset := e.start("git:"+repo+"@HEAD~", dir)
	screen := func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
	scr := screen("[1 identical] [1 left] [1 right] [1 contents]")
	checkHeader(t, pt, offset, scr, [2]string{"(git)", repo}, [2]string{"(dir)", dir})
	// The revision's side can't be edited, and the popup lists only the
	// directory's.
	pt.Write("n")
	screen("HEAD~:dir/changed")
	e.do(`write:one\ntwo\n`)
	pt.Write("e")
	if scr := screen("The left side is in a git revision."); strings.Contains(scr, "h ←") || !strings.Contains(scr, "l →") {
		t.Errorf("popup for the right side:\n%s", scr)
	}
	pt.Write("l")
	pt.Write("k")
	screen("[2 identical] [1 left] [1 right]")
	real, err := filepath.EvalSymlinks(filepath.Join(dir, "dir", "changed"))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.log(); len(got) != 1 || got[0] != real {
		t.Errorf("editor given %q", got)
	}
	pt.Write("\x03x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}

	// A work tree opposite a directory, both on disk, compares no times,
	// even when asked, as git records none.
	copied := writeFiles(t, map[string]string{"dir/changed": "one\nTWO\n", "same": "x\n", "new": "n\n"})
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"dir/changed", "same", "new"} {
		if err := os.Chtimes(filepath.Join(copied, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	pt = startIn(t, copied, "--compare=+mtime", "git:"+repo+"@.", copied)
	offset = pt.Wait(0, enterScreen)
	scr = pt.WaitScreen(100, 30, offset, "[3 identical]")
	if strings.Contains(scr, "mtime]") {
		t.Errorf("times compared:\n%s", scr)
	}
	// Nor do the panels, though the directory's files are older.
	pt.Write("*jj")
	scr = pt.WaitScreen(100, 30, offset, "mtime │ 2001-")
	if strings.Contains(scr, "(newer)") {
		t.Errorf("panels compare times:\n%s", scr)
	}
	pt.Write("\x03x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("work tree: exit status %d", code)
	}
}

// What can't be compared across repositories, or named as a backend.
func TestGitOtherRepositoryErrors(t *testing.T) {
	repo := gitRepo(t)
	dir := t.TempDir()
	// A repository whose .git is a symlink to that of one inside it: the
	// same index found from both.
	alias := t.TempDir()
	if err := os.Rename(repo, filepath.Join(alias, "inner")); err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(alias, "inner")
	if err := os.Symlink(filepath.Join("inner", ".git"), filepath.Join(alias, ".git")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cmd  *exec.Cmd
		want string
	}{
		{binate(t, "hg:"+repo+"@main", dir), `unknown backend "hg"`},
		{binate(t, "git:"+dir+"/missing@main", dir), "no directory before an @"},
		{agitate(t, repo, repo+"@.", "."), "unable to compare two work trees"},
		{agitate(t, repo, repo+"@:", ":"), "the index must be compared with a revision or the work tree"},
		{agitate(t, dir, alias+"@:", repo+"@:"), "the index must be compared with a revision or the work tree"},
		{agitate(t, dir, dir+"@main"), "not a git repository"},
		// With -g, git: starts a revision's path: a hint says to drop it.
		{agitate(t, repo, "git:"+repo+"@HEAD~", "HEAD"), "(with -g, give another repository's side without git:, as " + repo + "@HEAD~)"},
		// A relative directory gets ./, without which it would be a revision.
		{agitate(t, alias, "git:inner@HEAD~", "HEAD"), "(with -g, give another repository's side without git:, as ./inner@HEAD~)"},
		{binate(t, "git:"+repo+"@HEAD:same", dir), "the revision names a file"},
		{binate(t, "-q", "git:"+repo+"@HEAD", dir), "--quick with a git side"},
	} {
		if tc.cmd.Dir == "" {
			tc.cmd.Dir = dir
		}
		pt := ptytest.Open(t, 100, 30)
		pt.Start(tc.cmd)
		pt.Wait(0, tc.want)
		if code := pt.Exit(); code != 2 {
			t.Errorf("%q: exit status %d", tc.cmd.Args, code)
		}
	}
}

// TestReadStatus checks that the archives' size is known unless one is a
// pipe, also with one empty.
func TestReadStatus(t *testing.T) {
	for _, tc := range []struct {
		sizes [2]int64
		total int64
	}{
		{[2]int64{0, 100}, 100},
		{[2]int64{50, 100}, 150},
		{[2]int64{-1, 100}, 0},
	} {
		c := &child{sizes: tc.sizes}
		c.archives = [2]*os.File{os.Stdin, os.Stdin}
		if got := c.readStatus().Total; got != tc.total {
			t.Errorf("sizes %v: total %d, want %d", tc.sizes, got, tc.total)
		}
	}
}

// The loading box shows the entry of an archive still being read, not of
// one read whole.
func TestReadStatusCurrent(t *testing.T) {
	c := &child{archives: [2]*os.File{os.Stdin, os.Stdin}, sizes: [2]int64{-1, -1}}
	c.progress[0].Begin("a.tar:second")
	c.progress[1].Begin("b.tar:last")
	if got := c.readStatus().Current; got != "b.tar:last" {
		t.Errorf("both being read: %q", got)
	}
	c.done[1].Store(true)
	if got := c.readStatus().Current; got != "a.tar:second" {
		t.Errorf("the right read: %q", got)
	}
	c.done[0].Store(true)
	if got := c.readStatus().Current; got != "" {
		t.Errorf("both read: %q", got)
	}
}
