// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package supervise

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseRequest(t *testing.T) {
	for _, tt := range []struct {
		line string
		want Request
	}{
		{"screen on", Request{Kind: "screen", Screen: true}},
		{"screen off", Request{Kind: "screen"}},
		{"suspend", Request{Kind: "suspend"}},
		{"edit left", Request{Kind: "edit", Side: Left}},
		{`edit right "a/b c"`, Request{Kind: "edit", Side: Right, Path: "a/b c", HasPath: true}},
		{`edit left "new\nline\xff"`, Request{Kind: "edit", Side: Left, Path: "new\nline\xff", HasPath: true}},
	} {
		got, err := ParseRequest(tt.line)
		if err != nil || got != tt.want {
			t.Errorf("ParseRequest(%q) = %+v, %v; want %+v", tt.line, got, err, tt.want)
		}
	}
	for _, line := range []string{
		"", "bogus", "screen", "screen maybe", "suspend now", "edit", "edit middle",
		"edit left a/b", `edit left "unterminated`, "edit left `raw`",
	} {
		if _, err := ParseRequest(line); err == nil {
			t.Errorf("ParseRequest(%q) accepted", line)
		}
	}
}

// root creates a directory with a file, a subdirectory with a file, a
// symlink to a file inside, a symlink out, a FIFO, and a file outside.
func root(t *testing.T) (root, outside string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "root")
	outside = filepath.Join(dir, "outside")
	for _, d := range []string{root, filepath.Join(root, "sub")} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(root, "file"), filepath.Join(root, "sub", "file"), outside} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("file", filepath.Join(root, "link-in")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link-out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(root, "sub", "up")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, outside
}

// editPath returns the path an edit request's file stands for: its open
// directory's, as the kernel has it, joined with its name.
func editPath(t *testing.T, p *Parent, req Request) (string, error) {
	t.Helper()
	f, err := p.EditPath(req)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if f.Dir == nil {
		return f.Name, nil
	}
	dir, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(f.Dir.Fd())))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, strings.TrimPrefix(f.Name, "./")), nil
}

// realRoot returns root's path as the kernel reports an open directory's,
// as the temporary directory may be reached through a symlink.
func realRoot(t *testing.T, root string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestEditPathDirectoryMode(t *testing.T) {
	r, _ := root(t)
	p := &Parent{Left: Target{Path: r, Root: true}, Right: Target{Path: r, Root: true}}
	r = realRoot(t, r)
	for _, tt := range []struct {
		req  Request
		want string // the path, or the start of the error after "error: "
	}{
		{Request{Side: Left, Path: "file", HasPath: true}, filepath.Join(r, "file")},
		{Request{Side: Right, Path: "sub/file", HasPath: true}, filepath.Join(r, "sub", "file")},
		{Request{Side: Left, Path: "sub/../file", HasPath: true}, filepath.Join(r, "file")},
		{Request{Side: Left, Path: "../outside", HasPath: true}, "error: the path leads outside"},
		{Request{Side: Left, Path: "sub/../../outside", HasPath: true}, "error: the path leads outside"},
		{Request{Side: Left, Path: "/etc/passwd", HasPath: true}, "error: invalid path"},
		{Request{Side: Left, Path: "", HasPath: true}, "error: invalid path"},
		{Request{Side: Left, Path: "sub/", HasPath: true}, "error: invalid path"},
		{Request{Side: Left, Path: "sub/..", HasPath: true}, "error: invalid path"},
		{Request{Side: Left, Path: "link-in/x", HasPath: true}, "error: the path leads outside the compared directory or through a symlink"},
		{Request{Side: Left, Path: "file/x", HasPath: true}, "error: failed opening the file (not a directory"},
		{Request{Side: Left, Path: "link-in", HasPath: true}, "error: the path leads outside the compared directory or through a symlink"},
		{Request{Side: Left, Path: "link-out", HasPath: true}, "error: the path leads outside the compared directory or through a symlink"},
		{Request{Side: Left, Path: "sub/up/file", HasPath: true}, "error: the path leads outside the compared directory or through a symlink"},
		{Request{Side: Left, Path: "sub", HasPath: true}, "error: the file is no longer a regular file"},
		{Request{Side: Left, Path: "fifo", HasPath: true}, "error: the file is no longer a regular file"},
		{Request{Side: Left, Path: "missing", HasPath: true}, "error: failed opening the file (no such file"},
		{Request{Side: Left}, "error: no path given"},
		{Request{Side: "middle", Path: "file", HasPath: true}, "error: invalid side"},
	} {
		got, err := editPath(t, p, tt.req)
		if err != nil {
			got = "error: " + err.Error()
		}
		if !strings.HasPrefix(got, tt.want) {
			t.Errorf("EditPath(%+v) = %q, want %q", tt.req, got, tt.want)
		}
	}
}

func TestEditPathTwoFileMode(t *testing.T) {
	r, _ := root(t)
	p := &Parent{
		Left:  Target{Path: filepath.Join(r, "file"), File: true},
		Right: Target{Path: "/dev/null"},
	}
	if f, err := p.EditPath(Request{Side: Left}); err != nil || f.Name != filepath.Join(r, "file") || f.Dir != nil {
		t.Errorf("left = %+v, %v", f, err)
	}
	if _, err := p.EditPath(Request{Side: Left, Path: "../outside", HasPath: true}); err == nil {
		t.Error("a path sent in two-file mode was accepted")
	}
	if _, err := p.EditPath(Request{Side: Right}); err == nil {
		t.Error("/dev/null was accepted")
	}
	// Replaced by a directory since startup.
	if err := os.Remove(filepath.Join(r, "file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(r, "file"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := p.EditPath(Request{Side: Left}); err == nil {
		t.Error("a directory was accepted")
	}
}

func TestEditorArgument(t *testing.T) {
	t.Chdir(t.TempDir())
	const plus = `+call writefile(["executed"],"marker")|qall!`
	for _, name := range []string{"-dash", plus, "plain"} {
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		p := &Parent{Left: Target{Path: name, File: true}}
		if f, err := p.EditPath(Request{Side: Left}); err != nil || f.Name != "./"+name {
			t.Errorf("two-file EditPath = %+v, %v; want ./%s", f, err, name)
		}
		p = &Parent{Left: Target{Path: ".", Root: true}}
		f, err := p.EditPath(Request{Side: Left, Path: name, HasPath: true})
		if err != nil || f.Name != "./"+name {
			t.Errorf("tree EditPath = %+v, %v; want ./%s", f, err, name)
		}
		f.Close()
	}
	if got := editorArg("/abs/+x"); got != "/abs/+x" {
		t.Errorf("editorArg(/abs/+x) = %q", got)
	}
}

func TestServe(t *testing.T) {
	r, _ := root(t)
	childEnd, parentEnd := net.Pipe()
	var screens []bool
	var edited []string
	suspended := 0
	p := &Parent{
		Left:   Target{Path: r, Root: true},
		Right:  Target{Path: r, Root: true},
		Screen: func(on bool) { screens = append(screens, on) },
		Edit: func(f EditFile) error {
			edited = append(edited, f.Name)
			if strings.HasSuffix(f.Dir.Name(), "sub") {
				return errors.New("the editor exited with status 1")
			}
			return nil
		},
		Suspend: func() error { suspended++; return nil },
	}
	served := make(chan error, 1)
	go func() { served <- p.Serve(parentEnd) }()

	c := NewClient(childEnd)
	if err := c.Screen(true); err != nil {
		t.Fatal(err)
	}
	if err := c.Edit(Left, "file", true); err != nil {
		t.Errorf("edit: %v", err)
	}
	if err := c.Edit(Right, "sub/file", true); err == nil || err.Error() != "the editor exited with status 1" {
		t.Errorf("edit with a failing editor: %v", err)
	}
	if err := c.Edit(Left, "../outside", true); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("edit outside: %v", err)
	}
	if err := c.Suspend(); err != nil {
		t.Errorf("suspend: %v", err)
	}
	if err := c.Screen(false); err != nil {
		t.Fatal(err)
	}
	_ = childEnd.Close()
	if err := <-served; err != nil {
		t.Errorf("Serve: %v", err)
	}
	if len(screens) != 2 || !screens[0] || screens[1] || suspended != 1 || len(edited) != 2 {
		t.Errorf("screens %v, suspended %d, edited %q", screens, suspended, edited)
	}
}

func TestServeStopsOnABadRequest(t *testing.T) {
	childEnd, parentEnd := net.Pipe()
	p := &Parent{}
	served := make(chan error, 1)
	go func() { served <- p.Serve(parentEnd) }()
	_, _ = childEnd.Write([]byte("rm -rf /\n"))
	if err := <-served; err == nil {
		t.Error("Serve accepted a bad request")
	}
}

// TestEditorStaysInTheCheckedDirectory checks that a directory on the way
// replaced by a symlink out, after the check, doesn't lead the editor out:
// it starts in the directory checked, wherever that is now.
func TestEditorStaysInTheCheckedDirectory(t *testing.T) {
	r, outside := root(t)
	p := &Parent{Left: Target{Path: r, Root: true}}
	f, err := p.EditPath(Request{Side: Left, Path: "sub/file", HasPath: true})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Swap sub for a symlink to a directory holding a "file" outside.
	out := filepath.Dir(outside)
	if err := os.Rename(outside, filepath.Join(out, "file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(r, "sub"), filepath.Join(r, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(r, "sub")); err != nil {
		t.Fatal(err)
	}
	cmd, err := f.Command([]string{"sh", "-c", `echo edited > "$1"`, "sh"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("editor: %v: %s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(r, "moved", "file")); string(b) != "edited\n" {
		t.Errorf("the checked file has %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "file")); string(b) != "x" {
		t.Errorf("the file outside has %q", b)
	}
}

// TestEditorCommand checks the editor's program and arguments: a relative
// program is found from binate's directory, not the file's.
func TestEditorCommand(t *testing.T) {
	r, _ := root(t)
	t.Chdir(r)
	p := &Parent{Left: Target{Path: ".", Root: true}}
	f, err := p.EditPath(Request{Side: Left, Path: "sub/file", HasPath: true})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	editor := []string{"./ed", "-x"}
	cmd, err := f.Command(editor)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(r, "ed"); cmd.Path != want || !slices.Equal(cmd.Args[1:], []string{"-x", "./file"}) {
		t.Errorf("command %q %q, want %q", cmd.Path, cmd.Args, want)
	}
	if editor[1] != "-x" || len(editor) != 2 {
		t.Errorf("editor changed to %q", editor)
	}
	// Without a directory, as in two-file mode, nothing changes.
	cmd, err = EditFile{Name: "./file"}.Command(editor)
	if err != nil || cmd.Path != "./ed" || cmd.Dir != "" {
		t.Errorf("two-file command %q in %q, %v", cmd.Path, cmd.Dir, err)
	}
}

// TestOpenBeneath checks the fallback for kernels or seccomp filters
// without openat2 directly, wherever the tests run.
func TestOpenBeneath(t *testing.T) {
	r, _ := root(t)
	dir, err := os.Open(r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	for path, wantErr := range map[string]error{
		"file":              nil,
		"sub/file":          nil,
		"sub/../file":       nil,
		"../outside":        errEscapes,
		"sub/../../outside": errEscapes,
		"sub/up/file":       errEscapes,
		"missing":           unix.ENOENT,
		"file/x":            unix.ENOTDIR,
	} {
		fd, err := openBeneath(int(dir.Fd()), path)
		if !errors.Is(err, wantErr) {
			t.Errorf("openBeneath(%q) error %v, want %v", path, err, wantErr)
		}
		if err == nil {
			_ = unix.Close(fd)
		}
	}
	// A symlink at the end is opened as a symlink, for the caller to refuse.
	fd, err := openBeneath(int(dir.Fd()), "link-out")
	if err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFLNK {
		t.Errorf("link-out opened as mode %o, %v", st.Mode, err)
	}
	_ = unix.Close(fd)
}
