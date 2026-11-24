// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package tar

import (
	stdtar "archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/formats/archive"
	"pxy.se/go/binate/internal/source"
)

// tarOf returns a tar archive of hdrs, each regular file with the
// contents in data by its name.
func tarOf(t *testing.T, hdrs []stdtar.Header, data map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := stdtar.NewWriter(&buf)
	for _, h := range hdrs {
		if h.Typeflag == stdtar.TypeReg {
			h.Size = int64(len(data[h.Name]))
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == stdtar.TypeReg {
			if _, err := io.WriteString(tw, data[h.Name]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// read returns the root of the archive in data.
func read(t *testing.T, data []byte) *archive.Root {
	t.Helper()
	blob, err := archive.NewBlob(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := Read(bytes.NewReader(data), blob, "a.tar", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func contents(t *testing.T, r *archive.Root, rel string) string {
	t.Helper()
	f, err := r.Open(rel, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func names(t *testing.T, r *archive.Root, rel string) []string {
	t.Helper()
	got, err := r.ReadDir(rel)
	if err != nil {
		t.Fatal(err)
	}
	return slices.Sorted(slices.Values(got))
}

var mtime = time.Unix(1000000000, 0)

func TestRead(t *testing.T) {
	r := read(t, tarOf(t, []stdtar.Header{
		{Name: "./", Typeflag: stdtar.TypeDir, Mode: 0o750, Uid: 1, Gid: 2, ModTime: mtime},
		{Name: "./d/", Typeflag: stdtar.TypeDir, Mode: 0o700, ModTime: mtime},
		{Name: "./d/f", Typeflag: stdtar.TypeReg, Mode: 0o4644, Uid: 1000, Gid: 100, Uname: "me", Gname: "users", ModTime: mtime},
		{Name: "./d/g", Typeflag: stdtar.TypeReg, Mode: 0o600, ModTime: mtime},
		{Name: "./d/l", Typeflag: stdtar.TypeSymlink, Linkname: "f", Mode: 0o777},
		{Name: "./d/h", Typeflag: stdtar.TypeLink, Linkname: "./d/f", Mode: 0o644},
		{Name: "./dev/null", Typeflag: stdtar.TypeChar, Devmajor: 1, Devminor: 3, Mode: 0o666},
		{Name: "./p", Typeflag: stdtar.TypeFifo, Mode: 0o644},
	}, map[string]string{"./d/f": "first", "./d/g": "second"}))

	if got := names(t, r, ""); !slices.Equal(got, []string{"d", "dev", "p"}) {
		t.Errorf("root holds %q", got)
	}
	if got := names(t, r, "d"); !slices.Equal(got, []string{"f", "g", "h", "l"}) {
		t.Errorf("d holds %q", got)
	}
	if got := contents(t, r, "d/f"); got != "first" {
		t.Errorf("d/f holds %q", got)
	}
	if got := contents(t, r, "d/g"); got != "second" {
		t.Errorf("d/g holds %q", got)
	}
	// A hard link has its target's contents, and both count the link.
	if got := contents(t, r, "d/h"); got != "first" {
		t.Errorf("d/h holds %q", got)
	}
	for _, rel := range []string{"d/f", "d/h"} {
		st, err := r.Stat(rel)
		if err != nil {
			t.Fatal(err)
		}
		if st.Nlink != 2 || st.Size != 5 {
			t.Errorf("%s: %d links, size %d", rel, st.Nlink, st.Size)
		}
	}

	root, err := r.Entry("", false)
	if err != nil {
		t.Fatal(err)
	}
	if root.Mode != unix.S_IFDIR|0o750 || root.Uid != 1 || root.Gid != 2 || !root.Mtime.Equal(mtime) {
		t.Errorf("root %+v", root)
	}
	st, err := r.Stat("d/f")
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != unix.S_IFREG|0o4644 || st.Uid != 1000 || st.User != "me" || st.Group != "users" {
		t.Errorf("d/f %+v", st)
	}
	// The root holds d, two links, and dev, three.
	if st, _ := r.Stat(""); st.Nlink != 4 {
		t.Errorf("root has %d links", st.Nlink)
	}

	l, err := r.Entry("d/l", false)
	if err != nil || l.Mode != unix.S_IFLNK|0o777 || l.Target != "f" || l.Size != 1 {
		t.Errorf("d/l %+v, %v", l, err)
	}
	if _, err := r.Entry("d/l", true); !errors.Is(err, archive.ErrFollow) {
		t.Errorf("following d/l: %v", err)
	}
	if _, err := r.Open("d/l", false); !errors.Is(err, files.ErrNotRegular) {
		t.Errorf("opening d/l: %v", err)
	}
	dev, err := r.Entry("dev/null", false)
	if err != nil || dev.Mode != unix.S_IFCHR|0o666 || dev.Rdev != unix.Mkdev(1, 3) {
		t.Errorf("dev/null %+v, %v", dev, err)
	}
	if _, err := r.Open("dev/null", false); !errors.Is(err, files.ErrNotRegular) {
		t.Errorf("opening dev/null: %v", err)
	}
	if _, err := r.Entry("missing", false); !errors.Is(err, unix.ENOENT) {
		t.Errorf("missing: %v", err)
	}
	if _, err := r.ReadDir("d/f"); !errors.Is(err, unix.ENOTDIR) {
		t.Errorf("listing d/f: %v", err)
	}
	if got := r.Path("d/f"); got != "a.tar:d/f" {
		t.Errorf("path %q", got)
	}
	var _ source.Archive = r
}

// TestReadProgress checks that Read reports the archive read, as given,
// the entries and the last read.
func TestReadProgress(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(tarOf(t, []stdtar.Header{
		{Name: "d/", Typeflag: stdtar.TypeDir, Mode: 0o755},
		{Name: "d/f", Typeflag: stdtar.TypeReg, Mode: 0o644},
	}, map[string]string{"d/f": "data"})); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	data := gz.Bytes()
	blob, err := archive.NewBlob(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var p archive.Progress
	if p.Current() != "" {
		t.Errorf("current before reading %q", p.Current())
	}
	r, err := Read(bytes.NewReader(data), blob, "a.tar.gz", &p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if p.Read() != int64(len(data)) || p.Entries() != 2 || p.Current() != "a.tar.gz:d/f" {
		t.Errorf("read %d of %d, %d entries, current %q", p.Read(), len(data), p.Entries(), p.Current())
	}
}

// TestReadMtimePrecision checks that an mtime is known to be recorded
// only to the second unless a PAX record holds a fraction of it, which may
// be written from floating point: then it is compared to the microsecond.
func TestReadMtimePrecision(t *testing.T) {
	mtime := time.Unix(1700000000, 250000000)
	r := read(t, tarOf(t, []stdtar.Header{
		{Name: "ustar", Typeflag: stdtar.TypeReg, Mode: 0o644, ModTime: mtime},
		{Name: "gnu", Typeflag: stdtar.TypeReg, Mode: 0o644, ModTime: mtime, Format: stdtar.FormatGNU},
		{Name: "pax", Typeflag: stdtar.TypeReg, Mode: 0o644, ModTime: mtime, Format: stdtar.FormatPAX},
		// Too late for ustar's field, in a PAX record with no fraction.
		{Name: "pax, whole", Typeflag: stdtar.TypeReg, Mode: 0o644, ModTime: time.Unix(1<<34, 0), Format: stdtar.FormatPAX},
	}, nil))
	for _, tc := range []struct {
		rel       string
		mtime     time.Time
		precision time.Duration
	}{
		{"ustar", time.Unix(1700000000, 0), time.Second},
		{"gnu", time.Unix(1700000000, 0), time.Second},
		{"pax", mtime, time.Microsecond},
		{"pax, whole", time.Unix(1<<34, 0), time.Second},
	} {
		e, err := r.Entry(tc.rel, false)
		if err != nil {
			t.Fatal(err)
		}
		st, err := r.Stat(tc.rel)
		if err != nil {
			t.Fatal(err)
		}
		if !e.Mtime.Equal(tc.mtime) || e.MtimePrecision != tc.precision || st.MtimePrecision != tc.precision {
			t.Errorf("%s: mtime %v, to %v, stat's to %v, want %v, to %v",
				tc.rel, e.Mtime, e.MtimePrecision, st.MtimePrecision, tc.mtime, tc.precision)
		}
	}
}

// Entries are put as tar extracts them: in directories made where not
// listed, and a later entry in place of an earlier one.
func TestReadReplaces(t *testing.T) {
	r := read(t, tarOf(t, []stdtar.Header{
		{Name: "/a/b/f", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "x", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "x", Typeflag: stdtar.TypeReg, Mode: 0o600},
		{Name: "a/b", Typeflag: stdtar.TypeDir, Mode: 0o700},
		{Name: "y/z", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "y", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "w", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "w/v", Typeflag: stdtar.TypeReg, Mode: 0o644},
	}, map[string]string{"/a/b/f": "f", "x": "x2"}))

	if got := names(t, r, ""); !slices.Equal(got, []string{"a", "w", "x", "y"}) {
		t.Errorf("root holds %q", got)
	}
	// A directory listed after its entries keeps them, with its own mode.
	if got := names(t, r, "a/b"); !slices.Equal(got, []string{"f"}) {
		t.Errorf("a/b holds %q", got)
	}
	if e, _ := r.Entry("a/b", false); e.Mode != unix.S_IFDIR|0o700 || e.Implied {
		t.Errorf("a/b mode %o, implied %v", e.Mode, e.Implied)
	}
	if e, _ := r.Entry("a", false); e.Mode != unix.S_IFDIR|0o755 || !e.Implied {
		t.Errorf("a, implied, mode %o, implied %v", e.Mode, e.Implied)
	}
	if st, _ := r.Stat("a"); !st.Implied {
		t.Errorf("a's stat not implied")
	}
	if e, _ := r.Entry("x", false); e.Mode != unix.S_IFREG|0o600 {
		t.Errorf("x mode %o", e.Mode)
	}
	if got := contents(t, r, "x"); got != "x2" {
		t.Errorf("x holds %q", got)
	}
	// A directory with entries stays, as tar fails to replace it.
	if e, _ := r.Entry("y", false); e.Mode&unix.S_IFMT != unix.S_IFDIR {
		t.Errorf("y mode %o", e.Mode)
	}
	if _, err := r.Entry("y/z", false); err != nil {
		t.Errorf("y/z: %v", err)
	}
	if got := names(t, r, "w"); !slices.Equal(got, []string{"v"}) {
		t.Errorf("w holds %q", got)
	}
}

// TestReadLeftOut checks that the contents of a file left out, for a
// directory with entries that tar fails to replace, aren't stored.
func TestReadLeftOut(t *testing.T) {
	big := strings.Repeat("y", 1<<20)
	blob, err := archive.NewBlob(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := Read(bytes.NewReader(tarOf(t, []stdtar.Header{
		{Name: "a/", Typeflag: stdtar.TypeDir, Mode: 0o755},
		{Name: "a/b", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "a", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "after", Typeflag: stdtar.TypeReg, Mode: 0o644},
	}, map[string]string{"a/b": "x", "a": big, "after": "z"})), blob, "a.tar", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if info, err := blob.Stat(); err != nil || info.Size() != 2 {
		t.Errorf("blob of %v, %v, want 2 bytes", info.Size(), err)
	}
	if got := contents(t, r, "after"); got != "z" {
		t.Errorf("after holds %q", got)
	}
}

func TestReadHardLinkToNothing(t *testing.T) {
	r := read(t, tarOf(t, []stdtar.Header{
		{Name: "h", Typeflag: stdtar.TypeLink, Linkname: "gone", Mode: 0o644},
	}, nil))
	if _, err := r.Entry("h", false); err == nil || !strings.Contains(err.Error(), "gone, where the archive has no file before it") {
		t.Errorf("h: %v", err)
	}
	if got := names(t, r, ""); !slices.Equal(got, []string{"h"}) {
		t.Errorf("root holds %q", got)
	}
}

// TestReadHardLinks checks that a hard link is the entry at its target
// when it is read, as tar extracts it: of any type but a directory, also
// through another link, and kept once the target is replaced. Each name
// counts the link.
func TestReadHardLinks(t *testing.T) {
	data := tarOf(t, []stdtar.Header{
		{Name: "f", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "a", Typeflag: stdtar.TypeLink, Linkname: "f"},
		{Name: "b", Typeflag: stdtar.TypeLink, Linkname: "a"},
		{Name: "orig", Typeflag: stdtar.TypeReg, Mode: 0o600},
		{Name: "linked", Typeflag: stdtar.TypeLink, Linkname: "orig"},
		{Name: "s", Typeflag: stdtar.TypeSymlink, Linkname: "f", Mode: 0o777},
		{Name: "sl", Typeflag: stdtar.TypeLink, Linkname: "s"},
		{Name: "early", Typeflag: stdtar.TypeLink, Linkname: "late"},
		{Name: "late", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "d/", Typeflag: stdtar.TypeDir, Mode: 0o755},
		{Name: "dl", Typeflag: stdtar.TypeLink, Linkname: "d"},
	}, map[string]string{"f": "data", "orig": "first", "late": "x"})
	// Replaced after it is linked to, as a later entry of the same name.
	var buf bytes.Buffer
	tw := stdtar.NewWriter(&buf)
	if err := tw.WriteHeader(&stdtar.Header{Name: "orig", Typeflag: stdtar.TypeReg, Mode: 0o644, Size: 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("later")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	// Without the first archive's end, two zero blocks.
	data = append(data[:len(data)-1024:len(data)-1024], buf.Bytes()...)
	// Links resolved in an order of their own would differ between reads.
	for range 20 {
		r := read(t, data)
		for _, rel := range []string{"f", "a", "b"} {
			if got := contents(t, r, rel); got != "data" {
				t.Fatalf("%s holds %q", rel, got)
			}
			if st, err := r.Stat(rel); err != nil || st.Nlink != 3 || st.Mode != unix.S_IFREG|0o644 {
				t.Fatalf("%s: %+v, %v", rel, st, err)
			}
		}
		if got := contents(t, r, "linked"); got != "first" {
			t.Errorf("linked holds %q", got)
		}
		if got := contents(t, r, "orig"); got != "later" {
			t.Errorf("orig holds %q", got)
		}
		for _, rel := range []string{"linked", "orig"} {
			if st, _ := r.Stat(rel); st.Nlink != 1 {
				t.Errorf("%s has %d links", rel, st.Nlink)
			}
		}
		if e, err := r.Entry("sl", false); err != nil || e.Mode != unix.S_IFLNK|0o777 || e.Target != "f" {
			t.Errorf("sl: %+v, %v", e, err)
		}
		if st, _ := r.Stat("s"); st.Nlink != 2 {
			t.Errorf("s has %d links", st.Nlink)
		}
		for _, rel := range []string{"early", "dl"} {
			if _, err := r.Entry(rel, false); err == nil || !strings.Contains(err.Error(), "no file before it") {
				t.Errorf("%s: %v", rel, err)
			}
		}
	}
}

// TestReadHardLinkThroughSymlinks checks that a hard link's target is
// found through symlinks in its directories, as link(2) follows them, but
// not at its end, and nowhere through a loop, a file, or a symlink GNU
// tar makes only at the end: to an absolute path or through "..".
func TestReadHardLinkThroughSymlinks(t *testing.T) {
	link := func(name, target string) stdtar.Header {
		return stdtar.Header{Name: name, Typeflag: stdtar.TypeLink, Linkname: target}
	}
	symlink := func(name, target string) stdtar.Header {
		return stdtar.Header{Name: name, Typeflag: stdtar.TypeSymlink, Linkname: target, Mode: 0o777}
	}
	r := read(t, tarOf(t, []stdtar.Header{
		{Name: "d/", Typeflag: stdtar.TypeDir, Mode: 0o755},
		{Name: "d/f", Typeflag: stdtar.TypeReg, Mode: 0o644},
		symlink("alias", "d"),
		symlink("alias2", "alias/"),
		{Name: "sub/", Typeflag: stdtar.TypeDir, Mode: 0o755},
		symlink("sub/up", ".."),
		symlink("loop1", "loop2"),
		symlink("loop2", "loop1"),
		symlink("abs", "/etc"),
		symlink("out", "../.."),
		link("b", "alias/f"),
		link("c", "alias2/f"),
		link("u", "sub/up/d/f"),
		link("sl", "alias"),
		link("x", "loop1/f"),
		link("y", "abs/passwd"),
		link("z", "out/f"),
		link("w", "d/f/g"),
	}, map[string]string{"d/f": "data"}))
	for _, rel := range []string{"b", "c"} {
		if got := contents(t, r, rel); got != "data" {
			t.Errorf("%s holds %q", rel, got)
		}
	}
	if st, _ := r.Stat("d/f"); st.Nlink != 3 {
		t.Errorf("d/f has %d links", st.Nlink)
	}
	if e, err := r.Entry("sl", false); err != nil || e.Mode != unix.S_IFLNK|0o777 || e.Target != "d" {
		t.Errorf("sl: %+v, %v", e, err)
	}
	for _, rel := range []string{"u", "x", "y", "z", "w"} {
		if _, err := r.Entry(rel, false); err == nil || !strings.Contains(err.Error(), "no file before it") {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

// TestReadThroughSymlinks checks that an entry in a symlink to a
// directory goes in the directory, as tar extracts it, also through
// another symlink or one to ".", and the symlink stays. A symlink to an
// absolute path, to a file or to nothing is replaced by a directory
// instead, so that nothing is put outside the archive, nor in place of
// what it leads to.
func TestReadThroughSymlinks(t *testing.T) {
	r := read(t, tarOf(t, []stdtar.Header{
		{Name: "d/", Typeflag: stdtar.TypeDir, Mode: 0o755},
		{Name: "d/f", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "alias", Typeflag: stdtar.TypeSymlink, Linkname: "d", Mode: 0o777},
		{Name: "alias/g", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "alias/sub/", Typeflag: stdtar.TypeDir, Mode: 0o700},
		{Name: "abs", Typeflag: stdtar.TypeSymlink, Linkname: "/etc", Mode: 0o777},
		{Name: "abs/passwd", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "file", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "tofile", Typeflag: stdtar.TypeSymlink, Linkname: "file", Mode: 0o777},
		{Name: "tofile/f", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "dangling", Typeflag: stdtar.TypeSymlink, Linkname: "missing/dir", Mode: 0o777},
		{Name: "dangling/g", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "a1", Typeflag: stdtar.TypeSymlink, Linkname: "a2", Mode: 0o777},
		{Name: "a2", Typeflag: stdtar.TypeSymlink, Linkname: "d", Mode: 0o777},
		{Name: "a1/h", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "d/here", Typeflag: stdtar.TypeSymlink, Linkname: ".", Mode: 0o777},
		{Name: "d/here/i", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "f", Typeflag: stdtar.TypeReg, Mode: 0o644},
	}, map[string]string{"d/here/i": "i", "f": "root", "alias/g": "g", "abs/passwd": "p", "file": "keep", "tofile/f": "f", "dangling/g": "g", "a1/h": "h"}))
	if got := names(t, r, "d"); !slices.Equal(got, []string{"f", "g", "h", "here", "i", "sub"}) {
		t.Errorf("d holds %q", got)
	}
	if got := contents(t, r, "d/g"); got != "g" {
		t.Errorf("d/g holds %q", got)
	}
	if e, err := r.Entry("alias", false); err != nil || e.Mode&unix.S_IFMT != unix.S_IFLNK {
		t.Errorf("alias: %+v, %v", e, err)
	}
	if e, err := r.Entry("abs", false); err != nil || e.Mode&unix.S_IFMT != unix.S_IFDIR {
		t.Errorf("abs: %+v, %v", e, err)
	}
	if got := contents(t, r, "abs/passwd"); got != "p" {
		t.Errorf("abs/passwd holds %q", got)
	}
	if got := contents(t, r, "file"); got != "keep" {
		t.Errorf("file holds %q", got)
	}
	for rel, want := range map[string]string{"tofile/f": "f", "dangling/g": "g", "d/h": "h", "d/i": "i", "f": "root"} {
		if got := contents(t, r, rel); got != want {
			t.Errorf("%s holds %q", rel, got)
		}
	}
	for _, rel := range []string{"a1", "a2"} {
		if e, err := r.Entry(rel, false); err != nil || e.Mode&unix.S_IFMT != unix.S_IFLNK {
			t.Errorf("%s: %+v, %v", rel, e, err)
		}
	}
	if got := names(t, r, ""); !slices.Equal(got, []string{"a1", "a2", "abs", "alias", "d", "dangling", "f", "file", "tofile"}) {
		t.Errorf("root holds %q", got)
	}
}

// TestReadEmptySymlinks checks that a symlink to "", which symlink(2)
// refuses, is skipped, as tar makes nothing for it: what was there stays,
// and an entry in it goes in a directory.
func TestReadEmptySymlinks(t *testing.T) {
	r := read(t, tarOf(t, []stdtar.Header{
		{Name: "x", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "x", Typeflag: stdtar.TypeSymlink, Mode: 0o777},
		{Name: "alone", Typeflag: stdtar.TypeSymlink, Mode: 0o777},
		{Name: "d", Typeflag: stdtar.TypeSymlink, Mode: 0o777},
		{Name: "d/f", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "dd/", Typeflag: stdtar.TypeDir, Mode: 0o755},
		{Name: "dd/g", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "dd", Typeflag: stdtar.TypeSymlink, Mode: 0o777},
		{Name: "h", Typeflag: stdtar.TypeLink, Linkname: "alone", Mode: 0o644},
	}, map[string]string{"x": "kept", "d/f": "f", "dd/g": "g"}))
	if got := names(t, r, ""); !slices.Equal(got, []string{"d", "dd", "h", "x"}) {
		t.Errorf("root holds %q", got)
	}
	for rel, want := range map[string]string{"x": "kept", "d/f": "f", "dd/g": "g"} {
		if got := contents(t, r, rel); got != want {
			t.Errorf("%s holds %q", rel, got)
		}
	}
	for _, rel := range []string{"d", "dd"} {
		if e, err := r.Entry(rel, false); err != nil || e.Mode&unix.S_IFMT != unix.S_IFDIR {
			t.Errorf("%s: %+v, %v", rel, e, err)
		}
	}
	if _, err := r.Entry("h", false); err == nil || !strings.Contains(err.Error(), "no file before it") {
		t.Errorf("h: %v", err)
	}
}

// TestReadPlainLikeCompressed checks that a plain archive whose first
// entry's name starts as a compressed stream does is read as plain.
func TestReadPlainLikeCompressed(t *testing.T) {
	for _, name := range []string{"BZhello.txt", "]", "\x1f\x8bgz", "\xfd7zXZ"} {
		data := tarOf(t, []stdtar.Header{{Name: name, Typeflag: stdtar.TypeReg, Mode: 0o644, Format: stdtar.FormatGNU}},
			map[string]string{name: "x"})
		blob, err := archive.NewBlob(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		r, err := Read(bytes.NewReader(data), blob, "a.tar", nil)
		if err != nil {
			t.Errorf("%q: %v", name, err)
			continue
		}
		if got := contents(t, r, name); got != "x" {
			t.Errorf("%q holds %q", name, got)
		}
		_ = r.Close()
	}
}

// TestReadEmpty checks that nothing at all, compressed or not, is refused,
// as from a command that failed, but an archive of nothing is read.
func TestReadEmpty(t *testing.T) {
	var gz bytes.Buffer
	if err := gzip.NewWriter(&gz).Close(); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{nil, gz.Bytes()} {
		blob, err := archive.NewBlob(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Read(bytes.NewReader(data), blob, "a.tar", nil); err == nil || err.Error() != "empty, not a tar archive" {
			t.Errorf("%d bytes: %v", len(data), err)
		}
	}
	r := read(t, tarOf(t, nil, nil))
	if got := names(t, r, ""); len(got) != 0 {
		t.Errorf("an archive of nothing holds %q", got)
	}
}

// TestReadOutside checks that an entry with ".." in its name, which GNU
// tar refuses, is where tar names it, without all up to its last "..",
// as one that can't be read, unless an entry is there, and gives way to
// entries after it, and that a hard link through ".." links to its
// target without it, as GNU tar links it.
func TestReadOutside(t *testing.T) {
	// archive/tar gives these names with an error with the setting, which
	// GODEBUG may hold.
	for _, godebug := range []string{"", "tarinsecurepath=0"} {
		t.Run(godebug, func(t *testing.T) {
			t.Setenv("GODEBUG", godebug)
			testReadOutside(t)
		})
	}
}

func testReadOutside(t *testing.T) {
	r := read(t, tarOf(t, []stdtar.Header{
		{Name: "sub/../one", Typeflag: stdtar.TypeReg, Mode: 0o644, Size: 1},
		{Name: "f", Typeflag: stdtar.TypeReg, Mode: 0o644, Size: 1},
		{Name: "x/../f", Typeflag: stdtar.TypeReg, Mode: 0o644, Size: 1},
		{Name: "../d/", Typeflag: stdtar.TypeDir, Mode: 0o700},
		{Name: "d/g", Typeflag: stdtar.TypeReg, Mode: 0o644, Size: 1},
		{Name: "e/", Typeflag: stdtar.TypeDir, Mode: 0o755},
		{Name: "../e/", Typeflag: stdtar.TypeDir, Mode: 0o700},
		{Name: "h", Typeflag: stdtar.TypeLink, Linkname: "a/../f", Mode: 0o644},
	}, map[string]string{"sub/../one": "1", "f": "f", "x/../f": "x", "d/g": "g"}))
	if _, err := r.Entry("one", false); err == nil || !strings.Contains(err.Error(), `the name "sub/../one" has ".." in it, which tar refuses`) {
		t.Errorf("one: %v", err)
	}
	for rel, want := range map[string]string{"f": "f", "d/g": "g", "h": "f"} {
		if got := contents(t, r, rel); got != want {
			t.Errorf("%s holds %q, want %q", rel, got, want)
		}
	}
	if e, err := r.Entry("e", false); err != nil || e.Mode&0o777 != 0o755 {
		t.Errorf("e: %o, %v", e.Mode, err)
	}
	if got := names(t, r, ""); !slices.Equal(got, []string{"d", "e", "f", "h", "one"}) {
		t.Errorf("root holds %q", got)
	}
	// A rejected entry in a file's place doesn't make a directory of it,
	// before or after it, as tar leaves the file.
	for _, hdrs := range [][]stdtar.Header{
		{{Name: "d", Typeflag: stdtar.TypeReg, Mode: 0o644}, {Name: "x/../d/f", Typeflag: stdtar.TypeReg, Mode: 0o644}},
		{{Name: "x/../d/f", Typeflag: stdtar.TypeReg, Mode: 0o644}, {Name: "d", Typeflag: stdtar.TypeReg, Mode: 0o644}},
		// Nor does one keep a directory it would be in from being replaced,
		// as tar replaces it empty.
		{{Name: "d/", Typeflag: stdtar.TypeDir, Mode: 0o755}, {Name: "x/../d/f", Typeflag: stdtar.TypeReg, Mode: 0o644}, {Name: "d", Typeflag: stdtar.TypeReg, Mode: 0o644}},
	} {
		r := read(t, tarOf(t, hdrs, nil))
		if e, err := r.Entry("d", false); err != nil || e.Mode&unix.S_IFMT != unix.S_IFREG {
			t.Errorf("%s first: d %o, %v", hdrs[0].Name, e.Mode, err)
		}
	}
}

func TestReadOtherTypes(t *testing.T) {
	var buf bytes.Buffer
	tw := stdtar.NewWriter(&buf)
	for _, f := range []struct {
		name string
		typ  byte
	}{{"unknown", 'Z'}, {"names", 'N'}, {"label", typeVolume}, {"continued", typeMultiVolume}, {"solaris", typeSolarisExtended}, {"after", stdtar.TypeReg}} {
		data := f.name + "\n"
		if f.typ == typeSolarisExtended {
			data = "14 path=other\n"
		}
		hdr := &stdtar.Header{Name: f.name, Typeflag: f.typ, Mode: 0o644, Size: int64(len(data))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	r := read(t, buf.Bytes())
	// As GNU tar extracts them: an unknown type as a file, but no volume
	// label, nor what is continued from another volume, which it refuses.
	if got := names(t, r, ""); !slices.Equal(got, []string{"after", "continued", "names", "unknown"}) {
		t.Errorf("root holds %q", got)
	}
	for _, rel := range []string{"unknown", "names", "after"} {
		if got := contents(t, r, rel); got != rel+"\n" {
			t.Errorf("%s holds %q", rel, got)
		}
		if e, err := r.Entry(rel, false); err != nil || e.Mode != unix.S_IFREG|0o644 {
			t.Errorf("%s: %o, %v", rel, e.Mode, err)
		}
	}
	if _, err := r.Entry("continued", false); err == nil || !strings.Contains(err.Error(), "continued from another volume") {
		t.Errorf("continued: %v", err)
	}
}

func TestReadRefuses(t *testing.T) {
	blob := func() *os.File {
		f, err := archive.NewBlob(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	full := tarOf(t, []stdtar.Header{{Name: "f", Typeflag: stdtar.TypeReg}}, map[string]string{"f": strings.Repeat("x", 2000)})
	named := tarOf(t, []stdtar.Header{{Name: "dir/file", Typeflag: stdtar.TypeReg}}, map[string]string{"dir/file": "x"})
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"garbage", bytes.Repeat([]byte("not a tar "), 100), "not a tar archive"},
		{"truncated", full[:1024], "ends early"},
		{"lzop", []byte("\x89LZO\x00\r\n\x1a\n"), "compressed with lzop"},
		{"old gzip", []byte("\x1f\x9e\x08\x00"), "compressed with gzip's old format"},
		{"only skippable frames", skippableFrame(0x50, 4), "followed by neither"},
		{"skippable frame, then garbage", append(skippableFrame(0x50, 4), bytes.Repeat([]byte("not a tar "), 100)...), "followed by neither"},
		{"truncated skippable frame", skippableFrame(0x50, 600)[:300], "ends early"},
		// A dictionary of neither 2ⁿ nor 2ⁿ + 2ⁿ⁻¹ bytes, which lzma never has.
		{"not lzma", append([]byte("\x5d\x00\x00\x01\x01\xff\xff\xff\xff\xff\xff\xff\xff"), bytes.Repeat([]byte("not a tar "), 100)...), "not a tar archive"},
		// Only "BZh" and a block size, which doesn't make bzip2.
		{"BZh", bytes.Repeat([]byte("BZh9 not bzip2 "), 100), "not a tar archive"},
		// Shorter than a header, or not one, named for what it is.
		{"short", []byte("hello\n"), "not a tar archive, shorter than a header"},
		{"short gzip", gzipOf(t, []byte("hello\n")), "compressed with gzip, but not a tar archive"},
		{"gzip", gzipOf(t, bytes.Repeat([]byte("not a tar "), 100)), "compressed with gzip, but not a tar archive"},
		// Cut short in its first header, which ustar's magic tells. A
		// name of one letter would start an lzma header, as xz takes it.
		{"truncated header", named[:300], "ends early"},
		{"truncated header, gzip", gzipOf(t, named[:300]), "ends early"},
	} {
		_, err := Read(bytes.NewReader(tc.data), blob(), "a.tar", nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}
}

// toolStreams are tar -c x, of x/f holding "hello\n", compressed by each
// tool.
var toolStreams = map[string]string{
	"xz -9":                     "fd377a585a000004e6d6b44604c074805021011c00000000000000001d450031e027ff006c5d003c0bbc1ba78c3444e1c20775e8fce3c092652fc74a68ff6cb21b9a37d4d8c7c66fafdf88277b5ed1a424656c0dd2a626cb6725f2f238b939e44b64ab861d15d5331d72c743dc4c2603062f287610aa2d0081c32f0784271757a96ba63a367a417c58c7ad40f237e1d4b844a4007681436bc49796e80001900180500000a573b662b1c467fb020000000004595a",
	"lzma -9":                   "5d00000004ffffffffffffffff003c0bbc1ba78c3444e1c20775e8fce3c092652fc74a68ff6cb21b9a37d4d8c7c66fafdf88277b5ed1a424656c0dd2a626cb6725f2f238b939e44b64ab861d15d5331d72c743dc4c2603062f287610aa2d0081c32f0784271757a96ba63a367a417c58c7ad40f237e1d53f35e0ffffd5d78840",
	"lzma -9, dict 4 KiB":       "5d00100000ffffffffffffffff003c0bbc1ba78c3444e1c20775e8fce3c092652fc74a68ff6cb21b9a37d4d8c7c66fafdf88277b5ed1a424656c0dd2a626cb6725f2f238b939e44b64ab861d15d5331d72c743dc4c2603062f287610aa2d0081c32f0784271757a96ba63a367a417c58c7ad40f237e1d53f35e0ffffd5d78840",
	"lzma -9, lc 0":             "5a00000004ffffffffffffffff003c0bb92c700b0bf9dc760d6d3708fc17110433412fbf3624c2383b76f25d2c94eee7d794e2213f3c7ef89c3a43b7210fc2153b08b464d269bb1156525327b93b64c6dd4f7c1b43308f2a2a9ad6ee3db23426209ffe92d68175475f40d1b78928121cb67d9d14e7af64d5acffffe448ed60",
	"lzma -9, lc 1, lp 2, pb 0": "1300000004ffffffffffffffff003c0c1a1eb3e161659c9389138e11d280300f5c8093810098d74c73abb40ee8130716ad36f408d017868eb0c671b35c851172d843bf5c2d64d9257bddd7a4ed4c6d10f663cf766a3495b7de41ad04382c739a82d8d0699e3f9e73613fff156186e4bb9894c24461e68c23ccffdb7cc340",
	"zstd -19":                  "28b52ffd640027750200e2c20a11a0edb0e1197e0090b426b56101aac23105066c2da4f2ce418a6a4921fa6fe73968efad2b4a7ad7a4430c0c20502fb03bf6036fc0a805b90d8718201df429fe63baa380070c1a603e4137540e8bd7",
	"lz4 -9":                    "04224d186440a7840000003f782f0001004e9230303030373535003001000c08000f0c00008f36313534002035000100508f75737461722020000100e43f782f660002523f3634340002081c3600026f3332300020300002ff517f68656c6c6f0a000100ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff05500000000000000000007b64d551",
	"lz4 -l -9":                 "02214c18840000003f782f0001004e9230303030373535003001000c08000f0c00008f36313534002035000100508f75737461722020000100e43f782f660002523f3634340002081c3600026f3332300020300002ff517f68656c6c6f0a000100ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff05500000000000",
}

// skippableFrame returns a skippable frame of zstd and lz4, of the magic
// starting with first, holding n bytes.
func skippableFrame(first byte, n int) []byte {
	b := []byte{first, 0x2a, 0x4d, 0x18, byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
	return append(b, bytes.Repeat([]byte{0xaa}, n)...)
}

func TestReadCompressed(t *testing.T) {
	plain := tarOf(t, []stdtar.Header{{Name: "x/f", Typeflag: stdtar.TypeReg, Mode: 0o644}}, map[string]string{"x/f": "hello\n"})
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	// tar -c x | bzip2 -9, of x/f holding "hello\n".
	bz, err := hex.DecodeString("425a68393141592653598a7551e2000095fb80c99000104000df80000363449e400808200074129353d40d001a034f50494434da86800680fb66b2f714a403412422ec26631b418941e44421c29bd7a68c0941010b0801bc980a08185f190aa58488ab0c53536f696d628a84cf24fecc98e38e3d770881f8bb9229c2848453aa8f10")
	if err != nil {
		t.Fatal(err)
	}
	streams := map[string][]byte{"plain": plain, "gzip": gz.Bytes(), "bzip2": bz}
	for name, h := range toolStreams {
		data, err := hex.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		streams[name] = data
		// zstd and lz4 after skippable frames, one larger than what is
		// peeked at.
		if strings.HasPrefix(name, "zstd") || strings.HasPrefix(name, "lz4 -9") {
			streams[name+", skippable frames"] = slices.Concat(skippableFrame(0x50, 3), skippableFrame(0x5f, 100<<10), data)
		}
	}
	for name, data := range streams {
		r := read(t, data)
		if got := contents(t, r, "x/f"); got != "hello\n" {
			t.Errorf("%s: x/f holds %q", name, got)
		}
	}
}

// Blocks of zeros are left as holes in the blob, which take no space, and
// read as zeros, at the end of the blob too.
func TestReadZeros(t *testing.T) {
	big := strings.Repeat("\x00", 8<<20)
	mixed := "start" + big + "middle" + big
	blob, err := archive.NewBlob(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r, err := Read(bytes.NewReader(tarOf(t, []stdtar.Header{
		{Name: "mixed", Typeflag: stdtar.TypeReg},
		{Name: "after", Typeflag: stdtar.TypeReg},
		{Name: "zeros", Typeflag: stdtar.TypeReg},
	}, map[string]string{"mixed": mixed, "after": "x", "zeros": big})), blob, "a.tar", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	for rel, want := range map[string]string{"mixed": mixed, "after": "x", "zeros": big} {
		if got := contents(t, r, rel); got != want {
			t.Errorf("%s: %d bytes read, want %d", rel, len(got), len(want))
		}
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(blob.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	if st.Size != int64(3*len(big)+len("startmiddlex")) || st.Blocks*512 > 1<<20 {
		t.Errorf("blob of %d bytes takes %d", st.Size, st.Blocks*512)
	}
}

// The blob has no name, so nothing is left in the directory.
func TestNewBlob(t *testing.T) {
	dir := t.TempDir()
	f, err := archive.NewBlob(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(dir)
	if err != nil || len(ents) != 0 {
		t.Errorf("directory holds %v, %v", ents, err)
	}
	if _, err := archive.NewBlob(dir + "/missing"); err == nil {
		t.Error("no error for a missing directory")
	}
}

// FuzzRead checks that any stream is read or refused without a panic, and
// that every entry listed can then be read.
func FuzzRead(f *testing.F) {
	f.Add([]byte{})
	f.Add(tarOf(&testing.T{}, []stdtar.Header{
		{Name: "d/f", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "d/h", Typeflag: stdtar.TypeLink, Linkname: "d/f"},
		{Name: "l", Typeflag: stdtar.TypeSymlink, Linkname: "d"},
	}, map[string]string{"d/f": "x"}))
	for _, h := range toolStreams {
		data, err := hex.DecodeString(h)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		blob, err := archive.NewBlob(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = blob.Close() }()
		r, err := Read(bytes.NewReader(data), blob, "a.tar", nil)
		if err != nil {
			return
		}
		var walk func(rel string)
		walk = func(rel string) {
			if _, err := r.Stat(rel); err != nil {
				return
			}
			if f, err := r.Open(rel, false); err == nil {
				_, _ = io.Copy(io.Discard, f)
				_ = f.Close()
			}
			names, _ := r.ReadDir(rel)
			for _, name := range names {
				if rel != "" {
					name = rel + "/" + name
				}
				walk(name)
			}
		}
		walk("")
	})
}

// gzipOf returns b compressed by gzip, as a member of its own.
func gzipOf(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestReadTrailing checks what follows a compressed stream: another gzip
// member, read as gzip reads it, zeros, which a tape's blocks are padded
// with and gzip ignores, and after bzip2 anything, which bzip2 ignores,
// but not anything else after gzip, which makes gzip fail.
func TestReadTrailing(t *testing.T) {
	plain := tarOf(t, []stdtar.Header{{Name: "x/f", Typeflag: stdtar.TypeReg, Mode: 0o644}}, map[string]string{"x/f": "hello\n"})
	bz, err := hex.DecodeString("425a68393141592653598a7551e2000095fb80c99000104000df80000363449e400808200074129353d40d001a034f50494434da86800680fb66b2f714a403412422ec26631b418941e44421c29bd7a68c0941010b0801bc980a08185f190aa58488ab0c53536f696d628a84cf24fecc98e38e3d770881f8bb9229c2848453aa8f10")
	if err != nil {
		t.Fatal(err)
	}
	zeros := make([]byte, 10<<10)
	members := slices.Concat(gzipOf(t, plain[:700]), gzipOf(t, plain[700:]))
	for name, data := range map[string][]byte{
		"gzip, zeros":            slices.Concat(gzipOf(t, plain), zeros),
		"gzip members":           members,
		"gzip members, zeros":    slices.Concat(members, zeros),
		"gzip, an empty member":  slices.Concat(gzipOf(t, plain), gzipOf(t, nil)),
		"bzip2, zeros":           slices.Concat(bz, zeros),
		"bzip2, a zero":          slices.Concat(bz, []byte{0}),
		"bzip2, Z":               slices.Concat(bz, []byte("Z")),
		"bzip2, garbage":         slices.Concat(bz, []byte("garbage!")),
		"bzip2 streams, garbage": slices.Concat(bz, bz, []byte("garbage!")),
		"bzip2, BZ!":             slices.Concat(bz, []byte("BZ! not bzip2")),
		"bzip2, BZh0":            slices.Concat(bz, []byte("BZh0 not bzip2")),
	} {
		r := read(t, data)
		if got := contents(t, r, "x/f"); got != "hello\n" {
			t.Errorf("%s: x/f holds %q", name, got)
		}
	}
	// A stream cut short, after the tar archive's end, a stream after the
	// last that starts as one but isn't, or is cut short, and a damaged
	// checksum, which bzip2 fails on too.
	damaged := slices.Clone(bz)
	damaged[len(damaged)-2] ^= 1
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"gzip, garbage", slices.Concat(gzipOf(t, plain), zeros[:100], []byte("garbage!")), "data after the gzip stream"},
		{"bzip2, cut short", bz[:len(bz)-1], "ends early"},
		// As the start of another stream, cut short.
		{"bzip2, B", slices.Concat(bz, []byte("B")), "ends early"},
		{"bzip2, BZ", slices.Concat(bz, []byte("BZ")), "ends early"},
		{"bzip2, BZh", slices.Concat(bz, []byte("BZh")), "ends early"},
		{"bzip2, BZh9 and garbage", slices.Concat(bz, []byte("BZh9 not bzip2")), "bzip2 data invalid"},
		{"bzip2, damaged checksum", damaged, "checksum mismatch"},
	} {
		blob, err := archive.NewBlob(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Read(bytes.NewReader(tc.data), blob, "a.tar", nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
		_ = blob.Close()
	}
	// An error reading, given once, at the start and after a skippable
	// frame, isn't lost.
	for name, data := range map[string][]byte{"start": nil, "skippable frame": skippableFrame(0x50, 4)} {
		blob, err := archive.NewBlob(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		in := io.MultiReader(bytes.NewReader(data), &errOnce{err: unix.EIO})
		if _, err := Read(in, blob, "a.tar", nil); !errors.Is(err, unix.EIO) {
			t.Errorf("EIO at the %s: %v", name, err)
		}
		_ = blob.Close()
	}
	// An error reading after a complete stream, and after a byte of
	// padding, isn't taken for its end, given on every read or once. The
	// gzip stream is larger than what is buffered, for the error to come
	// at its end, and not when its start is peeked at.
	noise := make([]byte, 200<<10)
	_, _ = rand.NewChaCha8([32]byte{}).Read(noise)
	big := tarOf(t, []stdtar.Header{{Name: "f", Typeflag: stdtar.TypeReg, Mode: 0o644}}, map[string]string{"f": string(noise)})
	for name, data := range map[string][]byte{"plain": plain, "gzip": gzipOf(t, plain), "large gzip": gzipOf(t, big), "bzip2": bz} {
		for _, extra := range [][]byte{nil, {0}} {
			for _, once := range []bool{false, true} {
				blob, err := archive.NewBlob(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				fail := iotest.ErrReader(unix.EIO)
				if once {
					fail = &errOnce{err: unix.EIO}
				}
				in := io.MultiReader(bytes.NewReader(slices.Concat(data, extra)), fail)
				if _, err := Read(in, blob, "a.tar", nil); !errors.Is(err, unix.EIO) {
					t.Errorf("%s, %d bytes, then EIO, once %v: %v", name, len(extra), once, err)
				}
				_ = blob.Close()
			}
		}
	}
}

// errOnce fails the first read with err, and then ends.
type errOnce struct{ err error }

func (e *errOnce) Read([]byte) (int, error) {
	err := e.err
	e.err = nil
	if err == nil {
		return 0, io.EOF
	}
	return 0, err
}

// TestReadGlobalPAX checks that the records of a global PAX header apply
// to the entries after it, as GNU tar extracts them, unless their own
// records override them, until a later one removes them.
func TestReadGlobalPAX(t *testing.T) {
	global := func(records map[string]string) stdtar.Header {
		return stdtar.Header{Name: "pax_global_header", Typeflag: stdtar.TypeXGlobalHeader, PAXRecords: records}
	}
	r := read(t, tarOf(t, []stdtar.Header{
		{Name: "before", Typeflag: stdtar.TypeReg, Mode: 0o644, Uid: 1, Gid: 2, ModTime: mtime},
		global(map[string]string{"uid": "1234", "gid": "2345", "uname": "u", "gname": "g", "mtime": "1700000000.25"}),
		{Name: "after", Typeflag: stdtar.TypeReg, Mode: 0o644, Uid: 1, Gid: 2, ModTime: mtime},
		// Its uid too large for ustar's field, and its mtime's fraction,
		// held in its own records.
		{Name: "own", Typeflag: stdtar.TypeReg, Mode: 0o644, Uid: 1 << 22, Gid: 2,
			ModTime: time.Unix(1600000000, 500000000), Format: stdtar.FormatPAX},
		global(map[string]string{"uid": "", "mtime": ""}),
		{Name: "removed", Typeflag: stdtar.TypeReg, Mode: 0o644, Uid: 1, Gid: 2, ModTime: mtime},
	}, nil))
	for _, tc := range []struct {
		rel       string
		uid, gid  uint32
		user      string
		mtime     time.Time
		precision time.Duration
	}{
		{"before", 1, 2, "", mtime, time.Second},
		{"after", 1234, 2345, "u", time.Unix(1700000000, 250000000), time.Microsecond},
		{"own", 1 << 22, 2345, "u", time.Unix(1600000000, 500000000), time.Microsecond},
		{"removed", 1, 2345, "u", mtime, time.Second},
	} {
		st, err := r.Stat(tc.rel)
		if err != nil {
			t.Fatal(err)
		}
		if st.Uid != tc.uid || st.Gid != tc.gid || st.User != tc.user || !st.Mtime.Equal(tc.mtime) || st.MtimePrecision != tc.precision {
			t.Errorf("%s: %d:%d %q, %v to %v; want %d:%d %q, %v to %v", tc.rel, st.Uid, st.Gid, st.User, st.Mtime, st.MtimePrecision,
				tc.uid, tc.gid, tc.user, tc.mtime, tc.precision)
		}
	}
	if got := names(t, r, ""); !slices.Equal(got, []string{"after", "before", "own", "removed"}) {
		t.Errorf("root holds %q", got)
	}

}

func TestPAXTime(t *testing.T) {
	for _, tc := range []struct {
		v    string
		want time.Time
		ok   bool
	}{
		{"1700000000", time.Unix(1700000000, 0), true},
		{"1700000000.5", time.Unix(1700000000, 500000000), true},
		{"1700000000.1234567891", time.Unix(1700000000, 123456789), true},
		{"-1.5", time.Unix(-1, -500000000), true},
		{"-0.25", time.Unix(0, -250000000), true},
		{"", time.Time{}, false},
		{"1.", time.Unix(1, 0), true},
		{"+1", time.Unix(1, 0), true},
		{"1.x", time.Time{}, false},
		{"1.-5", time.Time{}, false},
		{"1e3", time.Time{}, false},
	} {
		got, err := paxTime(tc.v)
		if (err == nil) != tc.ok || !got.Equal(tc.want) {
			t.Errorf("%q: %v, %v", tc.v, got, err)
		}
	}
}

// A hard link named as the root, which stays a directory, counts no link
// to its target.
func TestReadHardLinkAsRoot(t *testing.T) {
	r := read(t, tarOf(t, []stdtar.Header{
		{Name: "a", Typeflag: stdtar.TypeReg, Mode: 0o644},
		{Name: "./", Typeflag: stdtar.TypeLink, Linkname: "a"},
	}, map[string]string{"a": "x"}))
	if st, err := r.Stat("a"); err != nil || st.Nlink != 1 {
		t.Errorf("a: %+v, %v", st, err)
	}
	if e, err := r.Entry("", false); err != nil || e.Mode&unix.S_IFMT != unix.S_IFDIR {
		t.Errorf("root: %+v, %v", e, err)
	}
}
