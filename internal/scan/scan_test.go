// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// tree creates files from a map of path to contents. A trailing "/" makes
// a directory, "->target" a symlink, and "|fifo" a FIFO.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, contents := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		var err error
		switch {
		case strings.HasSuffix(p, "/"):
			err = os.MkdirAll(full, 0o755)
		case strings.HasPrefix(contents, "->"):
			err = os.Symlink(contents[2:], full)
		case contents == "|fifo":
			err = unix.Mkfifo(full, 0o644)
		default:
			err = os.WriteFile(full, []byte(contents), 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func scanWait(t *testing.T, left, right string, opts Options) *Scan {
	t.Helper()
	s := Start(left, right, opts, nil)
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the scan did not finish")
	}
	return s
}

// marks returns each node's path and mark, in tree order.
func marks(n *Node) string {
	var b strings.Builder
	var walk func(*Node)
	walk = func(n *Node) {
		for _, c := range n.Children {
			b.WriteString(string(c.Mark()) + " " + c.Path() + "\n")
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func TestStates(t *testing.T) {
	left := tree(t, map[string]string{
		"same":            "abc",
		"content":         "abc",
		"size":            "abc",
		"left-only":       "x",
		"type":            "x",
		"link-same":       "->target",
		"link-differ":     "->a",
		"link-vs-file":    "->a",
		"dir/same":        "x",
		"dir/differ":      "x",
		"onesided/a":      "x",
		"onesided/b/c":    "x",
		"empty/":          "",
		"zz-dir-vs-file/": "",
	})
	right := tree(t, map[string]string{
		"same":           "abc",
		"content":        "abd",
		"size":           "abcd",
		"right-only":     "x",
		"type/":          "",
		"link-same":      "->target",
		"link-differ":    "->b",
		"link-vs-file":   "a",
		"dir/same":       "x",
		"dir/differ":     "y",
		"empty/":         "",
		"zz-dir-vs-file": "x",
	})
	s := scanWait(t, left, right, Options{})
	want := `= dir
| dir/differ
= dir/same
= empty
< onesided
< onesided/b
< onesided/b/c
< onesided/a
! type
! zz-dir-vs-file
| content
< left-only
| link-differ
= link-same
! link-vs-file
> right-only
= same
| size
`
	if got := marks(s.Root); got != want {
		t.Errorf("marks:\n%s\nwant:\n%s", got, want)
	}
	if s.Root.State != Differ {
		t.Errorf("root state %c, want |", s.Root.State.Mark())
	}
	if n, _ := s.Progress(); n != 18 {
		t.Errorf("scanned %d entries, want 18", n)
	}
	if p := s.Current(); p != "" {
		t.Errorf("current path %q once done, want none", p)
	}
	if p := s.current.Load(); p == nil || s.Root.Find(*p) == nil && *p != "" {
		t.Errorf("last path read %v is not in the tree", p)
	}
	d := s.Root.Find("dir")
	if d.L.Count != 2 || d.R.Count != 2 {
		t.Errorf("dir counts %d, %d", d.L.Count, d.R.Count)
	}
}

func TestIdenticalTrees(t *testing.T) {
	files := map[string]string{"a/b/c": "x", "d": "y", "e/": ""}
	s := scanWait(t, tree(t, files), tree(t, files), Options{})
	if s.Root.State != Same {
		t.Errorf("root state %c, want =\n%s", s.Root.State.Mark(), marks(s.Root))
	}
}

func TestOrder(t *testing.T) {
	files := map[string]string{"b": "", "B": "", "a/": "", "Z/": "", "é": "", "_": ""}
	s := scanWait(t, tree(t, files), tree(t, files), Options{})
	var names []string
	for _, c := range s.Root.Children {
		names = append(names, c.Name)
	}
	if got, want := strings.Join(names, " "), "Z a B _ b é"; got != want {
		t.Errorf("order %q, want %q", got, want)
	}
}

func TestExcludes(t *testing.T) {
	left := tree(t, map[string]string{"keep": "x", "skip.o": "x", ".git/config": "x", "sub/skip.o": "x"})
	right := tree(t, map[string]string{"keep": "x", "sub/": ""})
	s := scanWait(t, left, right, Options{Excludes: []string{"*.o", ".git"}})
	want := "= sub\n= keep\n"
	if got := marks(s.Root); got != want {
		t.Errorf("marks:\n%s\nwant:\n%s", got, want)
	}
}

func TestQuickMode(t *testing.T) {
	left := tree(t, map[string]string{"same": "abc", "touched": "abc", "size": "abc", "content": "abc"})
	right := tree(t, map[string]string{"same": "abc", "touched": "abc", "size": "abcd", "content": "abd"})
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range []string{"same", "content"} {
		for _, root := range []string{left, right} {
			if err := os.Chtimes(filepath.Join(root, name), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Chtimes(filepath.Join(left, "touched"), old, old); err != nil {
		t.Fatal(err)
	}
	s := scanWait(t, left, right, Options{Quick: true})
	// Same size and time pass in quick mode, even with other contents.
	want := "= content\n= same\n| size\n? touched\n"
	if got := marks(s.Root); got != want {
		t.Errorf("marks:\n%s\nwant:\n%s", got, want)
	}
}

func TestLargeFiles(t *testing.T) {
	a := strings.Repeat("x", 3*blockSize+17)
	b := a[:2*blockSize] + "y" + a[2*blockSize+1:]
	left := tree(t, map[string]string{"same": a, "late": a, "exact": a[:blockSize]})
	right := tree(t, map[string]string{"same": a, "late": b, "exact": a[:blockSize]})
	s := scanWait(t, left, right, Options{})
	want := "= exact\n| late\n= same\n"
	if got := marks(s.Root); got != want {
		t.Errorf("marks:\n%s\nwant:\n%s", got, want)
	}
}

func TestCompareContentsReuse(t *testing.T) {
	large := strings.Repeat("x", 3*blockSize+17)
	var comparer contentComparer
	for _, tc := range []struct {
		name        string
		left, right string
		want        State
	}{
		{"large", large, large, Same},
		{"late", large, large[:2*blockSize] + "y" + large[2*blockSize+1:], Differ},
		{"short", "x", "x", Same},
		{"length", "x", "xy", Differ},
		{"empty", "", "", Same},
		{"exact", large[:blockSize], large[:blockSize], Same},
		{"first", "y" + large[1:], large, Differ},
		{"short-again", "x", "x", Same},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left := tree(t, map[string]string{"file": tc.left})
			right := tree(t, map[string]string{"file": tc.right})
			state, lErr, rErr := comparer.compareContents(context.Background(),
				filepath.Join(left, "file"), filepath.Join(right, "file"))
			if state != tc.want || lErr != nil || rErr != nil {
				t.Fatalf("comparison: %v, %v, %v; want %v", state, lErr, rErr, tc.want)
			}
		})
	}
}

func TestUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	left := tree(t, map[string]string{"file": "x", "dir/a": "x", "dir/sub/b": "x", "other": "x"})
	right := tree(t, map[string]string{"file": "x", "dir/a": "x", "dir/sub/b": "x", "other": "xy"})
	for _, p := range []string{"file", "dir"} {
		if err := os.Chmod(filepath.Join(right, p), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(right, p), 0o755) })
	}
	// The modes differ, a difference known without reading: leave them out.
	s := scanWait(t, left, right, Options{Compare: DefaultAttrs &^ Mode})
	file := s.Root.Find("file")
	if file.State != Unverified || file.R.Err == nil || !strings.Contains(file.R.Err.Error(), "permission denied") {
		t.Errorf("file: state %c, right error %v", file.State.Mark(), file.R.Err)
	}
	dir := s.Root.Find("dir")
	if dir.R.Err == nil || dir.State != Unverified || dir.R.Count != 0 {
		t.Errorf("dir: state %c, right error %v, right count %d", dir.State.Mark(), dir.R.Err, dir.R.Count)
	}
	// The unreadable side's entries may be there or not: they are unknown,
	// not missing, down to the bottom.
	for _, p := range []string{"dir/a", "dir/sub", "dir/sub/b"} {
		n := s.Root.Find(p)
		if n == nil || n.State != Unverified || n.R.Kind != Unknown || n.R.Err == nil ||
			!strings.Contains(n.R.Err.Error(), "failed listing its directory (permission denied)") {
			t.Errorf("%s = %+v", p, n)
		}
	}
	if marks := marks(s.Root); strings.Contains(marks, "<") {
		t.Errorf("one-sided entries:\n%s", marks)
	}
	// A size difference needs no reading.
	if o := s.Root.Find("other"); o.State != Differ {
		t.Errorf("other: state %c", o.State.Mark())
	}
	// Compared, the modes differ, unreadable or not.
	s = scanWait(t, left, right, Options{})
	if got := marks(s.Root); !strings.HasPrefix(got, "~ dir\n") || !strings.Contains(got, "~ file\n") {
		t.Errorf("with modes compared:\n%s", got)
	}
}

func TestUnreadableDirectoryIsUnverified(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	left := tree(t, map[string]string{"dir/": ""})
	right := tree(t, map[string]string{"dir/": ""})
	if err := os.Chmod(filepath.Join(right, "dir"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(right, "dir"), 0o755) })
	s := scanWait(t, left, right, Options{Compare: DefaultAttrs &^ Mode})
	if d := s.Root.Find("dir"); d.State != Unverified {
		t.Errorf("dir: state %c, want ?", d.State.Mark())
	}
}

func TestSpecialFilesAreNotOpened(t *testing.T) {
	left := tree(t, map[string]string{"fifo": "|fifo", "fifo-vs-file": "|fifo", "file-vs-fifo": "x"})
	right := tree(t, map[string]string{"fifo": "|fifo", "fifo-vs-file": "x", "file-vs-fifo": "|fifo"})
	// A FIFO opened for reading would block forever: the scan must finish.
	s := scanWait(t, left, right, Options{})
	want := "= fifo\n! fifo-vs-file\n! file-vs-fifo\n"
	if got := marks(s.Root); got != want {
		t.Errorf("marks:\n%s\nwant:\n%s", got, want)
	}
}

// Data keeps what comparing a pair's data found apart from its
// attributes, which State doesn't tell for a pair whose attributes differ.
func TestData(t *testing.T) {
	file := func(perm uint32, mtime int64) Side {
		return Side{Kind: File, Size: 4, Perm: perm, Mtime: time.Unix(mtime, 0)}
	}
	for _, tt := range []struct {
		name        string
		l, r        Side
		opts        Options
		state, data State
	}{
		// Quick mode can't tell equal sizes with different mtimes apart.
		{"quick, mode and mtime", file(0o644, 1), file(0o600, 2), Options{Compare: DefaultAttrs, Quick: true}, Differ, Unverified},
		{"quick, mtime", file(0o644, 1), file(0o644, 2), Options{Compare: DefaultAttrs, Quick: true}, Unverified, Unverified},
		{"quick, mode", file(0o644, 1), file(0o600, 1), Options{Compare: DefaultAttrs, Quick: true}, Differ, Same},
		{"to compare", file(0o644, 1), file(0o600, 2), Options{Compare: DefaultAttrs}, Pending, Pending},
		{"one side", file(0o644, 1), Side{Kind: Missing}, Options{Compare: DefaultAttrs}, LeftOnly, Pending},
	} {
		n := &Node{L: tt.l, R: tt.r}
		classify(n, tt.opts)
		if n.State != tt.state || n.Data != tt.data {
			t.Errorf("%s: state %c, data %c, want %c, %c", tt.name, n.State.Mark(), n.Data.Mark(), tt.state.Mark(), tt.data.Mark())
		}
	}
}

func TestDevices(t *testing.T) {
	// Devices are compared by number, and never opened.
	n := &Node{L: statSide("/dev/null", false), R: statSide("/dev/zero", false)}
	classify(n, Options{Compare: DefaultAttrs})
	if n.L.Kind != CharDev || n.State != Differ {
		t.Errorf("/dev/null vs /dev/zero: kind %v, state %c", n.L.Kind, n.State.Mark())
	}
	n = &Node{L: statSide("/dev/null", false), R: statSide("/dev/null", false)}
	classify(n, Options{Compare: DefaultAttrs})
	if n.State != Same {
		t.Errorf("/dev/null vs itself: state %c", n.State.Mark())
	}
}

func TestRecheck(t *testing.T) {
	left := tree(t, map[string]string{"dir/file": "abc", "dir/other": "x", "gone": "x"})
	right := tree(t, map[string]string{"dir/file": "abd", "dir/other": "x", "gone": "x"})
	s := scanWait(t, left, right, Options{})
	if s.Root.State != Differ {
		t.Fatalf("root state %c", s.Root.State.Mark())
	}

	// Edited to match: the entry and its parents become the same.
	if err := os.WriteFile(filepath.Join(right, "dir", "file"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.Recheck("dir/file")
	if got := marks(s.Root); got != "= dir\n= dir/file\n= dir/other\n= gone\n" || s.Root.State != Same {
		t.Errorf("after the edit:\n%s", got)
	}

	// Replaced by rename, as editors save.
	tmp := filepath.Join(left, "dir", ".file.swp")
	if err := os.WriteFile(tmp, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(left, "dir", "file")); err != nil {
		t.Fatal(err)
	}
	s.Recheck("dir/file")
	if f := s.Root.Find("dir/file"); f.State != Differ || s.Root.State != Differ {
		t.Errorf("after the rename: %c", f.State.Mark())
	}

	// Deleted on one side: one-sided.
	if err := os.Remove(filepath.Join(left, "dir", "file")); err != nil {
		t.Fatal(err)
	}
	s.Recheck("dir/file")
	if f := s.Root.Find("dir/file"); f.State != RightOnly || s.Root.Find("dir").L.Count != 1 {
		t.Errorf("after deleting: %c", f.State.Mark())
	}

	// Deleted on both sides: gone.
	for _, root := range []string{left, right} {
		if err := os.Remove(filepath.Join(root, "gone")); err != nil {
			t.Fatal(err)
		}
	}
	s.Recheck("gone")
	if s.Root.Find("gone") != nil {
		t.Errorf("an entry missing on both sides was kept")
	}
}

func TestCancel(t *testing.T) {
	files := map[string]string{}
	for i := range 200 {
		files[filepath.Join("d", string(rune('a'+i%26)), strings.Repeat("f", 1+i/26))] = "x"
	}
	s := Start(tree(t, files), tree(t, files), Options{}, nil)
	s.Cancel()
	if !s.Done() {
		t.Error("not done after Cancel")
	}
	// The tree may be read once cancelled.
	s.Lock()
	_ = marks(s.Root)
	s.Unlock()
}

func TestParseAttrs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Attrs
	}{
		{"content", Content},
		{"mode,owner", Mode | Owner},
		{"mtime,mtime", Mtime},
		{"+mtime", DefaultAttrs | Mtime},
		{"+mode", DefaultAttrs},
		{"-owner", DefaultAttrs &^ Owner},
		{"+mtime,-owner,-mode", Left | Right | Type | Content | Target | Device | Mtime},
		{"-left,-right", Type | Content | Target | Device | Mode | Owner},
		{"-mtime", DefaultAttrs},
		{"-left,-right,-type,-content,-target,-device,-mode,-owner", None},
		{"-left,-right,-type,-content,-target,-device,-mode,-owner,+mode", Mode},
		{"none", None},
		{None.String(), None},
	} {
		got, err := ParseAttrs(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseAttrs(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{"", "+", "-", "size", "-size", "mode,", "mode,+mtime", "+mtime,owner",
		"++mtime", "+-mtime", " mode", "+mtime, -owner", "none,mode", "-none"} {
		if _, err := ParseAttrs(in); err == nil {
			t.Errorf("ParseAttrs(%q): no error", in)
		}
	}
	if got := (DefaultAttrs | Mtime).String(); got != "left,right,type,content,target,device,mode,owner,mtime" {
		t.Errorf("String() = %q", got)
	}
	if got := None.String(); got != "none" {
		t.Errorf("None.String() = %q", got)
	}
}

func TestCompareAttrs(t *testing.T) {
	files := map[string]string{
		"dir/": "", "mode": "x", "mtime": "x", "content": "x", "link": "->a", "same": "x",
	}
	left := tree(t, files)
	files["content"], files["link"] = "y", "->b"
	right := tree(t, files)
	if err := os.Chmod(filepath.Join(right, "mode"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(right, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, root := range []string{left, right} {
		for _, name := range []string{"dir", "mode", "content", "link", "same"} {
			ts := []unix.Timespec{unix.NsecToTimespec(old.UnixNano()), unix.NsecToTimespec(old.UnixNano())}
			if err := unix.UtimesNanoAt(unix.AT_FDCWD, filepath.Join(root, name), ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Chtimes(filepath.Join(left, "mtime"), old, old); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		compare Attrs
		want    string
	}{
		{0, "~ dir\n| content\n| link\n~ mode\n= mtime\n= same\n"},
		{DefaultAttrs, "~ dir\n| content\n| link\n~ mode\n= mtime\n= same\n"},
		{Content, "= dir\n| content\n= link\n= mode\n= mtime\n= same\n"},
		{Mode, "~ dir\n= content\n= link\n~ mode\n= mtime\n= same\n"},
		{Mode | Mtime, "~ dir\n= content\n= link\n~ mode\n# mtime\n= same\n"},
		{Mtime, "= dir\n= content\n= link\n= mode\n# mtime\n= same\n"},
		{None, "= dir\n= content\n= link\n= mode\n= mtime\n= same\n"},
	} {
		s := scanWait(t, left, right, Options{Compare: tc.compare})
		if got := marks(s.Root); got != tc.want {
			t.Errorf("%v:\n%s\nwant:\n%s", tc.compare, got, tc.want)
		}
	}
}

func TestCompareOwner(t *testing.T) {
	l := Side{Kind: File, Size: 1, Uid: 1, Gid: 1}
	for _, r := range []Side{{Kind: File, Size: 1, Uid: 2, Gid: 1}, {Kind: File, Size: 1, Uid: 1, Gid: 2}} {
		n := &Node{L: l, R: r}
		classify(n, Options{Compare: Owner})
		if n.State != Differ {
			t.Errorf("owner %d:%d vs %d:%d: state %c", l.Uid, l.Gid, r.Uid, r.Gid, n.State.Mark())
		}
		classify(n, Options{Compare: Content})
		if n.State != Pending {
			t.Errorf("owner not compared: state %c", n.State.Mark())
		}
	}
}

func TestMarks(t *testing.T) {
	left := tree(t, map[string]string{
		"both": "x", "content": "x", "times/a": "x", "mixed/a": "x", "mixed/b": "x", "only/a": "x", "only/b": "x",
	})
	right := tree(t, map[string]string{
		"both": "y", "content": "y", "times/a": "x", "mixed/a": "y", "mixed/b": "x", "only/a": "x",
	})
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, root := range []string{left, right} {
		for _, p := range []string{"both", "content", "mixed/a", "mixed/b", "only/a", "mixed", "only", "times"} {
			if err := os.Chtimes(filepath.Join(root, p), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, p := range []string{"both", "mixed/b"} {
		if err := os.Chmod(filepath.Join(right, p), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(left, "times/a"), old, old); err != nil {
		t.Fatal(err)
	}
	// A directory is marked by its own attributes alone, not what differs
	// below it, though it differs.
	if err := os.Chmod(filepath.Join(right, "times"), 0o700); err != nil {
		t.Fatal(err)
	}
	s := scanWait(t, left, right, Options{Compare: DefaultAttrs | Mtime})
	want := "= mixed\n| mixed/a\n~ mixed/b\n= only\n= only/a\n< only/b\n~ times\n# times/a\n* both\n| content\n"
	if got := marks(s.Root); got != want {
		t.Errorf("marks:\n%s\nwant:\n%s", got, want)
	}
	for _, p := range []string{"mixed", "only", "times"} {
		if n := s.Root.Find(p); n.State != Differ {
			t.Errorf("%s: state %c", p, n.State.Mark())
		}
	}
	if n := s.Root.Find("both"); n.Own != Content|Mode {
		t.Errorf("both: own %v", n.Own)
	}

	// Edited to match, the content no longer counts.
	if err := os.WriteFile(filepath.Join(right, "both"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(right, "both"), old, old); err != nil {
		t.Fatal(err)
	}
	s.Recheck("both")
	if n := s.Root.Find("both"); n.Mark() != '~' {
		t.Errorf("both after the edit: mark %c", n.Mark())
	}
}

func TestReclassify(t *testing.T) {
	left := tree(t, map[string]string{"content": "x", "mode": "x", "mtime": "x", "dir/a": "x"})
	right := tree(t, map[string]string{"content": "y", "mode": "x", "mtime": "x", "dir/a": "x"})
	if err := os.Chmod(filepath.Join(right, "mode"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, root := range []string{left, right} {
		for _, p := range []string{"content", "mode", "dir", "dir/a"} {
			if err := os.Chtimes(filepath.Join(root, p), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Chtimes(filepath.Join(left, "mtime"), old, old); err != nil {
		t.Fatal(err)
	}
	s := scanWait(t, left, right, Options{})
	for _, tt := range []struct {
		opts   Options
		want   string
		unread bool
	}{
		{Options{Compare: Mtime}, "= dir\n= dir/a\n= content\n= mode\n# mtime\n", false},
		{Options{Compare: None}, "= dir\n= dir/a\n= content\n= mode\n= mtime\n", false},
		// Read by the scan, the contents are kept.
		{Options{Compare: Content | Mode}, "= dir\n= dir/a\n| content\n~ mode\n= mtime\n", false},
		{Options{Compare: Content, Quick: true}, "= dir\n= dir/a\n= content\n= mode\n? mtime\n", false},
		{Options{}, "= dir\n= dir/a\n| content\n~ mode\n= mtime\n", false},
	} {
		if unread := s.Reclassify(tt.opts); unread != tt.unread {
			t.Errorf("%v: unread %v", tt.opts, unread)
		}
		if got := marks(s.Root); got != tt.want {
			t.Errorf("%v:\n%s\nwant:\n%s", tt.opts, got, tt.want)
		}
		if got, want := s.Options().Compare, tt.opts.Compare; want != 0 && got != want {
			t.Errorf("options %v, want %v", got, want)
		}
	}
	if s.Root.State != Differ {
		t.Errorf("root state %c", s.Root.State.Mark())
	}

	// Contents not read by the scan must be read by another.
	s = scanWait(t, left, right, Options{Compare: Mode})
	if !s.Reclassify(Options{Compare: Content}) {
		t.Error("contents never read, but not reported unread")
	}
	if n := s.Root.Find("content"); n.State != Pending {
		t.Errorf("content: state %c, want pending", n.State.Mark())
	}
}

func TestIgnoredEntries(t *testing.T) {
	left := tree(t, map[string]string{"gone/a": "x", "gone/b/c": "x", "kind": "x", "same": "x"})
	right := tree(t, map[string]string{"new": "x", "kind/": "", "same": "x"})
	s := scanWait(t, left, right, Options{})
	if got, want := marks(s.Root), "< gone\n< gone/b\n< gone/b/c\n< gone/a\n! kind\n> new\n= same\n"; got != want {
		t.Errorf("marks:\n%s\nwant:\n%s", got, want)
	}
	// Not compared, they are ignored, with what is below them, and the
	// tree is the same.
	for _, tt := range []struct {
		compare Attrs
		ignored []string
	}{
		{DefaultAttrs &^ Left, []string{"gone", "gone/a", "gone/b", "gone/b/c"}},
		{DefaultAttrs &^ Right, []string{"new"}},
		{DefaultAttrs &^ Type, []string{"kind"}},
	} {
		s.Reclassify(Options{Compare: tt.compare})
		for _, p := range tt.ignored {
			if n := s.Root.Find(p); n.State != Ignored {
				t.Errorf("%v: %s state %c", tt.compare, p, n.State.Mark())
			}
		}
		if s.Root.State != Differ {
			t.Errorf("%v: root %c", tt.compare, s.Root.State.Mark())
		}
	}
	s.Reclassify(Options{Compare: DefaultAttrs &^ (Left | Right | Type)})
	if s.Root.State != Same {
		t.Errorf("all ignored: root %c", s.Root.State.Mark())
	}
	s.Reclassify(Options{})
	if got := marks(s.Root); !strings.HasPrefix(got, "< gone\n< gone/b\n< gone/b/c\n< gone/a\n! kind\n") {
		t.Errorf("compared again:\n%s", got)
	}
}
