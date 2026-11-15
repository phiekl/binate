// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/diff"
	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/scan"
)

func TestErrorSentences(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"the editor exited with status 1", "The editor exited with status 1."},
		{"Environment variable VISUAL or EDITOR must be set.", "Environment variable VISUAL or EDITOR must be set."},
		{"", ""},
	} {
		if got := sentence(tt.in); got != tt.want {
			t.Errorf("sentence(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	for _, tt := range []struct {
		err  error
		want string
	}{
		{&sideError{err: diff.ErrBinary}, "Only UTF-8 encoded text files can be opened (the left side is not)."},
		{&sideError{right: true, err: diff.ErrTooLarge}, "Only files up to 10 MiB can be opened (the right side is larger)."},
		{&sideError{err: files.ErrNotRegular}, "The left side is no longer a regular file."},
		{&sideError{right: true, err: unix.EACCES}, "Failed reading the right side (permission denied)."},
		{fmt.Errorf("wrapped: %w", &sideError{err: diff.ErrBinary}), "Only UTF-8 encoded text files can be opened (the left side is not)."},
		{errors.New("plain"), "plain"},
	} {
		if got := errorText(tt.err); got != tt.want {
			t.Errorf("errorText(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
	pair := func(l, r scan.Kind) *scan.Node { return &scan.Node{L: scan.Side{Kind: l}, R: scan.Side{Kind: r}} }
	for _, tt := range []struct {
		n    *scan.Node
		want []string
	}{
		{pair(scan.File, scan.Missing), nil},
		{pair(scan.Symlink, scan.Symlink), []string{"Only regular files can be opened (both sides are symlinks)."}},
		{pair(scan.File, scan.FIFO), []string{"Only regular files can be opened (the right side is a FIFO)."}},
		{pair(scan.CharDev, scan.Missing), []string{"Only regular files can be opened (the left side is a character device)."}},
		{pair(scan.Symlink, scan.Socket), []string{"Only regular files can be opened:", "The left side is a symlink.", "The right side is a socket."}},
	} {
		if got := whyNotOpened(tt.n); !slices.Equal(got, tt.want) {
			t.Errorf("whyNotOpened(%v, %v) = %q, want %q", tt.n.L.Kind, tt.n.R.Kind, got, tt.want)
		}
	}
}

func TestSuspendError(t *testing.T) {
	if got, want := suspendError(unix.EPERM), "Failed stopping the job (operation not permitted)."; got != want {
		t.Errorf("suspendError = %q, want %q", got, want)
	}
}

func TestErrorTexts(t *testing.T) {
	if got := errorTexts(&sideError{err: diff.ErrBinary}); !slices.Equal(got, []string{"Only UTF-8 encoded text files can be opened (the left side is not)."}) {
		t.Errorf("one side: %q", got)
	}
	if got := errorTexts(errors.New("plain")); !slices.Equal(got, []string{"plain"}) {
		t.Errorf("plain: %q", got)
	}
}

func TestBothSidesErrorSentences(t *testing.T) {
	for _, tt := range []struct {
		left, right error
		want        []string
	}{
		// One sentence where the sides failed alike, else a list of two.
		{diff.ErrBinary, diff.ErrBinary, []string{"Only UTF-8 encoded text files can be opened (neither side is)."}},
		{diff.ErrTooLarge, diff.ErrTooLarge, []string{"Only files up to 10 MiB can be opened (both sides are larger)."}},
		{files.ErrNotRegular, files.ErrNotRegular, []string{"The left side is no longer a regular file.", "The right side is no longer a regular file."}},
		{diff.ErrBinary, diff.ErrTooLarge, []string{"Only UTF-8 encoded text files can be opened (the left side is not).", "Only files up to 10 MiB can be opened (the right side is larger)."}},
		{unix.EACCES, diff.ErrBinary, []string{"Failed reading the left side (permission denied).", "Only UTF-8 encoded text files can be opened (the right side is not)."}},
	} {
		err := &bothSidesError{
			left:  &sideError{err: tt.left},
			right: &sideError{right: true, err: tt.right},
		}
		wrapped := fmt.Errorf("wrapped: %w", err)
		if got := errorTexts(wrapped); !slices.Equal(got, tt.want) {
			t.Errorf("errorTexts(%v) = %q, want %q", err, got, tt.want)
		}
		if got, want := errorText(wrapped), strings.Join(tt.want, " "); got != want {
			t.Errorf("errorText(%v) = %q, want %q", err, got, want)
		}
		if !errors.Is(err, tt.left) || !errors.Is(err, tt.right) {
			t.Errorf("%v does not unwrap to both errors", err)
		}
	}
}
