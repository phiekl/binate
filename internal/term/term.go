// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package term is binate's terminal layer, written on x/term and x/sys:
// raw mode and the alternate screen, a key parser, and a cell buffer that
// writes only what changed. It assumes an xterm-style, UTF-8 terminal.
package term

import (
	"errors"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
	xterm "golang.org/x/term"
)

// EscTimeout is how long a lone ESC waits for the rest of a sequence
// before it is taken as the Esc key. A slow connection that splits a
// sequence for longer makes it read as Esc and other keys.
var EscTimeout = 25 * time.Millisecond

const (
	enterScreen = "\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J"
	// Erase the last frame while still on the alternate screen. Terminals
	// that leave that buffer visible must not leave UI text by the prompt.
	leaveScreen = "\x1b[0m\x1b[H\x1b[2J\x1b[?1049l\x1b[?25h"
)

// Terminal is the terminal binate draws on: keys are read from in, and
// the screen written to out. Reading happens only in Next, so while the
// terminal is released, as for the editor, nothing reads from it.
type Terminal struct {
	in, out *os.File
	state   *xterm.State
	active  bool

	wakeR, wakeW int
	pending      []byte
	buf          []byte
}

// Open returns the terminal on in and out, not yet entered.
func Open(in, out *os.File) (*Terminal, error) {
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_NONBLOCK|unix.O_CLOEXEC); err != nil {
		return nil, err
	}
	return &Terminal{in: in, out: out, wakeR: p[0], wakeW: p[1], buf: make([]byte, 4096)}, nil
}

// Enter puts the terminal in raw mode and switches to the alternate
// screen.
func (t *Terminal) Enter() error {
	if t.active {
		return nil
	}
	state, err := xterm.MakeRaw(int(t.in.Fd()))
	if err != nil {
		return err
	}
	t.state = state
	t.active = true
	t.pending = nil
	_, err = io.WriteString(t.out, enterScreen)
	return err
}

// Leave returns to the main screen and the settings Enter found.
func (t *Terminal) Leave() error {
	if !t.active {
		return nil
	}
	t.active = false
	_, werr := io.WriteString(t.out, leaveScreen)
	rerr := xterm.Restore(int(t.in.Fd()), t.state)
	return errors.Join(werr, rerr)
}

// Out returns where the screen is written.
func (t *Terminal) Out() io.Writer {
	return t.out
}

// Size returns the terminal's columns and rows.
func (t *Terminal) Size() (int, int, error) {
	ws, err := unix.IoctlGetWinsize(int(t.out.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return int(ws.Col), int(ws.Row), nil
}

// Wake makes a waiting Next return. It may be called from any goroutine.
func (t *Terminal) Wake() {
	// A full pipe already wakes Next.
	_, _ = unix.Write(t.wakeW, []byte{0})
}

// Next waits for keys, a Wake, or the timeout (negative to wait without
// one), and returns the keys read and whether Wake was called.
func (t *Terminal) Next(timeout time.Duration) ([]Key, bool, error) {
	deadline := time.Time{}
	if timeout >= 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		wait := -1
		if !deadline.IsZero() {
			wait = int(max(0, time.Until(deadline).Milliseconds()))
		}
		if len(t.pending) > 0 {
			esc := int(EscTimeout.Milliseconds())
			if wait < 0 || esc < wait {
				wait = esc
			}
		}
		fds := []unix.PollFd{
			{Fd: int32(t.in.Fd()), Events: unix.POLLIN},
			{Fd: int32(t.wakeR), Events: unix.POLLIN},
		}
		n, err := unix.Poll(fds, wait)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if n == 0 {
			if len(t.pending) > 0 {
				keys, rest := ParseKeys(t.pending, true)
				t.pending = rest
				return keys, false, nil
			}
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return nil, false, nil
			}
			continue
		}
		woke := false
		if fds[1].Revents != 0 {
			for {
				if n, _ := unix.Read(t.wakeR, t.buf); n <= 0 {
					break
				}
			}
			woke = true
		}
		var keys []Key
		if fds[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0 {
			n, err := unix.Read(int(t.in.Fd()), t.buf)
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			if err != nil {
				return nil, woke, err
			}
			if n == 0 {
				return nil, woke, io.EOF
			}
			t.pending = append(t.pending, t.buf[:n]...)
			keys, t.pending = ParseKeys(t.pending, false)
		}
		if len(keys) > 0 || woke {
			return keys, woke, nil
		}
	}
}
