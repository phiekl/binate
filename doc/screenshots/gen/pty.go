// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// session is a program running in a pseudo-terminal, with what it writes
// fed to a terminal emulator.
type session struct {
	master *os.File
	cmd    *exec.Cmd
	log    *logger

	mu   sync.Mutex
	vt   *term
	last time.Time // when the program last wrote
	done chan struct{}
}

// options are how a program is started.
type options struct {
	dir  string
	env  []string // the whole environment
	root bool     // run as root of a user namespace
	log  *logger
}

// start runs argv in a new pseudo-terminal of cols × rows.
func start(cols, rows int, o options, argv ...string) (*session, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err == nil {
		err = unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0)
	}
	if err == nil {
		err = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ,
			&unix.Winsize{Col: uint16(cols), Row: uint16(rows)})
	}
	if err != nil {
		master.Close()
		return nil, err
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		master.Close()
		return nil, err
	}
	defer slave.Close()

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = o.dir
	cmd.Env = o.env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if o.root {
		// Files owned by the caller show as owned by root, as on a real
		// root file system.
		cmd.SysProcAttr.Cloneflags = syscall.CLONE_NEWUSER
		cmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}
		cmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}
	}
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, fmt.Errorf("starting %s: %w", argv[0], err)
	}
	s := &session{master: master, cmd: cmd, log: o.log, vt: newTerm(cols, rows), last: time.Now(), done: make(chan struct{})}
	go s.read()
	return s, nil
}

func (s *session) read() {
	defer close(s.done)
	buf := make([]byte, 1<<16)
	for {
		n, err := s.master.Read(buf)
		s.mu.Lock()
		s.vt.write(buf[:n])
		s.last = time.Now()
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// settle waits until the program has written nothing for quiet.
func (s *session) settle(quiet time.Duration) {
	for {
		s.mu.Lock()
		d := time.Since(s.last)
		s.mu.Unlock()
		if d >= quiet {
			return
		}
		time.Sleep(quiet - d)
	}
}

// waitFor waits until the screen shows all of texts, or fails after
// timeout with the screen in the error.
func (s *session) waitFor(timeout time.Duration, texts ...string) error {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		screen := s.vt.text()
		s.mu.Unlock()
		missing := ""
		for _, t := range texts {
			if !strings.Contains(screen, t) {
				missing = t
				break
			}
		}
		if missing == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the screen doesn't show %q:\n%s", missing, screen)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// typeKeys writes each key in turn, and waits for the screen to change and
// settle before the next. Each key must change the screen. Keys typed
// ahead while binate is busy would be handled together, and it would
// scroll only once, for the last.
func (s *session) typeKeys(keys []string) error {
	for _, k := range keys {
		s.mu.Lock()
		before := s.vt.text()
		s.mu.Unlock()
		if _, err := s.master.Write([]byte(k)); err != nil {
			return err
		}
		t := time.Now()
		deadline := t.Add(30 * time.Second)
		for {
			s.mu.Lock()
			now := s.vt.text()
			s.mu.Unlock()
			if now != before {
				if d := time.Since(t); d > time.Second {
					s.log.printf("binate took %.1fs to handle the key %q", d.Seconds(), k)
				}
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("the key %q didn't change the screen:\n%s", k, now)
			}
			time.Sleep(20 * time.Millisecond)
		}
		s.settle(100 * time.Millisecond)
	}
	return nil
}

// close ends the program.
func (s *session) close() {
	s.cmd.Process.Kill()
	s.cmd.Wait()
	s.master.Close()
	<-s.done
}
