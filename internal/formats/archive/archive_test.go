// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package archive

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/stat"
)

// newRoot returns an empty root, following symlinks as follow says.
func newRoot(t *testing.T, follow func(string) bool) *Root {
	t.Helper()
	blob, err := NewBlob(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := New("a.zip", "zip", blob, follow)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func putFile(t *testing.T, r *Root, rel, data string) {
	t.Helper()
	e := &Entry{Stat: stat.Stat{Mode: unix.S_IFREG | 0o644, Size: int64(len(data))}}
	if err := r.Store(e, strings.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	r.Put(rel, e)
}

func symlink(r *Root, rel, target string) {
	r.Put(rel, &Entry{Stat: stat.Stat{Mode: unix.S_IFLNK | 0o777, Target: target, Size: int64(len(target))}})
}

func contents(t *testing.T, r *Root, rel string) string {
	t.Helper()
	f, err := r.Open(rel, false)
	if err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestFollow checks that a member beneath a symlink to a directory goes
// in the directory where the format follows it, as tar's, and else
// replaces the symlink with a directory, as unzip, which makes symlinks
// only once all else is extracted, would leave it.
func TestFollow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		follow func(string) bool
		inDir  bool
	}{
		{"tar", FollowedByTar, true},
		{"never", nil, false},
	} {
		r := newRoot(t, tc.follow)
		putFile(t, r, "d/f", "f")
		symlink(r, "alias", "d")
		putFile(t, r, "alias/g", "g")
		if err := r.Finish(); err != nil {
			t.Fatal(err)
		}
		names, _ := r.ReadDir("d")
		e, err := r.Entry("alias", false)
		if err != nil {
			t.Fatal(err)
		}
		if tc.inDir {
			if !slices.Equal(names, []string{"f", "g"}) || e.Mode&unix.S_IFMT != unix.S_IFLNK {
				t.Errorf("%s: d holds %q, alias %o", tc.name, names, e.Mode)
			}
			continue
		}
		if !slices.Equal(names, []string{"f"}) || e.Mode&unix.S_IFMT != unix.S_IFDIR || contents(t, r, "alias/g") != "g" {
			t.Errorf("%s: d holds %q, alias %o", tc.name, names, e.Mode)
		}
	}
	// A symlink to nothing, which symlink(2) can't make, isn't followed
	// to its own directory.
	r := newRoot(t, FollowedByTar)
	symlink(r, "empty", "")
	putFile(t, r, "empty/f", "f")
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if e, err := r.Entry("empty", false); err != nil || e.Mode&unix.S_IFMT != unix.S_IFDIR || contents(t, r, "empty/f") != "f" {
		t.Errorf("empty: %o, %v", e.Mode, err)
	}
}

// TestStore checks that files' contents follow one another in the blob,
// and that Finish counts links and lists directories.
func TestStore(t *testing.T) {
	r := newRoot(t, nil)
	putFile(t, r, "a", "first")
	putFile(t, r, "sub/b", "second")
	putFile(t, r, "a", "replaced")
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"a": "replaced", "sub/b": "second"} {
		if got := contents(t, r, rel); got != want {
			t.Errorf("%s holds %q, want %q", rel, got, want)
		}
	}
	if names, _ := r.ReadDir(""); !slices.Equal(names, []string{"a", "sub"}) {
		t.Errorf("root holds %q", names)
	}
	if st, _ := r.Stat(""); st.Nlink != 3 {
		t.Errorf("root has %d links", st.Nlink)
	}
	if r.Archive() != "zip" || r.Path("sub/b") != "a.zip:sub/b" {
		t.Errorf("format %q, path %q", r.Archive(), r.Path("sub/b"))
	}
}

// TestCountAt checks that each byte read is counted once, however often,
// and in whatever order, it is read.
func TestCountAt(t *testing.T) {
	var p Progress
	r := p.CountAt(strings.NewReader(strings.Repeat("x", 100)))
	for _, read := range []struct{ off, n, want int64 }{
		{90, 10, 10},
		{0, 10, 20},
		{5, 10, 25},
		{90, 10, 25},
		{30, 10, 35},
		{20, 10, 45},
		{50, 5, 50},
		{0, 100, 100},
		{0, 100, 100},
	} {
		if _, err := r.ReadAt(make([]byte, read.n), read.off); err != nil {
			t.Fatal(err)
		}
		if got := p.Read(); got != read.want {
			t.Errorf("after %d at %d: %d read, want %d", read.n, read.off, got, read.want)
		}
	}
	// What a read fails to read isn't counted.
	if _, err := r.ReadAt(make([]byte, 10), 95); err != io.EOF {
		t.Errorf("past the end: %v", err)
	}
	if got := p.Read(); got != 100 {
		t.Errorf("past the end: %d read", got)
	}
}

// TestKeepDirectories checks that a directory with entries stays where a
// later entry of another type has its name, as tar fails to replace it,
// and an empty one too if KeepDirectories was called, as unzip keeps it,
// and that a hard link not put there counts no link.
func TestKeepDirectories(t *testing.T) {
	for _, keep := range []bool{false, true} {
		r := newRoot(t, FollowedByTar)
		if keep {
			r.KeepDirectories()
		}
		dir := func() *Entry { return &Entry{Stat: stat.Stat{Mode: unix.S_IFDIR | 0o755}} }
		r.Put("full", dir())
		putFile(t, r, "full/f", "x")
		r.Put("empty", dir())
		putFile(t, r, "target", "t")
		file := &Entry{Stat: stat.Stat{Mode: unix.S_IFREG | 0o644}}
		if r.Puts("full", file) || r.Puts("empty", file) == keep || !r.Puts("full", dir()) || !r.Puts("new", file) {
			t.Errorf("keep %v: Puts full %v, empty %v, a directory at full %v, new %v", keep,
				r.Puts("full", file), r.Puts("empty", file), r.Puts("full", dir()), r.Puts("new", file))
		}
		putFile(t, r, "full", "file")
		symlink(r, "full", "elsewhere")
		putFile(t, r, "empty", "file")
		link := &Entry{}
		r.HardLink(link, "target")
		r.Put("full", link)
		if err := r.Finish(); err != nil {
			t.Fatal(err)
		}
		if e, err := r.Entry("full", false); err != nil || e.Mode&unix.S_IFMT != unix.S_IFDIR {
			t.Errorf("keep %v: full: %+v, %v", keep, e, err)
		}
		if got := contents(t, r, "full/f"); got != "x" {
			t.Errorf("keep %v: full/f holds %q", keep, got)
		}
		want := uint32(unix.S_IFREG)
		if keep {
			want = unix.S_IFDIR
		}
		if e, err := r.Entry("empty", false); err != nil || e.Mode&unix.S_IFMT != want {
			t.Errorf("keep %v: empty: %+v, %v", keep, e, err)
		}
		if st, err := r.Stat("target"); err != nil || st.Nlink != 1 {
			t.Errorf("keep %v: target: %+v, %v", keep, st, err)
		}
	}
}

// TestReaderAt checks that an archive in a regular file is read where it
// is, and one in anything else, as a pipe, from its copy at the start of
// the blob, whose space Finish frees, the files stored after it.
func TestReaderAt(t *testing.T) {
	data := strings.Repeat("x", 3*zeroBlock)
	path := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, tc := range []struct {
		name   string
		src    io.Reader
		copied bool
	}{
		{"file", f, false},
		{"pipe", strings.NewReader(data), true},
	} {
		r := newRoot(t, nil)
		var p Progress
		ra, size, err := r.ReaderAt(tc.src, &p)
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, size)
		if _, err := ra.ReadAt(b, 0); err != nil || string(b) != data {
			t.Fatalf("%s: read %d bytes, %v", tc.name, len(b), err)
		}
		if _, ok := ra.(*io.SectionReader); ok != tc.copied || p.Read() != size {
			t.Errorf("%s: copied %t, %d read", tc.name, ok, p.Read())
		}
		putFile(t, r, "f", "after")
		if err := r.Finish(); err != nil {
			t.Fatal(err)
		}
		if got := contents(t, r, "f"); got != "after" {
			t.Errorf("%s: f holds %q", tc.name, got)
		}
		start := make([]byte, 1)
		if _, err := r.blob.ReadAt(start, 0); tc.copied && err == nil && start[0] != 0 && canPunch(t, r.blob.Name()) {
			t.Errorf("%s: the copy's space isn't freed", tc.name)
		}
	}
}

// canPunch reports whether the file system of dir can punch holes, as
// Finish frees the space of an archive copied where it can.
func canPunch(t *testing.T, dir string) bool {
	t.Helper()
	f, err := os.CreateTemp(dir, "punch")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	return unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, 0, 1) == nil
}

// TestLink checks that an entry linked to another, however the format
// finds it, shares its contents and counts its links, and that a link
// to an entry that can't be read can't be either.
func TestLink(t *testing.T) {
	r := newRoot(t, nil)
	putFile(t, r, "a", "shared")
	a, _ := r.resolve("a")
	b := new(Entry)
	Link(b, a)
	r.Put("b", b)
	bad := &Entry{Stat: stat.Stat{Mode: unix.S_IFREG | 0o644}, Err: io.ErrUnexpectedEOF}
	r.Put("bad", bad)
	c := new(Entry)
	Link(c, bad)
	r.Put("c", c)
	// Nor can one to a directory, and one to an entry replaced since
	// counts only its own name.
	putFile(t, r, "dir/f", "f")
	dir, _ := r.resolve("dir")
	d := new(Entry)
	Link(d, dir)
	r.Put("d", d)
	putFile(t, r, "old", "old")
	old, _ := r.resolve("old")
	putFile(t, r, "old", "new")
	n := new(Entry)
	Link(n, old)
	r.Put("n", n)
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"a", "b"} {
		if st, _ := r.Stat(rel); st.Nlink != 2 || contents(t, r, rel) != "shared" {
			t.Errorf("%s has %d links", rel, st.Nlink)
		}
	}
	for _, rel := range []string{"c", "d"} {
		if _, err := r.Open(rel, false); err == nil {
			t.Errorf("%s opened", rel)
		}
	}
	for rel, want := range map[string]string{"n": "old", "old": "new"} {
		if st, _ := r.Stat(rel); st.Nlink != 1 || contents(t, r, rel) != want {
			t.Errorf("%s has %d links", rel, st.Nlink)
		}
	}
}

// TestOpenMode checks that an opened file has the mode Stat gives, with
// setuid, setgid and sticky, and the one git records opposite git.
func TestOpenMode(t *testing.T) {
	for _, git := range []bool{false, true} {
		r := newRoot(t, nil)
		if git {
			r.SetGitModes()
		}
		e := &Entry{Stat: stat.Stat{Mode: unix.S_IFREG | unix.S_ISUID | unix.S_ISVTX | 0o750, Size: 1}}
		if err := r.Store(e, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
		r.Put("f", e)
		if err := r.Finish(); err != nil {
			t.Fatal(err)
		}
		f, err := r.Open("f", false)
		if err != nil {
			t.Fatal(err)
		}
		info, _ := f.Stat()
		st, _ := r.Stat("f")
		want := fs.FileMode(st.Mode&0o777) | fs.ModeSetuid | fs.ModeSticky
		if git {
			want = 0o755
		}
		if info.Mode() != want {
			t.Errorf("git modes %t: %v, want %v", git, info.Mode(), want)
		}
	}
}

// TestMark checks that an entry marked as left out is shown with why
// unless an entry is there, and that any entry put later takes its place,
// as does a directory for one in it.
func TestMark(t *testing.T) {
	r := newRoot(t, nil)
	putFile(t, r, "kept", "kept")
	for _, rel := range []string{"kept", "file", "dir", "shown"} {
		r.Mark(rel, &Entry{Stat: stat.Stat{Mode: unix.S_IFDIR | 0o700}, Err: io.ErrUnexpectedEOF})
	}
	// Nor in place of a file in its way, and in place of a directory not
	// there, until one is.
	r.Mark("kept/in", &Entry{Stat: stat.Stat{Mode: unix.S_IFREG | 0o644}, Err: io.ErrUnexpectedEOF})
	r.Mark("new/in", &Entry{Stat: stat.Stat{Mode: unix.S_IFREG | 0o644}, Err: io.ErrUnexpectedEOF})
	r.Mark("later/in", &Entry{Stat: stat.Stat{Mode: unix.S_IFREG | 0o644}, Err: io.ErrUnexpectedEOF})
	putFile(t, r, "later/f", "later")
	// A directory holding only marks isn't kept for them, nor counts a
	// link from a directory marked in it.
	r.Put("empty", &Entry{Stat: stat.Stat{Mode: unix.S_IFDIR | 0o755}})
	r.Mark("empty/f", &Entry{Stat: stat.Stat{Mode: unix.S_IFREG | 0o644}, Err: io.ErrUnexpectedEOF})
	putFile(t, r, "empty", "now a file")
	// One with an entry in place of a mark in it stays.
	r.Put("mixed", &Entry{Stat: stat.Stat{Mode: unix.S_IFDIR | 0o755}})
	r.Mark("mixed/f", &Entry{Stat: stat.Stat{Mode: unix.S_IFREG | 0o644}, Err: io.ErrUnexpectedEOF})
	putFile(t, r, "mixed/f", "f")
	putFile(t, r, "mixed", "not put")
	r.Put("parent", &Entry{Stat: stat.Stat{Mode: unix.S_IFDIR | 0o755}})
	r.Mark("parent/gone/f", &Entry{Stat: stat.Stat{Mode: unix.S_IFREG | 0o644}, Err: io.ErrUnexpectedEOF})
	if !r.Puts("file", &Entry{Stat: stat.Stat{Mode: unix.S_IFREG | 0o644}}) {
		t.Error("a file isn't put over a mark")
	}
	putFile(t, r, "file", "file")
	putFile(t, r, "dir/f", "f")
	if err := r.Finish(); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.Stat("parent"); st.Nlink != 2 {
		t.Errorf("parent has %d links", st.Nlink)
	}
	for rel, want := range map[string]string{"kept": "kept", "file": "file", "dir/f": "f", "later/f": "later", "empty": "now a file", "mixed/f": "f"} {
		if got := contents(t, r, rel); got != want {
			t.Errorf("%s holds %q", rel, got)
		}
	}
	for _, rel := range []string{"shown", "new"} {
		if _, err := r.Entry(rel, false); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("%s: %v", rel, err)
		}
	}
	if names, _ := r.ReadDir("later"); !slices.Equal(names, []string{"f"}) {
		t.Errorf("later holds %q", names)
	}
}
