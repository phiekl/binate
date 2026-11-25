// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/ptytest"
)

// imaginate returns a command running binate as imaginate.
func imaginate(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cmd := binate(t, args...)
	cmd.Args[0] = "/usr/local/bin/" + archivesName
	return cmd
}

// TestArchivesNameSession checks that run as imaginate, binate reads
// archives given without their formats, from files and pipes, its child
// telling each format by the archive's start.
func TestArchivesNameSession(t *testing.T) {
	dir := writeFiles(t, map[string]string{"same": "x\n", "changed": "one\n"})
	other := writeFiles(t, map[string]string{"same": "x\n", "changed": "two\n"})
	tmp := t.TempDir()
	// Named for neither format, as nothing but the start tells it.
	tgz := writeFile(t, filepath.Join(tmp, "dir.bin"), gzipped(t, tarOf(t, dir, nil)))
	zipped := writeFile(t, filepath.Join(tmp, "other.bin"), zipOf(t, other, nil))
	for _, tc := range []struct {
		name        string
		left, right string
		want        string
	}{
		{"tar with zip", tgz, zipped, "[1 identical] [1 contents]"},
		{"tar with its tree", tgz, dir, "[2 identical]"},
		{"with tar:", "tar:" + tgz, zipped, "[1 identical] [1 contents]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pt := ptytest.Open(t, 100, 30)
			pt.Start(imaginate(t, tc.left, tc.right))
			offset := pt.Wait(0, enterScreen)
			pt.WaitScreen(100, 30, offset, tc.want)
			quit(t, pt, offset, "x")
			if code := pt.Exit(); code != 0 {
				t.Errorf("exit status %d", code)
			}
		})
	}

	// From pipes, which the child reads the start of only once.
	pt := ptytest.Open(t, 100, 30)
	cmd := imaginate(t, "/dev/fd/3", "/dev/fd/4")
	var pipes []*os.File
	for _, data := range [][]byte{gzipped(t, tarOf(t, dir, nil)), zipOf(t, other, nil)} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, _ = w.Write(data)
			_ = w.Close()
		}()
		pipes = append(pipes, r)
	}
	cmd.ExtraFiles = pipes
	pt.Start(cmd)
	for _, r := range pipes {
		_ = r.Close()
	}
	offset := pt.Wait(0, enterScreen)
	pt.WaitScreen(100, 30, offset, "[1 identical] [1 contents]")
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("pipes: exit status %d", code)
	}
}

// TestArchivesNameRestricted checks that a file's format, told by
// decompressing its start, is told only once Landlock restricts the child,
// where it can: as binate says, as its minimum ABI may be above the
// kernel's.
func TestArchivesNameRestricted(t *testing.T) {
	dir := writeFiles(t, map[string]string{"f": "x\n"})
	archive := writeFile(t, filepath.Join(t.TempDir(), "a"), gzipped(t, tarOf(t, dir, nil)))
	pt := ptytest.Open(t, 100, 30)
	pt.Start(imaginate(t, "-vv", archive, dir))
	offset := pt.Wait(0, enterScreen)
	pt.WaitScreen(100, 30, offset, "[1 identical]")
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
	out := pt.Output()
	if strings.Contains(out, "landlock: unavailable") {
		t.Skip("no Landlock")
	}
	enabled, told := strings.Index(out, "landlock: enabled"), strings.Index(out, "told the archive's format")
	if enabled < 0 || told < 0 || told < enabled {
		t.Errorf("Landlock enabled at %d, the format told at %d, in %q", enabled, told, out)
	}
}

// TestArchivesNameDetectLimit checks that telling a file's format reads no
// more of it than of a pipe, here zstd's skippable frames of 16 GiB of
// holes, but refuses it at once, before the screen opens.
func TestArchivesNameDetectLimit(t *testing.T) {
	dir := writeFiles(t, map[string]string{"f": "x\n"})
	f, err := os.Create(filepath.Join(t.TempDir(), "a"))
	if err != nil {
		t.Fatal(err)
	}
	const frame = 0xfffffff0
	var off int64
	for range 4 {
		if _, err := f.WriteAt([]byte{0x50, 0x2a, 0x4d, 0x18, 0xf0, 0xff, 0xff, 0xff}, off); err != nil {
			t.Fatal(err)
		}
		off += 8 + frame
	}
	if _, err := f.WriteAt(compressed(t, tarOf(t, dir, nil), func(w io.Writer) (io.WriteCloser, error) { return zstd.NewWriter(w) }), off); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	code, out := failing(t, imaginate(t, f.Name(), dir), false)
	if want := f.Name() + ": more than 16 MiB read to tell the format"; code != 2 || !strings.Contains(out, want) || strings.Contains(out, enterScreen) {
		t.Errorf("exit status %d, output %q, want %q", code, out, want)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("ended after %v", took)
	}
}

// TestArchivesNameFifos checks that run as imaginate, binate reads two
// FIFOs at once, telling each's format as it is read, as a writer writing
// all of the left, more than a pipe holds, before it opens the right
// needs.
func TestArchivesNameFifos(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 1<<17)
	dir := writeFiles(t, map[string]string{"same": "x\n", "changed": "one\n", "big": big})
	other := writeFiles(t, map[string]string{"same": "x\n", "changed": "two\n", "big": big})
	tmp := t.TempDir()
	fifos := [2]string{filepath.Join(tmp, "left"), filepath.Join(tmp, "right")}
	for _, fifo := range fifos {
		if err := unix.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Plain, the left's 2 MiB fill the pipe.
	data := [2][]byte{tarOf(t, dir, nil), zipOf(t, other, nil)}
	go func() {
		for i, fifo := range fifos {
			w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
			if err != nil {
				return
			}
			_, _ = w.Write(data[i])
			_ = w.Close()
		}
	}()
	pt := ptytest.Open(t, 100, 30)
	pt.Start(imaginate(t, fifos[0], fifos[1]))
	offset := pt.Wait(0, enterScreen)
	pt.WaitScreen(100, 30, offset, "[2 identical] [1 contents]")
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

// TestArchivesNameHeader checks that an archive opposite git is labelled
// with the format told, a file's before it is read, and a pipe's once it
// is.
func TestArchivesNameHeader(t *testing.T) {
	repo := gitRepo(t)
	data := zipOf(t, writeFiles(t, map[string]string{"f": "x\n"}), nil)
	archive := writeFile(t, filepath.Join(t.TempDir(), "a"), data)
	for _, pipe := range []bool{false, true} {
		pt := ptytest.Open(t, 100, 30)
		cmd := imaginate(t, archive, "git:"+repo+"@")
		path := archive
		if pipe {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				_, _ = w.Write(data)
				_ = w.Close()
			}()
			cmd = imaginate(t, "/dev/fd/3", "git:"+repo+"@")
			cmd.ExtraFiles = []*os.File{r}
			defer func() { _ = r.Close() }()
			path = "/dev/fd/3"
		}
		pt.Start(cmd)
		offset := pt.Wait(0, enterScreen)
		scr := pt.WaitScreen(100, 30, offset, "[1 left]")
		checkHeader(t, pt, offset, scr, [2]string{"(zip)", path}, [2]string{"(git)", repo})
		quit(t, pt, offset, "x")
		if code := pt.Exit(); code != 0 {
			t.Errorf("pipe %t: exit status %d", pipe, code)
		}
	}
}

// TestArchivesNamePipeErrors checks that a pipe that isn't an archive,
// told as it is read, fails reading, in the error popup.
func TestArchivesNamePipeErrors(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = w.Write(bytes.Repeat([]byte("not an archive "), 100))
		_ = w.Close()
	}()
	cmd := binateWithin(t, 10*time.Second, "/dev/fd/3", t.TempDir())
	cmd.Args[0] = archivesName
	cmd.ExtraFiles = []*os.File{r}
	code, out := failing(t, cmd, true)
	_ = r.Close()
	if want := "/dev/fd/3: not a tar or zip archive by its start"; code != 2 || !strings.Contains(out, want) {
		t.Errorf("exit status %d, output %q, want %q", code, out, want)
	}
}

func TestArchivesNameErrors(t *testing.T) {
	dir := t.TempDir()
	garbage := writeFile(t, filepath.Join(dir, "garbage"), bytes.Repeat([]byte("not an archive "), 100))
	empty := writeFile(t, filepath.Join(dir, "empty"), nil)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{garbage, dir}, garbage + ": not a tar or zip archive by its start: give its format, as tar: or zip: before it"},
		{[]string{empty, dir}, empty + ": empty, not an archive"},
		{[]string{"/dev/zero", dir}, "/dev/zero: not a directory, an archive or /dev/null"},
		{[]string{"-g", dir, dir}, "--git as imaginate"},
	} {
		cmd := binateWithin(t, 10*time.Second, tc.args...)
		cmd.Args[0] = archivesName
		cmd.Stdin = nil
		pt := ptytest.Open(t, 100, 30)
		pt.Start(cmd)
		code := pt.Exit()
		if out := pt.Output(); code != 2 || !strings.Contains(out, tc.want) {
			t.Errorf("%q: exit status %d, output %q, want %q", tc.args, code, out, tc.want)
		}
	}
}
