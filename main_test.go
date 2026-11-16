// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
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
)

// asBinateEnv makes the test binary run main, so tests can start binate as
// a program, and binate can start itself again as the child.
const asBinateEnv = "BINATE_TEST_AS_BINATE"

func TestMain(m *testing.M) {
	if os.Getenv(asBinateEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
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
		{[]string{"-C", "-1", dir, dir}, "invalid context lines: -1"},
		{[]string{"-x", "[", dir, dir}, "invalid exclude pattern"},
		{[]string{"--compare", "+size", dir, dir}, `invalid --compare: unknown attribute "size"`},
		{[]string{"-c", "+size", dir, dir}, `invalid --compare: unknown attribute "size"`},
		{[]string{"--compare", "-owner,mtime", dir, dir}, `invalid --compare: "mtime": give every attribute a + or -, or none`},
		{[]string{dir + "/missing", dir}, "missing: no such file or directory"},
		{[]string{"--bogus", dir, dir}, "unknown flag: --bogus"},
	} {
		cmd := binate(t, tt.args...)
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
		_, got, err := parseOptions(append(tt.flags, "left", "right"))
		if err != nil {
			t.Errorf("%q: %v", tt.flags, err)
		} else if got.compare != tt.want {
			t.Errorf("%q: compare = %v, want %v", tt.flags, got.compare, tt.want)
		}
	}
}

func TestHelpFits80Columns(t *testing.T) {
	s, _, err := parseOptions([]string{"--help"})
	if err != args.ErrHelp {
		t.Fatal(err)
	}
	help := s.Help(0)
	if got := s.Help(80); got != help {
		t.Errorf("help wraps at 80 columns:\n%s", got)
	}
	for _, line := range strings.Split(help, "\n") {
		if len(line) > 80 {
			t.Errorf("help line exceeds 80 columns (%d): %s", len(line), line)
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

func TestTwoFiles(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\ntwo\n", "b.txt": "one\nTWO\n", "bin": "\xff\xfe"})
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")

	pt, offset := startInTerminal(t, a, b)
	scr := pt.WaitScreen(100, 30, offset, "1 changed")
	if !strings.Contains(scr, "2 two") || !strings.Contains(scr, "| │2 TWO") {
		t.Errorf("screen:\n%s", scr)
	}
	// The diff view's panels start with the paths too.
	if !regexp.MustCompile(`│ path │ /\S*a\.txt *│   │ path │ /\S*b\.txt *│`).MatchString(scr) {
		t.Errorf("panels without the paths:\n%s", scr)
	}
	quit(t, pt, offset, "x")
	pt.Wait(offset, leaveScreen)
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}

	// /dev/null is an empty side.
	pt, offset = startInTerminal(t, "/dev/null", a)
	pt.WaitScreen(100, 30, offset, "[2 right]")
	pressEsc(pt) // Esc never quits, nor does q
	pt.Write("q")
	quit(t, pt, offset, "x")
	pt.Wait(offset, leaveScreen)
	pt.Exit()

	// Binary input shows why on an empty diff view.
	pt, offset = startInTerminal(t, a, filepath.Join(dir, "bin"))
	pt.WaitScreen(100, 30, offset, "│Unable to compare files due to an error on the│")
	pt.WaitScreen(100, 30, offset, "│Unable to show data that is not UTF-8 encoded. │")
	quit(t, pt, offset, "x")
	pt.Exit()

	pt, offset = startInTerminal(t, filepath.Join(dir, "bin"), filepath.Join(dir, "bin"))
	pt.WaitScreen(100, 30, offset, "│Unable to show data that is not UTF-8 encoded.│   │Unable to show data that is not UTF-8 encoded. │")
	quit(t, pt, offset, "x")
	pt.Exit()
}

func TestPipeInput(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\ntwo\n"})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = w.WriteString("one\nthree\n")
		_ = w.Close()
	}()
	pt := ptytest.Open(t, 100, 30)
	cmd := binate(t, filepath.Join(dir, "a.txt"), "/dev/fd/3")
	cmd.ExtraFiles = []*os.File{r}
	pt.Start(cmd)
	_ = r.Close()
	offset := pt.Wait(0, enterScreen)
	pt.WaitScreen(100, 30, offset, "1 changed")
	// The file's panel is read in the background, after the pipe is: wait
	// for it, as "1 changed" can be drawn first. Waited for only after
	// "1 changed", it is the panel read once the pipe was, not before.
	scr := pt.WaitScreen(100, 30, offset, "type │ file")
	// The pipe's panel describes the pipe, read before it was.
	if !strings.Contains(scr, "| │2 three") || !strings.Contains(scr, "type │ pipe") {
		t.Errorf("screen:\n%s", scr)
	}
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

// TestDescriptorInputs checks that inputs named by inherited descriptors,
// as /dev/fd/3 from "3<file", name the same files in the child, though
// the socket to it and its streams are passed as descriptors too.
func TestDescriptorInputs(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\ntwo\n", "b.txt": "one\nTWO\n",
		"l/f": "a", "r/f": "b"})
	open := func(name string) *os.File {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = w.WriteString("one\nthree\n")
		_ = w.Close()
	}()
	for _, tt := range []struct {
		name  string
		files []*os.File
		args  []string
		want  string
	}{
		{"regular file", []*os.File{open("a.txt")}, []string{"/dev/fd/3", filepath.Join(dir, "b.txt")}, "| │2 TWO"},
		{"file and pipe", []*os.File{open("a.txt"), r}, []string{"/dev/fd/3", "/dev/fd/4"}, "| │2 three"},
		{"directories", []*os.File{open("l"), open("r")}, []string{"/dev/fd/3", "/dev/fd/4"}, "[1 contents]"},
	} {
		pt := ptytest.Open(t, 100, 30)
		cmd := binate(t, tt.args...)
		cmd.ExtraFiles = tt.files
		pt.Start(cmd)
		offset := pt.Wait(0, enterScreen)
		if scr := pt.WaitScreen(100, 30, offset, tt.want); strings.Contains(scr, "Unable") {
			t.Errorf("%s:\n%s", tt.name, scr)
		}
		quit(t, pt, offset, "x")
		if code := pt.Exit(); code != 0 {
			t.Errorf("%s: exit status %d", tt.name, code)
		}
	}
	_ = r.Close()
}

// TestSlowPipe checks that a pipe ending after startup is shown without
// waiting for a key.
func TestSlowPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	pt := ptytest.Open(t, 100, 30)
	cmd := binate(t, "/dev/fd/3", "/dev/null")
	cmd.ExtraFiles = []*os.File{r}
	pt.Start(cmd)
	_ = r.Close()
	offset := pt.Wait(0, enterScreen)
	pt.WaitScreen(100, 30, offset, "reading the input…")
	_, _ = w.WriteString("ready\n")
	_ = w.Close()
	pt.WaitScreen(100, 30, offset, "1 ready")
	quit(t, pt, offset, "x")
	pt.Exit()
}

// TestPendingPipe checks that a pipe that doesn't end yet doesn't keep
// binate from quitting.
func TestPendingPipe(t *testing.T) {
	for _, key := range []string{"\x03", "x"} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		pt := ptytest.Open(t, 100, 30)
		cmd := binate(t, "/dev/fd/3", "/dev/null")
		cmd.ExtraFiles = []*os.File{r}
		pt.Start(cmd)
		_ = r.Close()
		offset := pt.Wait(0, enterScreen)
		pt.WaitScreen(100, 30, offset, "reading the input…")
		quit(t, pt, offset, key)
		pt.Exit()
		_ = w.Close()
	}
}

func TestFileAndDirectory(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a/name.txt": "left\n", "b/name.txt": "right\n"})
	pt, offset := startInTerminal(t, filepath.Join(dir, "a", "name.txt"), filepath.Join(dir, "b"))
	scr := pt.WaitScreen(100, 30, offset, "1 changed")
	if !strings.Contains(scr, "b/name.txt") {
		t.Errorf("screen:\n%s", scr)
	}
	quit(t, pt, offset, "x")
	pt.Exit()
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
	screen("| │2 B")
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

	// One side only: edited at once, with a note. Deleted: gone.
	pt.Write("*")
	screen("  other.txt")
	pt.Write("\x1b[F")
	e.do("delete")
	pt.Write("e")
	screen("edited the left side, as the right side is missing")
	// The note shows until the next key.
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
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

func TestEditTwoFiles(t *testing.T) {
	e := newFakeEditor(t)
	dir := writeFiles(t, map[string]string{"a.txt": "one\n", "b.txt": "two\n"})
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	pt, offset := e.start(a, b)
	screen := func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
	screen("1 changed")

	e.do(`write:one\nmore\n`)
	pt.Write("e")
	screen("Select side to edit")
	pt.Write("l")
	screen("> │2 more")

	// Deleted: the diff view says so instead of showing old contents.
	e.do("delete")
	pt.Write("e")
	screen("Select side to edit")
	pt.Write("h")
	// Broken into lines in the left box, and the right box says which side
	// failed.
	scr := screen("│Failed reading this file (no such file or     │   │Unable to compare files due to an error on the │")
	if !strings.Contains(scr, "│directory).                                   │   │left side.") {
		t.Errorf("message:\n%s", scr)
	}
	if got := e.log(); len(got) != 2 || got[0] != b || got[1] != a {
		t.Errorf("editor given %q", got)
	}
	quit(t, pt, offset, "x")
	pt.Exit()

	// /dev/null can't be edited: the other side is edited at once.
	e.do(`write:changed\n`)
	pt, offset = e.start("/dev/null", b)
	screen = func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
	screen("[2 right]")
	pt.Write("e")
	screen("edited the right side, as the left side is not a regular file")
	screen("> │1 changed")
	quit(t, pt, offset, "x")
	pt.Exit()
}

func TestEditWithoutEditor(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.txt": "one\n", "b.txt": "two\n"})
	pt, offset := startInTerminal(t, filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt"))
	pt.WaitScreen(100, 30, offset, "1 changed")
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

// The child takes the inputs as the parent resolved them, from their kinds,
// without looking again.
func TestInputsEncode(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "f.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][2]string{{dir, other}, {file, other}, {other, file}, {file, "/dev/null"}} {
		in, err := resolveInputs(args[0], args[1])
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeInputs(args[0], args[1], in.encode())
		if err != nil || got != in {
			t.Errorf("%q: decoded %+v, %v, want %+v", args, got, err, in)
		}
	}
	git := gitInputs("HEAD~", "main:dir")
	if got, err := decodeInputs("HEAD~", "main:dir", git.encode()); err != nil || got != git || git.encode() != "gg" {
		t.Errorf("git: decoded %+v, %v, want %+v", got, err, git)
	}
	for _, code := range []string{"", "d", "ddd", "Dd", "FF", "xd"} {
		if _, err := decodeInputs("a", "b", code); err == nil {
			t.Errorf("decodeInputs(%q) succeeded", code)
		}
	}
}

// gitRepo makes a repository with two commits, in a temporary directory,
// running git, or skips the test if git is not installed.
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
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.org", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.org")
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
	run("commit", "-q", "-m", "first")
	if err := os.Remove(filepath.Join(dir, "gone")); err != nil {
		t.Fatal(err)
	}
	write(map[string]string{"dir/changed": "one\nTWO\n", "new": "n\n"})
	run("add", "-A")
	run("commit", "-q", "-m", "second")
	run("repack", "-adq")
	return dir
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
	screen("[1 identical] [1 left] [1 right] [1 contents]")
	// The panels show the revisions' paths, as git names them.
	pt.Write("n")
	scr := screen("main:dir/changed")
	if !regexp.MustCompile(`│ path │ HEAD~:dir/changed *│   │ path │ main:dir/changed *│`).MatchString(scr) {
		t.Errorf("panels without the paths:\n%s", scr)
	}
	pt.Write("e")
	screen("Files in git revisions can't be edited.")
	pt.Write("j")
	pt.Write("\r")
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

func TestGitFiles(t *testing.T) {
	repo := gitRepo(t)
	pt := startIn(t, filepath.Join(repo, "dir"), "--git", "HEAD~:./changed", "HEAD:dir/changed")
	offset := pt.Wait(0, enterScreen)
	scr := pt.WaitScreen(100, 30, offset, "1 changed")
	if !strings.Contains(scr, "| │2 TWO") || !regexp.MustCompile(`│ path │ HEAD~:\./changed *│   │ path │ HEAD:dir/changed *│`).MatchString(scr) {
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
func TestGitErrors(t *testing.T) {
	repo := gitRepo(t)
	for _, tc := range []struct {
		dir  string
		args []string
		want string
	}{
		{repo, []string{"-g", "nope", "HEAD"}, "error: revision nope: unknown revision"},
		{repo, []string{"-g", "HEAD@{1}", "HEAD"}, "not supported"},
		{repo, []string{"-g", "HEAD", "HEAD:same"}, "unable to compare a tree with a file"},
		{t.TempDir(), []string{"-g", "HEAD", "HEAD"}, "not a git repository"},
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
