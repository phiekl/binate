// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
)

// Location is where a repository is, as found from a directory: its
// directories, found by reading only the files that name them, so that
// they can be allowed before anything else is read.
type Location struct {
	// GitDir is the repository's directory, or a linked work tree's.
	GitDir string
	// CommonDir holds what work trees share: the objects, most refs and
	// the config. It is GitDir but for a linked work tree.
	CommonDir string
	// WorkTree is the work tree's top, or "" in a bare repository.
	WorkTree string
	// Prefix is the directory searched from, relative to WorkTree, with
	// "/" between names, or "" at the top or in a bare repository.
	Prefix string
	// ObjectDirs are the objects directories: the repository's, then its
	// alternates.
	ObjectDirs []string
}

// maxSmallFile is the most read of a file naming a directory or holding a
// ref.
const maxSmallFile = 64 << 10

// readSmall reads the regular file at path, of at most maxSmallFile bytes.
func readSmall(path string) (string, error) {
	return readLimited(path, maxSmallFile)
}

// readLimited reads the regular file at path, of at most max bytes.
func readLimited(path string, max int64) (string, error) {
	// Not through a symlink, which could lead outside the repository.
	f, err := files.OpenRegular(path, false)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	data, err := readBounded(f, max)
	return string(data), err
}

// readBounded reads all of f, of at most limit bytes.
func readBounded(f *os.File, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", f.Name(), limit)
	}
	return data, nil
}

// FindTop returns the top of the repository of the directory dir, beneath
// which all of it is to be: the nearest of dir and the directories above
// it with ".git" in it, or dir if a bare repository. GIT_DIR is ignored:
// binate finds the repository itself. As git, it stops at a file
// system's boundary, unless GIT_DISCOVERY_ACROSS_FILESYSTEM is true, and
// at a directory in GIT_CEILING_DIRECTORIES. Before the sandbox is applied,
// it resolves symlinks along dir, which may read links outside the repository,
// then searches by names without reading regular files. The top can then be
// all that is allowed before Discover reads the repository.
func FindTop(dir string, getenv func(string) string) (string, error) {
	dir, err := resolveDir(dir)
	if err != nil {
		return "", err
	}
	exists := func(p string) bool {
		_, err := os.Lstat(p)
		return err == nil
	}
	isDir := func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.IsDir()
	}
	// A bare repository only where binate starts: one above would be
	// told apart only by reading its HEAD.
	if !exists(filepath.Join(dir, ".git")) && exists(filepath.Join(dir, "HEAD")) &&
		isDir(filepath.Join(dir, "objects")) && isDir(filepath.Join(dir, "refs")) {
		return dir, nil
	}
	ceilings := map[string]bool{}
	for c := range strings.SplitSeq(getenv("GIT_CEILING_DIRECTORIES"), ":") {
		if filepath.IsAbs(c) {
			ceilings[filepath.Clean(c)] = true
		}
	}
	across, _ := parseBool(getenv("GIT_DISCOVERY_ACROSS_FILESYSTEM"))
	var st unix.Stat_t
	if err := unix.Stat(dir, &st); err != nil {
		return "", &fs.PathError{Op: "stat", Path: dir, Err: err}
	}
	dev := st.Dev
	for cur := dir; ; {
		if exists(filepath.Join(cur, ".git")) {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur || ceilings[parent] {
			return "", fmt.Errorf("not a git repository (or any of the parent directories): %s", dir)
		}
		if !across {
			if err := unix.Stat(parent, &st); err != nil {
				return "", &fs.PathError{Op: "stat", Path: parent, Err: err}
			}
			if st.Dev != dev {
				return "", fmt.Errorf("not a git repository (or any parent up to mount point %s): %s; stopping at the file system boundary", cur, dir)
			}
		}
		cur = parent
	}
}

// resolveDir returns the absolute path of dir with symlinks resolved. A
// relative dir is joined to the working directory first, which $PWD may
// name through symlinks. Neither is cleaned before symlinks are resolved:
// .. after a symlink leads above its target, as in Git.
func resolveDir(dir string) (string, error) {
	if !filepath.IsAbs(dir) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd + string(filepath.Separator) + dir
	}
	return filepath.EvalSymlinks(dir)
}

// Discover finds the repository at top, as FindTop found it from the
// directory dir, beneath it, reading nothing outside top: a repository
// with any of its directories outside, symlinks resolved, as a linked
// work tree's or an alternate's, is refused before anything there is
// read.
func Discover(top, dir string) (*Location, error) {
	top, err := filepath.Abs(top)
	if err != nil {
		return nil, err
	}
	if dir, err = resolveDir(dir); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(top)
	if err != nil {
		return nil, err
	}
	b := &beneath{dir: top, top: resolved}
	loc, err := b.discoverAt(top)
	if err != nil {
		return nil, err
	}
	if loc == nil {
		return nil, fmt.Errorf("not a git repository: %s", top)
	}
	if loc.WorkTree != "" {
		if rel, err := filepath.Rel(loc.WorkTree, dir); err == nil && rel != "." && within(loc.WorkTree, dir) {
			loc.Prefix = filepath.ToSlash(rel)
		}
	}
	return loc, nil
}

// parseBool parses a boolean as git does.
func parseBool(s string) (bool, bool) {
	switch strings.ToLower(s) {
	case "true", "yes", "on", "1":
		return true, true
	case "false", "no", "off", "0", "":
		return false, true
	}
	return false, false
}

// beneath confines discovery to a directory: dir, as given made absolute,
// and top, dir with symlinks resolved. A nil beneath confines nothing.
type beneath struct {
	dir, top string
}

// errOutside is the error of a path outside the repository's top.
var errOutside = errors.New("binate reads nothing outside the repository's top")

// check checks that the path p is beneath the directory, as it is and
// with symlinks resolved, before anything there is read. A path that
// doesn't exist is checked as it is.
func (b *beneath) check(p string) error {
	if b == nil {
		return nil
	}
	p = filepath.Clean(p)
	if !within(b.dir, p) && !within(b.top, p) {
		return fmt.Errorf("%s is outside %s: %w", p, b.dir, errOutside)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !within(b.top, resolved) {
		return fmt.Errorf("%s, a symlink to %s, is outside %s: %w", p, resolved, b.dir, errOutside)
	}
	return nil
}

// within reports whether the clean absolute path p is root or beneath it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// discoverAt returns the repository at dir: a work tree with ".git", a
// directory or a file naming one, or a bare repository; or nil if dir is
// none of those.
func (b *beneath) discoverAt(dir string) (*Location, error) {
	dotGit := filepath.Join(dir, ".git")
	if err := b.check(dotGit); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(dotGit)
	if err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		// Checked beneath dir, as a directory or file in its place is.
		fi, err = os.Stat(dotGit)
	}
	switch {
	case err == nil && fi.IsDir():
		ok, err := b.isGitDir(dotGit)
		if errors.Is(err, errOutside) {
			return nil, err
		}
		if ok {
			return b.locate(dotGit, dir)
		}
	case err == nil && fi.Mode().IsRegular():
		data, err := readSmall(dotGit)
		if err != nil {
			return nil, err
		}
		target, ok := strings.CutPrefix(strings.TrimRight(data, "\r\n"), "gitdir: ")
		if !ok || target == "" {
			return nil, fmt.Errorf("%s: invalid gitfile format", dotGit)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		target = filepath.Clean(target)
		if err := b.check(target); err != nil {
			return nil, fmt.Errorf("%s names a git directory: %w", dotGit, err)
		}
		if ok, err := b.isGitDir(target); !ok {
			return nil, fmt.Errorf("%s: not a git repository: %s (%v)", dotGit, target, err)
		}
		return b.locate(target, dir)
	}
	ok, err := b.isGitDir(dir)
	if errors.Is(err, errOutside) {
		return nil, err
	}
	if ok {
		return b.locate(dir, "")
	}
	return nil, nil
}

// commonDir returns where the git directory gitDir shares its objects and
// refs: the directory its commondir file names, or gitDir itself.
func (b *beneath) commonDir(gitDir string) (string, error) {
	data, err := readSmall(filepath.Join(gitDir, "commondir"))
	if errors.Is(err, fs.ErrNotExist) {
		return gitDir, nil
	}
	if err != nil {
		return "", err
	}
	dir := strings.TrimRight(data, "\r\n")
	if dir == "" {
		return "", fmt.Errorf("%s: empty commondir", gitDir)
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(gitDir, dir)
	}
	dir = filepath.Clean(dir)
	if err := b.check(dir); err != nil {
		return "", fmt.Errorf("%s: commondir: %w", gitDir, err)
	}
	return dir, nil
}

// isGitDir reports whether dir is a git directory, as git checks: HEAD
// holds a symref into refs/ or an object name, and there are objects and
// refs, in the common directory.
func (b *beneath) isGitDir(dir string) (bool, error) {
	head, err := readSmall(filepath.Join(dir, "HEAD"))
	if err != nil {
		return false, err
	}
	if !validHead(head) {
		return false, errors.New("invalid HEAD")
	}
	common, err := b.commonDir(dir)
	if err != nil {
		return false, err
	}
	for _, sub := range []string{"objects", "refs"} {
		p := filepath.Join(common, sub)
		if err := b.check(p); err != nil {
			return false, err
		}
		fi, err := os.Stat(p)
		if err != nil {
			return false, err
		}
		if !fi.IsDir() {
			return false, fmt.Errorf("%s not a directory", sub)
		}
	}
	return true, nil
}

// validHead reports whether head is a HEAD file's contents as git checks
// them: "ref: refs/..." or an object name.
func validHead(head string) bool {
	head = strings.TrimRight(head, "\r\n")
	if target, ok := strings.CutPrefix(head, "ref:"); ok {
		return strings.HasPrefix(strings.TrimLeft(target, " \t"), "refs/")
	}
	return isHex(head) && (len(head) == 2*SHA1.size || len(head) == 2*SHA256.size)
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// locate returns the location of the git directory gitDir, of the work
// tree workTree, finding its common and objects directories.
func (b *beneath) locate(gitDir, workTree string) (*Location, error) {
	common, err := b.commonDir(gitDir)
	if err != nil {
		return nil, err
	}
	dirs, err := b.objectDirs(filepath.Join(common, "objects"))
	if err != nil {
		return nil, err
	}
	return &Location{GitDir: gitDir, CommonDir: common, WorkTree: workTree, ObjectDirs: dirs}, nil
}

// ReadDirs returns the directories the repository is read from, for the
// sandbox to allow: the objects directories too, as one may be a symlink
// to elsewhere beneath the directory searched.
func (l *Location) ReadDirs() []string {
	dirs := []string{l.GitDir}
	if l.CommonDir != l.GitDir {
		dirs = append(dirs, l.CommonDir)
	}
	return append(dirs, l.ObjectDirs...)
}

// Repository is a repository opened: its objects and refs.
type Repository struct {
	*Repo
	loc    *Location
	packed *packedRefs
}

// Open opens the repository at l, checking its format: a version git reads
// with no extension that changes what is read here unknown.
func Open(l *Location) (*Repository, error) {
	format, err := readFormat(filepath.Join(l.CommonDir, "config"))
	if err != nil {
		return nil, err
	}
	repo, err := openRepo(l.ObjectDirs, format)
	if err != nil {
		return nil, err
	}
	data, err := readLimited(filepath.Join(l.CommonDir, "shallow"), maxPackedRefs)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		repo.Close()
		return nil, err
	}
	repo.shallow = map[ID]bool{}
	for line := range strings.Lines(data) {
		id, err := ParseID(format, strings.TrimSuffix(line, "\n"))
		if err != nil {
			repo.Close()
			return nil, fmt.Errorf("shallow: %w", err)
		}
		repo.shallow[id] = true
	}
	return &Repository{Repo: repo, loc: l, packed: &packedRefs{path: filepath.Join(l.CommonDir, "packed-refs"), format: format}}, nil
}

// readFormat reads a repository's object format from its config at path,
// refusing a format it can't read, as git does.
func readFormat(path string) (*Format, error) {
	data, err := readSmall(path)
	if errors.Is(err, fs.ErrNotExist) {
		return SHA1, nil
	}
	if err != nil {
		return nil, err
	}
	f, reftable, err := parseFormat(data)
	if err == nil && reftable {
		err = errors.New("refs stored as reftable, which is not supported")
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// parseFormat returns the object format a repository's config data gives,
// and whether its refs are stored as reftable, refusing a format it can't
// read, as git does.
func parseFormat(data string) (*Format, bool, error) {
	c, err := parseConfig(data)
	if err != nil {
		return nil, false, err
	}
	return repositoryFormat(c)
}

// extensions are those git knows: whether they need format version 1,
// and whether they change how what is read here is read.
var extensions = map[string]struct{ v1, changes bool }{
	"noop":               {false, false},
	"noop-v1":            {true, false},
	"preciousobjects":    {false, false},
	"partialclone":       {false, false},
	"worktreeconfig":     {false, false},
	"objectformat":       {true, true},
	"compatobjectformat": {true, false},
	"refstorage":         {true, true},
}

// repositoryFormat returns the object format the config c gives, and
// whether refs are stored as reftable, checking the format version and
// extensions as git does: version 0 ignores extensions but those of
// version 1; version 1 refuses unknown ones.
func repositoryFormat(c config) (*Format, bool, error) {
	version := 0
	if v, ok := c.get("core.repositoryformatversion"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, false, fmt.Errorf("invalid core.repositoryformatversion %q", v)
		}
		version = n
	}
	if version > 1 {
		return nil, false, fmt.Errorf("repository format version %d, which is not supported", version)
	}
	format, reftable := SHA1, false
	for name, values := range c.values {
		ext, ok := strings.CutPrefix(name, "extensions.")
		if !ok || strings.Contains(ext, ".") {
			continue
		}
		value := values[len(values)-1]
		known, isKnown := extensions[ext]
		switch {
		case version == 0 && isKnown && known.v1:
			return nil, false, fmt.Errorf("repository format version 0 with extension %s, which needs version 1", ext)
		case version == 0:
			continue
		case !isKnown:
			return nil, false, fmt.Errorf("repository extension %s, which is not supported", ext)
		}
		switch ext {
		case "objectformat":
			f, err := ParseFormat(strings.ToLower(value))
			if err != nil {
				return nil, false, err
			}
			format = f
		case "refstorage":
			switch strings.ToLower(value) {
			case "files":
				reftable = false
			case "reftable":
				reftable = true
			default:
				return nil, false, fmt.Errorf("refs stored as %s, which is not supported", value)
			}
		}
	}
	return format, reftable, nil
}
