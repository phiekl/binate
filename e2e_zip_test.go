// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"pxy.se/go/binate/internal/ptytest"
)

// zipOf returns a zip archive of dir, as Info-ZIP's zip makes one, with
// its modes, owners and times, unless edit, called on each header, says
// otherwise.
func zipOf(t *testing.T, dir string, edit func(*zip.FileHeader)) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		hdr.Method = zip.Deflate
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			hdr.Extra = unixOwner(st.Uid, st.Gid)
		}
		if edit != nil {
			edit(hdr)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			_, err = w.Write(b)
			return err
		}
		return nil
	})
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// unixOwner returns Info-ZIP's new Unix extra field of uid and gid.
func unixOwner(uid, gid uint32) []byte {
	b := []byte{0x75, 0x78, 11, 0, 1, 4, 0, 0, 0, 0, 4, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(b[6:], uid)
	binary.LittleEndian.PutUint32(b[11:], gid)
	return b
}

// TestZipArchives checks zip archives compared with the tree they were
// made of, with a directory and with a tar archive, and read from a pipe.
func TestZipArchives(t *testing.T) {
	dir := writeFiles(t, map[string]string{"same": "x\n", "changed": "one\n", "sub/f": "f\n"})
	other := writeFiles(t, map[string]string{"same": "x\n", "changed": "two\n", "sub/f": "f\n", "new": "n\n"})
	tmp := t.TempDir()
	own := writeFile(t, filepath.Join(tmp, "dir.zip"), zipOf(t, dir, nil))
	changed := writeFile(t, filepath.Join(tmp, "other.zip"), zipOf(t, other, nil))
	tarred := writeFile(t, filepath.Join(tmp, "dir.tar"), tarOf(t, dir, nil))
	for _, tc := range []struct {
		name        string
		left, right string
		want        string
	}{
		{"same as its tree", "zip:" + own, dir, "[3 identical]"},
		{"with a directory", "zip:" + changed, dir, "[2 identical] [1 left] [1 contents]"},
		{"with a tar archive", "zip:" + changed, "tar:" + tarred, "[2 identical] [1 left] [1 contents]"},
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

	t.Run("pipe", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		data := zipOf(t, other, nil)
		go func() {
			_, _ = w.Write(data)
			_ = w.Close()
		}()
		pt := ptytest.Open(t, 100, 30)
		cmd := binate(t, dir, "zip:/dev/fd/3")
		cmd.ExtraFiles = []*os.File{r}
		pt.Start(cmd)
		_ = r.Close()
		offset := pt.Wait(0, enterScreen)
		pt.WaitScreen(100, 30, offset, "[2 identical] [1 right] [1 contents]")
		quit(t, pt, offset, "x")
		if code := pt.Exit(); code != 0 {
			t.Errorf("exit status %d", code)
		}
	})
}

// TestZipNotUnix checks an archive made elsewhere than on Unix, as on
// Windows, with neither modes nor owners, and DOS times alone, in local
// time to two seconds: modes and owners are neither compared nor shown,
// and an mtime a second off is the same.
func TestZipNotUnix(t *testing.T) {
	dir := writeFiles(t, map[string]string{"f": "x\n"})
	mtime := time.Date(2025, 6, 1, 12, 0, 1, 500000000, time.Local)
	if err := os.Chtimes(filepath.Join(dir, "f"), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "f"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := writeFile(t, filepath.Join(t.TempDir(), "win.zip"), zipOf(t, dir, func(h *zip.FileHeader) {
		// Made on FAT, its DOS time rounded up to 12:00:02, and no extra
		// fields, as of owners or times.
		h.CreatorVersion, h.ExternalAttrs, h.Extra = 0, 0, nil
		h.Modified = time.Time{}
		//lint:ignore SA1019 only the DOS fields write a DOS time alone
		h.ModifiedDate = uint16((2025-1980)<<9 | 6<<5 | 1)
		//lint:ignore SA1019 as above
		h.ModifiedTime = uint16(12<<11 | 0<<5 | 1)
	}))
	pt, offset := startInTerminal(t, "-c", "+mtime", "zip:"+archive, dir)
	pt.WaitScreen(100, 30, offset, "[1 identical]")
	// Identical, f is shown with *.
	pt.Write("*")
	scr := pt.WaitScreen(100, 30, offset, "owner │ —")
	if !strings.Contains(scr, "mode │ —") || strings.Contains(scr, "(newer)") {
		t.Errorf("panels:\n%s", scr)
	}
	quit(t, pt, offset, "x")
	if code := pt.Exit(); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

// TestZipErrors checks the errors of zip: arguments and archives.
func TestZipErrors(t *testing.T) {
	dir := t.TempDir()
	garbage := writeFile(t, filepath.Join(dir, "garbage.zip"), bytes.Repeat([]byte("not a zip "), 200))
	empty := writeFile(t, filepath.Join(dir, "empty.zip"), nil)
	for _, tc := range []struct {
		args []string
		want string
		// popup is whether it fails reading, on the screen.
		popup bool
	}{
		{[]string{"zip:" + dir, dir}, dir + ": a directory: give it without the zip: prefix", false},
		{[]string{"zip:/dev/zero", dir}, "/dev/zero: not a file or a pipe, which zip: reads", false},
		{[]string{"zip:", dir}, "no archive after zip:, as zip:old.zip", false},
		{[]string{"zip:" + garbage, dir}, garbage + ": not a zip archive", true},
		{[]string{"zip:" + empty, dir}, empty + ": empty, not a zip archive", true},
		{[]string{"rar:" + empty, dir}, "unknown backend \"rar\", not git, tar or zip", false},
	} {
		code, out := failing(t, binateWithin(t, 10*time.Second, tc.args...), tc.popup)
		if code != 2 || !strings.Contains(out, tc.want) {
			t.Errorf("%q: exit status %d, output %q, want %q", tc.args, code, out, tc.want)
		}
	}
}
