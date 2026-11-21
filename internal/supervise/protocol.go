// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package supervise connects the restricted child, which scans and draws,
// with the unrestricted parent, which runs the editor and stops the job for
// Ctrl+z. They talk over a socket pair, one line per message:
//
//	screen on|off                      notice: the child took or released the terminal
//	edit left|right quoted-path        request: edit a file, answered done or error
//	suspend                            request: stop the job, answered done once resumed
//
// Answers are "done" or "error" followed by a quoted message. Paths and
// messages are quoted as Go strings, so any byte can be sent.
package supervise

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Side is a side of the comparison.
type Side string

const (
	Left  Side = "left"
	Right Side = "right"
)

// Request is a request from the child, as the parent receives it.
type Request struct {
	// Kind is "screen", "edit" or "suspend".
	Kind string
	// Screen is the terminal's new state, of a screen notice.
	Screen bool
	// Side and Path are an edit request's side and path relative to that
	// side's root.
	Side Side
	Path string
}

// Client is the child's end.
type Client struct {
	mu sync.Mutex
	w  io.Writer
	r  *bufio.Reader
}

// NewClient returns a client talking over rw.
func NewClient(rw io.ReadWriter) *Client {
	return &Client{w: rw, r: bufio.NewReader(rw)}
}

// Screen tells the parent whether the child holds the terminal, so that
// the parent can release it if the child dies holding it.
func (c *Client) Screen(on bool) error {
	state := "off"
	if on {
		state = "on"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := io.WriteString(c.w, "screen "+state+"\n")
	return err
}

// Edit asks the parent to edit a file and waits until the editor exits.
// path is relative to the side's root.
func (c *Client) Edit(side Side, path string) error {
	return c.call("edit " + string(side) + " " + strconv.Quote(path))
}

// Suspend asks the parent to stop the job, and waits until it's resumed.
func (c *Client) Suspend() error {
	return c.call("suspend")
}

func (c *Client) call(line string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := io.WriteString(c.w, line+"\n"); err != nil {
		return err
	}
	answer, err := c.r.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("no answer from the parent (%w)", err)
	}
	answer = strings.TrimSuffix(answer, "\n")
	if answer == "done" {
		return nil
	}
	if msg, ok := strings.CutPrefix(answer, "error "); ok {
		if text, err := strconv.Unquote(msg); err == nil {
			return errors.New(text)
		}
	}
	return fmt.Errorf("invalid answer from the parent (%q)", answer)
}

// ParseRequest parses one line from the child, without its line feed.
// The child is restricted but not trusted: anything malformed is an error.
func ParseRequest(line string) (Request, error) {
	kind, rest, _ := strings.Cut(line, " ")
	switch kind {
	case "screen":
		switch rest {
		case "on":
			return Request{Kind: kind, Screen: true}, nil
		case "off":
			return Request{Kind: kind}, nil
		}
	case "suspend":
		if rest == "" && !strings.Contains(line, " ") {
			return Request{Kind: kind}, nil
		}
	case "edit":
		side, quoted, _ := strings.Cut(rest, " ")
		if Side(side) != Left && Side(side) != Right {
			return Request{}, fmt.Errorf("invalid side %q", side)
		}
		path, err := strconv.Unquote(quoted)
		if err != nil || !strings.HasPrefix(quoted, `"`) {
			return Request{}, fmt.Errorf("invalid path %q", quoted)
		}
		return Request{Kind: kind, Side: Side(side), Path: path}, nil
	}
	return Request{}, fmt.Errorf("invalid request %q", line)
}

// Answer formats the answer to a request: done for a nil error.
func Answer(err error) string {
	if err == nil {
		return "done\n"
	}
	return "error " + strconv.Quote(err.Error()) + "\n"
}
