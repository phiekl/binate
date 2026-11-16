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
	f, err := files.OpenRegular(path, true)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > max {
		return "", fmt.Errorf("%s: larger than %d bytes", path, max)
	}
	return string(data), nil
}

// Discover finds the repository of the directory dir, as git does: its
// own, or that of a directory above it, not past a file system's boundary
// or a directory in GIT_CEILING_DIRECTORIES, unless GIT_DIR names it.
func Discover(dir string, getenv func(string) string) (*Location, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if gd := getenv("GIT_DIR"); gd != "" {
		if !filepath.IsAbs(gd) {
			gd = filepath.Join(dir, gd)
		}
		loc, err := locate(filepath.Clean(gd), "")
		if err != nil {
			return nil, fmt.Errorf("GIT_DIR %s: %w", gd, err)
		}
		return loc, nil
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
		return nil, &fs.PathError{Op: "stat", Path: dir, Err: err}
	}
	dev := st.Dev
	for cur := dir; ; {
		loc, err := discoverAt(cur)
		if err != nil {
			return nil, err
		}
		if loc != nil {
			if loc.WorkTree != "" {
				rel, err := filepath.Rel(loc.WorkTree, dir)
				if err == nil && rel != "." {
					loc.Prefix = filepath.ToSlash(rel)
				}
			}
			return loc, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur || ceilings[parent] {
			return nil, fmt.Errorf("not a git repository (or any of the parent directories): %s", dir)
		}
		if !across {
			if err := unix.Stat(parent, &st); err != nil {
				return nil, &fs.PathError{Op: "stat", Path: parent, Err: err}
			}
			if st.Dev != dev {
				return nil, fmt.Errorf("not a git repository (or any parent up to mount point %s): %s; stopping at the file system boundary", cur, dir)
			}
		}
		cur = parent
	}
}

// discoverAt returns the repository at dir: a work tree with ".git", a
// directory or a file naming one, or a bare repository; or nil if dir is
// none of those.
func discoverAt(dir string) (*Location, error) {
	dotGit := filepath.Join(dir, ".git")
	fi, err := os.Stat(dotGit)
	switch {
	case err == nil && fi.IsDir():
		if ok, _ := isGitDir(dotGit); ok {
			return locate(dotGit, dir)
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
		if ok, err := isGitDir(target); !ok {
			return nil, fmt.Errorf("%s: not a git repository: %s (%v)", dotGit, target, err)
		}
		return locate(target, dir)
	}
	if ok, _ := isGitDir(dir); ok {
		return locate(dir, "")
	}
	return nil, nil
}

// commonDir returns where the git directory gitDir shares its objects and
// refs: the directory its commondir file names, or gitDir itself.
func commonDir(gitDir string) (string, error) {
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
	return filepath.Clean(dir), nil
}

// isGitDir reports whether dir is a git directory, as git checks: HEAD
// holds a symref into refs/ or an object name, and there are objects and
// refs, in the common directory.
func isGitDir(dir string) (bool, error) {
	head, err := readSmall(filepath.Join(dir, "HEAD"))
	if err != nil {
		return false, err
	}
	if !validHead(head) {
		return false, errors.New("invalid HEAD")
	}
	common, err := commonDir(dir)
	if err != nil {
		return false, err
	}
	for _, sub := range []string{"objects", "refs"} {
		fi, err := os.Stat(filepath.Join(common, sub))
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
func locate(gitDir, workTree string) (*Location, error) {
	common, err := commonDir(gitDir)
	if err != nil {
		return nil, err
	}
	dirs, err := objectDirs(filepath.Join(common, "objects"))
	if err != nil {
		return nil, err
	}
	return &Location{GitDir: gitDir, CommonDir: common, WorkTree: workTree, ObjectDirs: dirs}, nil
}

// ReadDirs returns the directories the repository is read from, for the
// sandbox to allow.
func (l *Location) ReadDirs() []string {
	dirs := []string{l.GitDir}
	if l.CommonDir != l.GitDir {
		dirs = append(dirs, l.CommonDir)
	}
	return append(dirs, l.ObjectDirs[1:]...)
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
	c, err := parseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	f, err := repositoryFormat(c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
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

// repositoryFormat returns the object format the config c gives, checking
// the format version and extensions as git does: version 0 ignores
// extensions but those of version 1; version 1 refuses unknown ones.
func repositoryFormat(c config) (*Format, error) {
	version := 0
	if v, ok := c.get("core.repositoryformatversion"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid core.repositoryformatversion %q", v)
		}
		version = n
	}
	if version > 1 {
		return nil, fmt.Errorf("repository format version %d, which is not supported", version)
	}
	format := SHA1
	for name, values := range c {
		ext, ok := strings.CutPrefix(name, "extensions.")
		if !ok || strings.Contains(ext, ".") {
			continue
		}
		value := values[len(values)-1]
		known, isKnown := extensions[ext]
		switch {
		case version == 0 && isKnown && known.v1:
			return nil, fmt.Errorf("repository format version 0 with extension %s, which needs version 1", ext)
		case version == 0:
			continue
		case !isKnown:
			return nil, fmt.Errorf("repository extension %s, which is not supported", ext)
		}
		switch ext {
		case "objectformat":
			f, err := ParseFormat(strings.ToLower(value))
			if err != nil {
				return nil, err
			}
			format = f
		case "refstorage":
			if strings.ToLower(value) != "files" {
				return nil, fmt.Errorf("refs stored as %s, which is not supported", value)
			}
		}
	}
	return format, nil
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
