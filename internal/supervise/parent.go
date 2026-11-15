// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package supervise

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
)

// Target is a side as the parent knows it from the command line.
type Target struct {
	// Path is the supplied path after resolving a lone directory argument
	// to an entry matching the other side's basename.
	Path string
	// Root is set for a directory compared as a tree: edit requests carry
	// a path relative to it.
	Root bool
	// File is set for a regular file compared on its own: edit requests
	// carry no path, and the parent edits Path itself.
	File bool
}

// Parent answers the child's requests.
type Parent struct {
	Left, Right Target
	// Screen is called with each screen notice.
	Screen func(on bool)
	// Edit runs the editor on a checked file and waits for it.
	Edit func(f EditFile) error
	// Suspend stops the job and returns once it's resumed.
	Suspend func() error
}

// Serve answers requests from conn until the child closes it. A malformed
// request ends serving with an error, since only a broken or compromised
// child sends one.
func (p *Parent) Serve(conn io.ReadWriter) error {
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if errors.Is(err, io.EOF) && line == "" {
			return nil
		}
		if err != nil {
			return err
		}
		req, err := ParseRequest(strings.TrimSuffix(line, "\n"))
		if err != nil {
			return err
		}
		var answer error
		switch req.Kind {
		case "screen":
			if p.Screen != nil {
				p.Screen(req.Screen)
			}
			continue
		case "suspend":
			answer = p.Suspend()
		case "edit":
			var f EditFile
			f, answer = p.EditPath(req)
			if answer == nil {
				answer = p.Edit(f)
				f.Close()
			}
		}
		if _, err := io.WriteString(conn, Answer(answer)); err != nil {
			return err
		}
	}
}

// EditFile is a file checked for editing: the editor's argument, and the
// directory to start it in.
type EditFile struct {
	// Dir is the file's directory in directory mode, held open since it
	// was checked, so that the editor starts in that very directory even
	// if one on the way was since replaced, as by a symlink. Nil in
	// two-file mode, where the editor starts in binate's directory.
	Dir *os.File
	// Name is the editor's argument: the file's name in Dir, or else the
	// path given on the command line, either starting with "./" if
	// relative (see editorArg).
	Name string
}

// Close closes Dir, if any.
func (f EditFile) Close() {
	if f.Dir != nil {
		_ = f.Dir.Close()
	}
}

// Command returns the command running editor, a program and its
// arguments, on f. With Dir, it starts in /proc/self/fd/N, which the
// kernel resolves to Dir itself, not by looking up its path again; a
// relative program is made absolute first, as it would else be looked up
// from Dir.
func (f EditFile) Command(editor []string) (*exec.Cmd, error) {
	prog := editor[0]
	if f.Dir != nil && strings.Contains(prog, "/") && !filepath.IsAbs(prog) {
		abs, err := filepath.Abs(prog)
		if err != nil {
			return nil, err
		}
		prog = abs
	}
	cmd := exec.Command(prog, append(slices.Clone(editor[1:]), f.Name)...)
	if f.Dir != nil {
		cmd.Dir = "/proc/self/fd/" + strconv.Itoa(int(f.Dir.Fd()))
	}
	return cmd, nil
}

// EditPath checks an edit request and returns the file to give the editor.
// In directory mode, the path must name a regular file inside the side's
// root, with no ".." or symlink leading out of it; its directory is
// returned open. In two-file mode, no path may be sent, and the side's file
// must still be a regular file. The caller closes the result.
func (p *Parent) EditPath(req Request) (EditFile, error) {
	var target Target
	switch req.Side {
	case Left:
		target = p.Left
	case Right:
		target = p.Right
	default:
		return EditFile{}, fmt.Errorf("invalid side %q", req.Side)
	}
	switch {
	case target.Root:
		if !req.HasPath {
			return EditFile{}, errors.New("no path given")
		}
		dir, name, err := openBeneathDir(target.Path, req.Path)
		if err != nil {
			return EditFile{}, err
		}
		return EditFile{Dir: dir, Name: editorArg(name)}, nil
	case target.File:
		if req.HasPath {
			return EditFile{}, errors.New("a path was given for a file compared on its own")
		}
		info, err := os.Stat(target.Path)
		if err != nil {
			return EditFile{}, fmt.Errorf("failed opening the file (%w)", files.UnwrapPath(err))
		}
		if !info.Mode().IsRegular() {
			return EditFile{}, errNotRegular
		}
		return EditFile{Name: editorArg(target.Path)}, nil
	}
	return EditFile{}, errors.New("unable to edit this side")
}

// errNotRegular is the error for a file no longer regular.
var errNotRegular = errors.New("the file is no longer a regular file")

// errEscapes is the error for a path that would leave the root.
var errEscapes = errors.New("the path leads outside the compared directory or through a symlink")

// openBeneathDir opens the directory of path, relative to root, without
// following symlinks or leaving root, and checks that path's last name is a
// regular file in it. It returns the directory and that name.
func openBeneathDir(root, path string) (*os.File, string, error) {
	if path == "" || filepath.IsAbs(path) {
		return nil, "", fmt.Errorf("invalid path %q", path)
	}
	dirPath, name := ".", path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		dirPath, name = path[:i], path[i+1:]
	}
	if name == "" || name == "." || name == ".." {
		return nil, "", fmt.Errorf("invalid path %q", path)
	}
	rootDir, err := os.Open(root)
	if err != nil {
		return nil, "", fmt.Errorf("failed opening the compared directory (%w)", files.UnwrapPath(err))
	}
	defer func() { _ = rootDir.Close() }()
	fd, err := unix.Openat2(int(rootDir.Fd()), dirPath, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC | unix.O_DIRECTORY,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
		// Before Linux 5.6, or filtered by seccomp, as in some containers.
		fd, err = openBeneath(int(rootDir.Fd()), dirPath)
	}
	if err != nil {
		if errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ELOOP) || errors.Is(err, errEscapes) {
			return nil, "", errEscapes
		}
		return nil, "", fmt.Errorf("failed opening the file (%w)", err)
	}
	dir := os.NewFile(uintptr(fd), filepath.Join(root, dirPath))
	if err := checkInDir(dir, name); err != nil {
		_ = dir.Close()
		return nil, "", err
	}
	return dir, name, nil
}

// checkInDir checks that dir, opened beneath the root, is a directory, as
// openBeneath doesn't check that of its last name, and that name in it is a
// regular file, not a symlink.
func checkInDir(dir *os.File, name string) error {
	var st unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &st); err != nil {
		return fmt.Errorf("failed opening the file (%w)", err)
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
	case unix.S_IFLNK:
		return errEscapes
	default:
		return fmt.Errorf("failed opening the file (%w)", unix.ENOTDIR)
	}
	if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("failed opening the file (%w)", err)
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return nil
	case unix.S_IFLNK:
		return errEscapes
	}
	return errNotRegular
}

// openBeneath does what openat2 with RESOLVE_BENEATH and
// RESOLVE_NO_SYMLINKS does, one name at a time: every directory on the way
// is opened without following a symlink, and ".." may not climb above
// rootfd. The last name is opened with O_PATH, also without following it,
// so a symlink there is returned as one, for the caller to refuse.
func openBeneath(rootfd int, path string) (int, error) {
	clean := filepath.Clean(path)
	// Lexically, after Clean, ".." can only lead: it never passes through
	// a name that could be a symlink.
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return -1, errEscapes
	}
	names := strings.Split(clean, "/")
	cur, err := unix.Dup(rootfd)
	if err != nil {
		return -1, err
	}
	for i, name := range names {
		flags := unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if i < len(names)-1 {
			flags |= unix.O_DIRECTORY
		}
		next, err := unix.Openat(cur, name, flags, 0)
		_ = unix.Close(cur)
		if errors.Is(err, unix.ENOTDIR) && i < len(names)-1 {
			// A symlink, or a file, where a directory should be.
			var st unix.Stat_t
			if unix.Fstatat(rootfd, strings.Join(names[:i+1], "/"), &st, unix.AT_SYMLINK_NOFOLLOW) == nil &&
				st.Mode&unix.S_IFMT == unix.S_IFLNK {
				return -1, errEscapes
			}
		}
		if err != nil {
			return -1, err
		}
		cur = next
	}
	return cur, nil
}

// editorArg starts a relative path with "./", so the editor never takes it
// for anything but a file name: not an option, as "-name" would be, nor a
// command, as "+name" is to vi, Vim and Emacs.
func editorArg(path string) string {
	if filepath.IsAbs(path) || strings.HasPrefix(path, "./") {
		return path
	}
	return "./" + path
}
