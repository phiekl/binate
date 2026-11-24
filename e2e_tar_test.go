// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/ptytest"
)

// tarOf returns a tar archive of the tree at dir, as tar -C dir -c .
// makes it, with each header changed by edit if set.
func tarOf(t *testing.T, dir string, edit func(*tar.Header)) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		hdr.Name = "./" + filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if edit != nil {
			edit(hdr)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			_, err = tw.Write(b)
			return err
		}
		return nil
	})
	if err == nil {
		err = tw.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func gzipped(t *testing.T, data []byte) []byte {
	t.Helper()
	return compressed(t, data, func(w io.Writer) (io.WriteCloser, error) { return gzip.NewWriter(w), nil })
}

// compressed returns data compressed by the writer newWriter makes.
func compressed(t *testing.T, data []byte, newWriter func(io.Writer) (io.WriteCloser, error)) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := newWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeFile(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestTarArchives checks that a tar archive, compressed or not, is
// compared as the tree it holds, with a directory or another archive.
func TestTarArchives(t *testing.T) {
	dir := writeFiles(t, map[string]string{"same": "x\n", "changed": "one\n", "sub/f": "f\n"})
	other := writeFiles(t, map[string]string{"same": "x\n", "changed": "two\n", "sub/f": "f\n", "new": "n\n"})
	tmp := t.TempDir()
	plain := writeFile(t, filepath.Join(tmp, "dir.tar"), tarOf(t, dir, nil))
	gz := writeFile(t, filepath.Join(tmp, "other.tar.gz"), gzipped(t, tarOf(t, other, nil)))
	xzed := writeFile(t, filepath.Join(tmp, "other.tar.xz"), compressed(t, tarOf(t, other, nil),
		func(w io.Writer) (io.WriteCloser, error) { return xz.NewWriter(w) }))
	lzmaed := writeFile(t, filepath.Join(tmp, "other.tar.lzma"), compressed(t, tarOf(t, other, nil),
		func(w io.Writer) (io.WriteCloser, error) { return lzma.NewWriter(w) }))
	zst := writeFile(t, filepath.Join(tmp, "other.tar.zst"), compressed(t, tarOf(t, other, nil),
		func(w io.Writer) (io.WriteCloser, error) { return zstd.NewWriter(w) }))
	lz := writeFile(t, filepath.Join(tmp, "other.tar.lz4"), compressed(t, tarOf(t, other, nil),
		func(w io.Writer) (io.WriteCloser, error) { return lz4.NewWriter(w), nil }))
	for _, tc := range []struct {
		name        string
		left, right string
		want        string
	}{
		{"same as its tree", "tar:" + plain, dir, "[3 identical]"},
		{"with a directory", "tar:" + gz, dir, "[2 identical] [1 left] [1 contents]"},
		{"with an archive", "tar:" + plain, "tar:" + gz, "[2 identical] [1 right] [1 contents]"},
		// Read by the restricted child, as zstd's decoder in goroutines.
		{"xz", "tar:" + xzed, dir, "[2 identical] [1 left] [1 contents]"},
		{"lzma", "tar:" + lzmaed, dir, "[2 identical] [1 left] [1 contents]"},
		{"zstd", "tar:" + zst, dir, "[2 identical] [1 left] [1 contents]"},
		{"lz4", "tar:" + lz, dir, "[2 identical] [1 left] [1 contents]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pt, offset := startInTerminal(t, tc.left, tc.right)
			pt.WaitScreen(100, 30, offset, tc.want)
			quit(t, pt, offset, "x")
			if code := pt.Exit(); code != 0 {
				t.Errorf("exit status %d", code)
			}
		})
	}
}

// TestTarMtimes checks that mtimes in an archive, recorded only to the
// second, are compared to the second, with those of its tree on disk.
func TestTarMtimes(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a": "x\n", "b": "y\n"})
	for i, name := range []string{"a", "b"} {
		mtime := time.Unix(1700000000+int64(i), 750000000)
		if err := os.Chtimes(filepath.Join(dir, name), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	// Truncated, as tar truncates it, where Go's writer rounds it.
	archive := writeFile(t, filepath.Join(t.TempDir(), "a.tar"), tarOf(t, dir, func(h *tar.Header) {
		h.ModTime = h.ModTime.Truncate(time.Second)
	}))
	if err := os.Chtimes(filepath.Join(dir, "b"), time.Unix(1700000002, 0), time.Unix(1700000002, 0)); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--compare=+mtime", "--quick"} {
		t.Run(flag, func(t *testing.T) {
			pt, offset := startInTerminal(t, flag, "tar:"+archive, dir)
			if flag == "--quick" {
				pt.WaitScreen(100, 30, offset, "[1 identical] [1 unverified]")
			} else {
				pt.WaitScreen(100, 30, offset, "[1 identical] [1 mtime]")
			}
			quit(t, pt, offset, "x")
			if code := pt.Exit(); code != 0 {
				t.Errorf("exit status %d", code)
			}
		})
	}
}

// TestTarPipe checks that an archive is read from a pipe, as from a
// process substitution.
func TestTarPipe(t *testing.T) {
	dir := writeFiles(t, map[string]string{"same": "x\n", "changed": "one\n"})
	other := writeFiles(t, map[string]string{"same": "x\n", "changed": "two\n"})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	data := gzipped(t, tarOf(t, other, nil))
	go func() {
		_, _ = w.Write(data)
		_ = w.Close()
	}()
	pt := ptytest.Open(t, 100, 30)
	cmd := binate(t, dir, "tar:/dev/fd/3")
	cmd.ExtraFiles = []*os.File{r}
	pt.Start(cmd)
	_ = r.Close()
	offset := pt.Wait(0, enterScreen)
	pt.WaitScreen(100, 30, offset, "[1 identical] [1 contents]")
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

// TestTarPanels checks that an archive's owners are compared, and shown
// by the names it records, that its files can't be edited, and that the
// archive, once read, may change without changing the comparison.
func TestTarPanels(t *testing.T) {
	dir := writeFiles(t, map[string]string{"changed": "one\n", "owned": "x\n"})
	other := writeFiles(t, map[string]string{"changed": "two\n", "owned": "x\n"})
	archive := writeFile(t, filepath.Join(t.TempDir(), "a.tar"), tarOf(t, other, func(h *tar.Header) {
		if h.Name == "./owned" {
			h.Uid, h.Uname, h.Gname = 54321, "someone", "elsewhere"
		}
	}))
	e := newFakeEditor(t)
	pt, offset := e.start("tar:"+archive, dir)
	screen := func(want string) string { return pt.WaitScreen(100, 30, offset, want) }
	screen("[1 contents] [1 attrs]")
	if err := os.WriteFile(archive, []byte("changed since"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The owner as the archive names it, and no inode.
	pt.Write("*jj")
	scr := screen("someone:elsewhere (54321:")
	for _, line := range strings.Split(scr, "\n") {
		if left := []rune(line); strings.Contains(string(left[:min(len(left), 48)]), "inode") {
			t.Errorf("an archive's inode shown:\n%s", scr)
		}
	}
	pt.Write("k")
	screen("a.tar:changed")
	e.do(`write:two\n`)
	pt.Write("e")
	if scr := screen("The left side is in an archive."); strings.Contains(scr, "h ←") || !strings.Contains(scr, "l →") {
		t.Errorf("popup for the right side:\n%s", scr)
	}
	pt.Write("h\r")
	screen("[1 identical] [1 attrs]")
	if got := e.log(); len(got) != 1 || filepath.Base(got[0]) != "changed" {
		t.Errorf("editor given %q", got)
	}
	pt.Write("j")
	screen("[1 identical] [1 attrs]")
	// Rescanned, from what was read of the archive.
	pt.Write("r")
	screen("[1 identical] [1 attrs]")
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

// failing runs cmd, failing, in a new pseudo-terminal, and returns its
// exit status and output. With popup, it fails reading the left side:
// the error is shown over the empty boxes, a key closes it, and binate
// then writes it too.
func failing(t *testing.T, cmd *exec.Cmd, popup bool) (int, string) {
	t.Helper()
	cmd.Stdin = nil
	pt := ptytest.Open(t, 100, 30)
	pt.Start(cmd)
	if popup {
		offset := pt.Wait(0, enterScreen)
		scr := pt.WaitScreen(100, 30, offset, "Unable to read the left side")
		if strings.Contains(scr, "entries") {
			t.Errorf("the loading box under the error:\n%s", scr)
		}
		pt.Write("x")
	}
	code := pt.Exit()
	return code, pt.Output()
}

func TestTarErrors(t *testing.T) {
	dir := t.TempDir()
	garbage := writeFile(t, filepath.Join(dir, "garbage.tar"), bytes.Repeat([]byte("not a tar "), 200))
	lzo := writeFile(t, filepath.Join(dir, "a.tar.lzo"), []byte("\x89LZO\x00\r\n\x1a\n and more"))
	empty := writeFile(t, filepath.Join(dir, "empty.tar"), nil)
	good := writeFile(t, filepath.Join(dir, "good.tar"), tarOf(t, writeFiles(t, map[string]string{"f": "x"}), nil))
	gzipped := writeFile(t, filepath.Join(dir, "text.gz"), gzipped(t, []byte("hello\n")))
	for _, tc := range []struct {
		args []string
		env  string
		want string
		// popup is whether it fails reading, on the screen.
		popup bool
	}{
		{[]string{"tar:" + dir, dir}, "", dir + ": a directory: give it without the tar: prefix", false},
		{[]string{"tar:/dev/zero", dir}, "", "/dev/zero: not a file or a pipe", false},
		{[]string{"tar:" + dir + "/missing", dir}, "", "no such file", false},
		{[]string{"tar:", dir}, "", "no archive after tar:", false},
		{[]string{"tar:" + garbage, dir}, "", garbage + ": not a tar archive", true},
		{[]string{"tar:" + lzo, dir}, "", "compressed with lzop", true},
		{[]string{"tar:" + gzipped, dir}, "", gzipped + ": compressed with gzip, but not a tar archive", true},
		{[]string{"tar:" + empty, dir}, "", empty + ": empty, not a tar archive", true},
		{[]string{"tar:" + good, dir}, "TMPDIR=" + dir + "/missing", dir + "/missing: no such file", false},
		{[]string{"-g", "tar:" + good, "HEAD"}, "", "", false},
	} {
		cmd := binateWithin(t, 10*time.Second, tc.args...)
		if tc.env != "" {
			cmd.Env = append(cmd.Env, tc.env)
		}
		code, out := failing(t, cmd, tc.popup)
		if tc.want == "" {
			// With -g, tar: starts a revision's path.
			if code == 0 || strings.Contains(out, "tar archive") {
				t.Errorf("%q: exit status %d, output %q", tc.args, code, out)
			}
			continue
		}
		if code != 2 || !strings.Contains(out, tc.want) {
			t.Errorf("%q: exit status %d, output %q, want %q", tc.args, code, out, tc.want)
		}
	}
}

// TestTarReading checks that binate shows how far it has read an
// archive, as from a pipe that never ends, and can be quit meanwhile.
func TestTarReading(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	pt := ptytest.Open(t, 100, 30)
	cmd := binate(t, "tar:/dev/fd/3", t.TempDir())
	cmd.ExtraFiles = []*os.File{r}
	pt.Start(cmd)
	_ = r.Close()
	// A file of which only the start is written.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "big", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(make([]byte, 1536)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	offset := pt.Wait(0, enterScreen)
	for _, want := range []string{"/dev/fd/3:big", "1 entry", "2.0 KiB read"} {
		pt.WaitScreen(100, 30, offset, want)
	}
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
	checkCooked(t, pt)
}

// TestTarFifoWaiting checks that a FIFO without a writer is waited on
// as it is read, on the loading screen, not taken for an empty archive,
// and read once a writer comes.
func TestTarFifoWaiting(t *testing.T) {
	dir := writeFiles(t, map[string]string{"f": "x\n"})
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	pt := ptytest.Open(t, 100, 30)
	pt.Start(binate(t, "tar:"+fifo, dir))
	offset := pt.Wait(0, enterScreen)
	pt.WaitScreen(100, 30, offset, "0 entries")
	time.Sleep(200 * time.Millisecond)
	if scr := pt.Screen(100, 30, offset); !strings.Contains(scr, "0 entries") {
		t.Fatalf("no longer waiting:\n%s", scr)
	}
	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(tarOf(t, dir, nil))
	_ = w.Close()
	pt.WaitScreen(100, 30, offset, "[1 identical]")
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

// TestTarGitModes checks that an archive compared with a revision has the
// modes git records, as a directory has, whatever modes it holds.
func TestTarGitModes(t *testing.T) {
	repo := gitRepo(t)
	dir := writeFiles(t, map[string]string{"dir/changed": "one\nTWO\n", "same": "x\n", "new": "n\n"})
	modes := map[string]int64{"./same": 0o600, "./new": 0o664, "./dir/": 0o775}
	archive := writeFile(t, filepath.Join(t.TempDir(), "a.tar"), tarOf(t, dir, func(h *tar.Header) {
		if m, ok := modes[h.Name]; ok {
			h.Mode = m
		}
	}))
	pt, offset := startInTerminal(t, "git:"+repo+"@HEAD~", "tar:"+archive)
	scr := pt.WaitScreen(100, 30, offset, "[1 identical] [1 left] [1 right] [1 contents]")
	if strings.Contains(scr, "attrs]") {
		t.Errorf("modes compared as the archive holds them:\n%s", scr)
	}
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

// TestTarPipesAtOnce checks that two pipes are read at once, as one
// program writes them, all of the right before the left, more than a pipe
// holds, or all of the left before it opens the right, and that one
// failing ends reading the other at once, whose writer writes nothing yet,
// as one slow to.
func TestTarPipesAtOnce(t *testing.T) {
	big := strings.Repeat("0123456789abcdef", 1<<17)
	dir := writeFiles(t, map[string]string{"same": "x\n", "changed": "one\n", "big": big})
	other := writeFiles(t, map[string]string{"same": "x\n", "changed": "two\n", "big": big})
	garbage := bytes.Repeat([]byte("not a tar "), 1<<17)
	for _, tc := range []struct {
		name  string
		data  [2][]byte
		order [2]int
		// late opens each FIFO only to write it.
		late bool
		want string
	}{
		{"right first", [2][]byte{tarOf(t, dir, nil), tarOf(t, other, nil)}, [2]int{1, 0}, false, ""},
		{"left before opening the right", [2][]byte{tarOf(t, dir, nil), tarOf(t, other, nil)}, [2]int{0, 1}, true, ""},
		{"left fails", [2][]byte{garbage, nil}, [2]int{0, -1}, false, "garbage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			fifos := [2]string{filepath.Join(tmp, "left"), filepath.Join(tmp, "right")}
			for _, fifo := range fifos {
				if err := unix.Mkfifo(fifo, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// A writer left open, writing nothing, is closed once the test
			// ends.
			var ws [2]*os.File
			t.Cleanup(func() {
				for _, w := range ws {
					if w != nil {
						_ = w.Close()
					}
				}
			})
			go func() {
				if tc.late {
					for _, i := range tc.order {
						w, err := os.OpenFile(fifos[i], os.O_WRONLY, 0)
						if err != nil {
							return
						}
						_, _ = w.Write(tc.data[i])
						_ = w.Close()
					}
					return
				}
				for i, fifo := range fifos {
					w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
					if err != nil {
						return
					}
					ws[i] = w
				}
				for _, i := range tc.order {
					if i >= 0 {
						_, _ = ws[i].Write(tc.data[i])
						_ = ws[i].Close()
					}
				}
			}()
			cmd := binateWithin(t, 10*time.Second, "tar:"+fifos[0], "tar:"+fifos[1])
			if tc.want == "" {
				pt := ptytest.Open(t, 100, 30)
				pt.Start(cmd)
				offset := pt.Wait(0, enterScreen)
				pt.WaitScreen(100, 30, offset, "[2 identical] [1 contents]")
				quit(t, pt, offset, "x")
				if code := pt.Exit(); code != 0 {
					t.Errorf("exit status %d", code)
				}
				return
			}
			code, out := failing(t, cmd, true)
			if want := fifos[0] + ": not a tar archive"; code != 2 || !strings.Contains(out, want) {
				t.Errorf("exit status %d, output %q, want %q", code, out, want)
			}
		})
	}
}
