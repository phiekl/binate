// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package ptytest runs programs in a pseudo-terminal for tests.
package ptytest

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/display"
)

// Term is a program running in a pseudo-terminal of its own, as the
// foreground job of a new session.
type Term struct {
	t      *testing.T
	Master *os.File
	Slave  *os.File
	Cmd    *exec.Cmd

	mu     sync.Mutex
	out    bytes.Buffer
	closed chan struct{}
}

// Open returns a pseudo-terminal of the given size, with no program yet.
func Open(t *testing.T, cols, rows int) *Term {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals: %v", err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("TIOCGPTN: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("TIOCSPTLCK: %v", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Skipf("opening the pseudo-terminal: %v", err)
	}
	pt := &Term{t: t, Master: master, Slave: slave, closed: make(chan struct{})}
	pt.Resize(cols, rows)
	go pt.read()
	t.Cleanup(pt.close)
	return pt
}

// Start runs cmd in the terminal, as a session leader with the terminal as
// its controlling terminal.
func (pt *Term) Start(cmd *exec.Cmd) {
	pt.t.Helper()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pt.Slave, pt.Slave, pt.Slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		pt.t.Fatalf("starting %s: %v", cmd.Path, err)
	}
	pt.Cmd = cmd
}

func (pt *Term) read() {
	defer close(pt.closed)
	buf := make([]byte, 4096)
	for {
		n, err := pt.Master.Read(buf)
		pt.mu.Lock()
		pt.out.Write(buf[:n])
		pt.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (pt *Term) close() {
	if pt.Cmd != nil && pt.Cmd.ProcessState == nil {
		_ = pt.Cmd.Process.Kill()
		_ = pt.Cmd.Wait()
	}
	_ = pt.Slave.Close()
	_ = pt.Master.Close()
}

// Resize sets the terminal's size.
func (pt *Term) Resize(cols, rows int) {
	pt.t.Helper()
	ws := &unix.Winsize{Col: uint16(cols), Row: uint16(rows)}
	if err := unix.IoctlSetWinsize(int(pt.Master.Fd()), unix.TIOCSWINSZ, ws); err != nil {
		pt.t.Fatalf("TIOCSWINSZ: %v", err)
	}
}

// Write types s.
func (pt *Term) Write(s string) {
	pt.t.Helper()
	if _, err := pt.Master.WriteString(s); err != nil {
		pt.t.Fatalf("writing to the terminal: %v", err)
	}
}

// Output returns everything written to the terminal so far.
func (pt *Term) Output() string {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return pt.out.String()
}

// Wait waits until the output written after offset contains want, and
// returns the offset just after it.
func (pt *Term) Wait(offset int, want string) int {
	pt.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		out := pt.Output()
		if offset > len(out) {
			offset = len(out)
		}
		if i := strings.Index(out[offset:], want); i >= 0 {
			return offset + i + len(want)
		}
		if time.Now().After(deadline) {
			pt.t.Fatalf("timed out waiting for %q; output after %d:\n%q", want, offset, out[offset:])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Exit waits for the program to exit and returns its exit status.
func (pt *Term) Exit() int {
	pt.t.Helper()
	done := make(chan error, 1)
	go func() { done <- pt.Cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = pt.Cmd.Process.Kill()
		pt.t.Fatalf("timed out waiting for the program to exit; output:\n%q", pt.Output())
	}
	return pt.Cmd.ProcessState.ExitCode()
}

// Termios returns the terminal's current settings.
func (pt *Term) Termios() *unix.Termios {
	pt.t.Helper()
	tios, err := unix.IoctlGetTermios(int(pt.Slave.Fd()), unix.TCGETS)
	if err != nil {
		pt.t.Fatalf("TCGETS: %v", err)
	}
	return tios
}

// Children returns the pids of pid's child processes, which any of its
// threads may have started.
func Children(pid int) []int {
	tasks, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
	var pids []int
	for _, task := range tasks {
		b, err := os.ReadFile(task)
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(b)) {
			var n int
			if _, err := fmt.Sscan(f, &n); err == nil {
				pids = append(pids, n)
			}
		}
	}
	return pids
}

// Screen returns what a terminal of the size given to Open would show
// after the output from offset on, as lines of text: it applies cursor
// positioning (CSI H), clearing the screen (CSI 2J) and text, which is all
// binate draws with, and ignores other sequences.
func (pt *Term) Screen(cols, rows, offset int) string {
	grid, _ := pt.emulate(cols, rows, offset)
	var b strings.Builder
	for _, line := range grid {
		b.WriteString(strings.TrimRight(strings.ReplaceAll(string(line), "\x00", ""), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// Links returns the target of the hyperlink (OSC 8) each cell of the
// screen Screen returns is in, by row and column, "" for none.
func (pt *Term) Links(cols, rows, offset int) [][]string {
	_, links := pt.emulate(cols, rows, offset)
	return links
}

// emulate returns the cells of the screen Screen describes, and the
// hyperlink each is in.
func (pt *Term) emulate(cols, rows, offset int) ([][]rune, [][]string) {
	grid := make([][]rune, rows)
	links := make([][]string, rows)
	clearGrid := func() {
		for y := range grid {
			grid[y] = []rune(strings.Repeat(" ", cols))
			links[y] = make([]string, cols)
		}
	}
	clearGrid()
	link := ""
	out := pt.Output()
	if offset > len(out) {
		offset = len(out)
	}
	s := []rune(out[offset:])
	x, y := 0, 0
	for i := 0; i < len(s); i++ {
		r := s[i]
		if r != 0x1b {
			if r < 0x20 {
				continue
			}
			if y < rows && x < cols {
				grid[y][x], links[y][x] = r, link
				if display.RuneWidth(r) == 2 && x+1 < cols {
					grid[y][x+1], links[y][x+1] = 0, link
				}
			}
			x += display.RuneWidth(r)
			continue
		}
		if i+1 < len(s) && s[i+1] == ']' {
			// OSC, as a hyperlink: up to its end, ST or BEL.
			j := i + 2
			for j < len(s) && s[j] != 0x07 && !(s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\') {
				j++
			}
			if target, ok := strings.CutPrefix(string(s[i+2:j]), "8;;"); ok {
				link = target
			}
			if j < len(s) && s[j] == 0x1b {
				j++
			}
			i = j
			continue
		}
		if i+1 >= len(s) || s[i+1] != '[' {
			i++
			continue
		}
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
			j++
		}
		if j >= len(s) {
			break
		}
		params := string(s[i+2 : j])
		switch s[j] {
		case 'H':
			row, col := 1, 1
			fmt.Sscanf(params, "%d;%d", &row, &col)
			y, x = row-1, col-1
		case 'J':
			if params == "2" {
				clearGrid()
			}
		}
		i = j
	}
	return grid, links
}

// WaitScreen waits until the screen, as Screen returns it, contains want,
// and returns it.
func (pt *Term) WaitScreen(cols, rows, offset int, want string) string {
	pt.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		scr := pt.Screen(cols, rows, offset)
		if strings.Contains(scr, want) {
			return scr
		}
		if time.Now().After(deadline) {
			if os.Getenv("PTYTEST_DUMP") != "" && pt.Cmd != nil {
				for _, c := range Children(pt.Cmd.Process.Pid) {
					_ = unix.Kill(c, unix.SIGABRT)
				}
				time.Sleep(2 * time.Second)
			}
			out := pt.Output()
			pt.t.Fatalf("timed out waiting for %q on the screen:\n%s\nlast output: %q", want, scr, out[max(0, len(out)-20000):])
		}
		time.Sleep(10 * time.Millisecond)
	}
}
