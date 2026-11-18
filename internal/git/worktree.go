// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/stat"
)

// WorkTree is a disk side with the repository's ignore rules and index.
// Each scan uses its own index and directory caches.
type WorkTree struct {
	root     *os.File
	repo     *Repository
	gitDir   string
	common   string
	metadata map[string]bool
	global   GlobalIgnore
	dirs     sync.Map // relative path to *workDirectory
	// nested caches whether each untracked directory checked is a
	// repository of its own, by relative path.
	nested sync.Map
	// listers limits the goroutines checking untracked directories.
	listers chan struct{}

	// The index and ignore rules are loaded on first use, which for a
	// reload is in the scan rather than on the caller's goroutine.
	loadOnce sync.Once
	loadErr  error
	index    workIndex
	rules    *ignoreRules
	settings workSettings
}

// workSettings are the configuration the work tree follows.
type workSettings struct {
	// fileMode is false where the executable bit on disk isn't to be
	// trusted, and the index's is used instead.
	fileMode bool
	// ignoreCase matches ignore patterns with letters of either case.
	ignoreCase bool
	// sparse is set in a sparse checkout, where Git clears the
	// skip-worktree flag of a file present on disk.
	sparse bool
	// excludesFile is core.excludesFile from the repository's own
	// configuration, if set, which takes the place of the user's.
	excludesFile    string
	hasExcludesFile bool
}

type workDirectory struct {
	rulesOnce sync.Once
	rules     *ignoreRules
	ignored   bool
	listOnce  sync.Once
	names     []string
	listErr   error
}

// OpenWorkTree opens the repository top and the metadata needed to
// decide which entries to show.
func (r *Repository) OpenWorkTree(global GlobalIgnore) (*WorkTree, error) {
	top := r.loc.WorkTree
	if top == "" {
		return nil, errors.New("the repository has no work tree")
	}
	fd, err := unix.Open(top, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: top, Err: err}
	}
	root := os.NewFile(uintptr(fd), top)
	w := &WorkTree{
		root: root, repo: r, global: global, metadata: make(map[string]bool),
		listers: make(chan struct{}, 2*runtime.NumCPU()),
	}
	// A .git file can point at an administrative directory elsewhere
	// beneath the top. Hide that directory as well as the .git file.
	for _, dir := range r.loc.ReadDirs() {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			_ = root.Close()
			return nil, err
		}
		rel, err := filepath.Rel(top, resolved)
		if err != nil || !within(top, resolved) {
			_ = root.Close()
			return nil, fmt.Errorf("%s is outside the work tree", dir)
		}
		rel = filepath.ToSlash(rel)
		if dir == r.loc.GitDir {
			w.gitDir = rel
		}
		if dir == r.loc.CommonDir {
			w.common = rel
		}
		if rel != "." {
			w.metadata[rel] = true
		}
		if alias, err := filepath.Rel(top, dir); err == nil && alias != "." && within(top, dir) {
			w.metadata[filepath.ToSlash(alias)] = true
		}
	}
	if err := w.loaded(); err != nil {
		_ = root.Close()
		return nil, err
	}
	return w, nil
}

// Close releases the root handle after all scans and reads have finished.
func (w *WorkTree) Close() error { return w.root.Close() }

// Editable reports whether the parent has an edit target for this side.
func (w *WorkTree) Editable() bool { return false }

// Reload returns a snapshot for a new scan, which reads a fresh index and
// ignore rules when first listing or reading below the top. An error
// reading them is returned from there. Both snapshots use the same open
// work-tree root.
func (w *WorkTree) Reload() (source.Root, error) {
	return &WorkTree{
		root: w.root, repo: w.repo, gitDir: w.gitDir, common: w.common,
		metadata: w.metadata, global: w.global, listers: w.listers,
	}, nil
}

func (w *WorkTree) loaded() error {
	w.loadOnce.Do(func() { w.loadErr = w.load() })
	return w.loadErr
}

func (w *WorkTree) load() error {
	var err error
	if w.settings, err = w.readSettings(); err != nil {
		return err
	}
	indexPath := path.Join(w.gitDir, "index")
	data, err := w.readFile(indexPath, maxWorkIndex)
	if err != nil {
		return fmt.Errorf("%s: %w", w.Path(indexPath), err)
	}
	if data == nil {
		w.index = workIndex{}
	} else if w.index, err = parseWorkIndex(w.repo.format, data); err != nil {
		return fmt.Errorf("%s: %w", w.Path(indexPath), err)
	}

	// Git warns about an excludes file it can't read and goes on
	// without its rules. Do the same, without the warning.
	global := parseIgnore(w.global.text, w.settings.ignoreCase)
	if w.global.relative != "" {
		global, _ = w.readIgnore(w.global.relative, false)
	}
	if w.settings.hasExcludesFile {
		global = nil
		if rel, ok := w.repoExcludesFile(); ok {
			global, _ = w.readIgnore(rel, false)
		}
	}
	excludes, _ := w.readIgnore(path.Join(w.common, "info", "exclude"), false)
	w.rules = &ignoreRules{patterns: excludes, parent: &ignoreRules{patterns: global}}
	return nil
}

// repoExcludesFile returns the path of the excludes file the repository's
// configuration sets, from the top or absolute, if any. It is left as
// written, for a ".." after a symlink to be resolved as Git's open does.
// One outside the top, out of reach after confinement, is then refused.
func (w *WorkTree) repoExcludesFile() (string, bool) {
	name := w.settings.excludesFile
	if name == "" || name == os.DevNull {
		return "", false
	}
	return expandHome(name, w.global.home), true
}

// readSettings reads the user's configuration, then the repository's and
// the work tree's own, each taking precedence over the last.
func (w *WorkTree) readSettings() (workSettings, error) {
	c := newConfig()
	c.merge(w.global.config)
	name := path.Join(w.common, "config")
	local, err := w.readConfig(name)
	if err != nil {
		return workSettings{}, err
	}
	own, err := local.bool("extensions.worktreeconfig", false)
	if err != nil {
		return workSettings{}, fmt.Errorf("%s: %w", w.Path(name), err)
	}
	if own {
		worktree, err := w.readConfig(path.Join(w.gitDir, "config.worktree"))
		if err != nil {
			return workSettings{}, err
		}
		local.merge(worktree)
	}
	c.merge(local)
	var s workSettings
	s.excludesFile, s.hasExcludesFile = local.get("core.excludesfile")
	if s.fileMode, err = c.bool("core.filemode", true); err != nil {
		return workSettings{}, err
	}
	if s.ignoreCase, err = c.bool("core.ignorecase", false); err != nil {
		return workSettings{}, err
	}
	sparse, err := c.bool("core.sparsecheckout", false)
	if err != nil {
		return workSettings{}, err
	}
	outside, err := c.bool("sparse.expectfilesoutsideofpatterns", false)
	if err != nil {
		return workSettings{}, err
	}
	s.sparse = sparse && !outside
	return s, nil
}

func (w *WorkTree) readConfig(name string) (config, error) {
	data, err := w.readFile(name, maxSmallFile)
	if err != nil {
		return config{}, fmt.Errorf("%s: %w", w.Path(name), err)
	}
	c, err := parseConfig(string(data))
	if err != nil {
		return config{}, fmt.Errorf("%s: %w", w.Path(name), err)
	}
	return c, nil
}

func (w *WorkTree) Path(rel string) string {
	return filepath.Join(w.root.Name(), filepath.FromSlash(rel))
}

func (w *WorkTree) Entry(rel string, _ bool) (source.Entry, error) {
	st, err := w.Stat(rel)
	if st == nil {
		return source.Entry{}, err
	}
	return source.Entry{
		Mode: st.Mode, Size: st.Size, Uid: st.Uid, Gid: st.Gid, Rdev: st.Rdev,
		Mtime: st.Mtime, Target: st.Target,
	}, err
}

func (w *WorkTree) Stat(rel string) (*stat.Stat, error) {
	if rel == "" {
		// Stat'ed when a scan starts, which mustn't wait for the index
		// or touch it while another goroutine loads it.
		st, err := stat.ReadAt(int(w.root.Fd()), ".")
		if st == nil {
			return nil, err
		}
		st.Mode = unixMode(ModeTree)
		return st, err
	}
	if err := w.loaded(); err != nil {
		return nil, err
	}
	if !w.visiblePath(rel) {
		return nil, unix.ENOENT
	}
	if id, ok := w.skipped(rel); ok {
		return w.indexStat(rel, id)
	}
	// A directory of skip-worktree entries needn't be on disk.
	absent := func(err error) bool {
		return w.index.skipTree.isDir(rel) && errors.Is(err, fs.ErrNotExist)
	}
	dir, name, err := w.parent(rel)
	if err != nil {
		if absent(err) {
			return &stat.Stat{Mode: unixMode(ModeTree)}, nil
		}
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	st, err := stat.ReadAt(int(dir.Fd()), name)
	if st == nil {
		if absent(err) {
			return &stat.Stat{Mode: unixMode(ModeTree)}, nil
		}
		return nil, err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		mode := ModeFile
		if !w.settings.fileMode {
			// As git add would record it.
			if m := w.index.files[rel]; m == ModeExecutable {
				mode = m
			}
		} else if st.Mode&0o100 != 0 {
			mode = ModeExecutable
		}
		st.Mode = unixMode(mode)
	case unix.S_IFLNK:
		st.Mode = unixMode(ModeSymlink)
	case unix.S_IFDIR:
		if w.index.files[rel] == ModeSubmodule || w.isNested(rel) {
			id, err := w.submoduleHead(rel)
			if err != nil {
				return nil, fmt.Errorf("reading submodule HEAD: %w", err)
			}
			st.Mode, st.Target = source.ModeGitlink, id.String()
			break
		}
		st.Mode = unixMode(ModeTree)
	}
	return st, err
}

func (w *WorkTree) Open(rel string, _ bool) (source.File, error) {
	if err := w.loaded(); err != nil {
		return nil, err
	}
	if !w.visiblePath(rel) {
		return nil, unix.ENOENT
	}
	if id, ok := w.skipped(rel); ok {
		return w.openIndexed(rel, id)
	}
	dir, name, err := w.parent(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	f, err := files.OpenRegularAt(int(dir.Fd()), name, false)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (w *WorkTree) ReadDir(rel string) ([]string, error) {
	if err := w.loaded(); err != nil {
		return nil, err
	}
	if !w.visiblePath(rel) {
		return nil, unix.ENOENT
	}
	if w.index.files[rel] == ModeSubmodule || w.isNested(rel) {
		return nil, unix.ENOTDIR
	}
	d := w.directory(rel)
	d.listOnce.Do(func() { d.names, d.listErr = w.list(rel, d) })
	return d.names, d.listErr
}

func (w *WorkTree) directory(rel string) *workDirectory {
	if d, ok := w.dirs.Load(rel); ok {
		return d.(*workDirectory)
	}
	d, _ := w.dirs.LoadOrStore(rel, new(workDirectory))
	return d.(*workDirectory)
}

func (w *WorkTree) directoryRules(rel string, d *workDirectory) {
	d.rulesOnce.Do(func() {
		d.rules = w.rules
		prefix := ""
		if rel != "" {
			parent, _ := path.Split(rel)
			parent = strings.TrimSuffix(parent, "/")
			p := w.directory(parent)
			w.directoryRules(parent, p)
			d.rules = p.rules
			d.ignored = p.ignored || d.rules.ignores(rel, true)
			if d.ignored {
				return
			}
			prefix = rel + "/"
		}
		// Git treats a symlink in place of a .gitignore as absent, and
		// warns about one it can't read and goes on without its rules.
		patterns, _ := w.readIgnore(path.Join(rel, ".gitignore"), true)
		if len(patterns) > 0 {
			d.rules = &ignoreRules{prefix: prefix, patterns: patterns, parent: d.rules}
		}
	})
}

func (w *WorkTree) list(rel string, d *workDirectory) ([]string, error) {
	w.directoryRules(rel, d)
	dir, err := files.OpenDirAt(int(w.root.Fd()), rel)
	if errors.Is(err, fs.ErrNotExist) && w.index.skipTree.isDir(rel) {
		return w.index.skipTree.names[rel], nil
	}
	if err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(dir.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	_ = dir.Close()
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), w.Path(rel))
	names, err := f.Readdirnames(-1)
	// Skip-worktree entries are listed whether on disk or not.
	skipNames := w.index.skipTree.names[rel]
	var onDisk map[string]bool
	if len(skipNames) > 0 {
		onDisk = make(map[string]bool, len(names))
		for _, name := range names {
			onDisk[name] = true
		}
	}
	visible := names[:0]
	var untrackedDirs []string
	for _, name := range names {
		child := path.Join(rel, name)
		if strings.EqualFold(name, ".git") || w.metadata[child] {
			continue
		}
		if _, tracked := w.index.files[child]; tracked {
			visible = append(visible, name)
			continue
		}
		var st unix.Stat_t
		if e := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); e != nil {
			if !errors.Is(e, fs.ErrNotExist) && !d.ignored {
				visible = append(visible, name)
			}
			continue
		}
		isDir := st.Mode&unix.S_IFMT == unix.S_IFDIR
		if isDir && w.index.tree.isDir(child) {
			visible = append(visible, name)
			continue
		}
		if d.ignored || d.rules.ignores(child, isDir) {
			continue
		}
		if isDir {
			// Git shows an untracked repository as one entry, as
			// adding it would make a gitlink.
			if w.isNested(child) {
				visible = append(visible, name)
				continue
			}
			untrackedDirs = append(untrackedDirs, name)
			continue
		}
		visible = append(visible, name)
	}
	_ = f.Close()
	// Git doesn't track empty directories. An untracked directory is
	// relevant only if it contains visible paths or read errors. List
	// them in parallel where a goroutine is free, as a large one would
	// hold up this listing. The listings are kept for the scan.
	keep := make([]bool, len(untrackedDirs))
	var wg sync.WaitGroup
	for i, name := range untrackedDirs {
		check := func() {
			nested, err := w.ReadDir(path.Join(rel, name))
			keep[i] = len(nested) > 0 || err != nil
		}
		select {
		case w.listers <- struct{}{}:
			wg.Go(func() {
				defer func() { <-w.listers }()
				check()
			})
		default:
			check()
		}
	}
	wg.Wait()
	for i, name := range untrackedDirs {
		if keep[i] {
			visible = append(visible, name)
		}
	}
	for _, name := range skipNames {
		if !onDisk[name] {
			visible = append(visible, name)
		}
	}
	return visible, err
}

// visiblePath also protects direct reads from reaching metadata or passing
// through a checked-out submodule.
func (w *WorkTree) visiblePath(rel string) bool {
	if rel == "" {
		return true
	}
	if !validIndexPath(rel) {
		return false
	}
	for p := rel; p != "."; p = path.Dir(p) {
		if w.metadata[p] || p != rel && (w.index.files[p] == ModeSubmodule || w.isNested(p)) {
			return false
		}
	}
	return true
}

// isNested reports whether rel is an untracked directory with a
// repository of its own, which Git shows as one entry. Each answer is
// kept for the snapshot, whatever order paths are read in.
func (w *WorkTree) isNested(rel string) bool {
	if _, tracked := w.index.files[rel]; tracked || rel == "" || w.index.tree.isDir(rel) {
		return false
	}
	if nested, ok := w.nested.Load(rel); ok {
		return nested.(bool)
	}
	nested, _ := w.nested.LoadOrStore(rel, w.isRepository(rel))
	return nested.(bool)
}

// isRepository reports whether the directory rel is a repository's work
// tree, as Git checks one: its .git is a git directory, or a file naming
// one. A git directory outside the top can't be read and is assumed
// valid.
func (w *WorkTree) isRepository(rel string) bool {
	dotGit := path.Join(rel, ".git")
	data, err := w.readFile(dotGit, maxSmallFile)
	switch {
	case errors.Is(err, files.ErrNotRegular) || errors.Is(err, unix.ELOOP):
		// A directory, or a symlink to one, which Git follows. One
		// leading out of the top can't be read and is assumed valid.
		if !w.isDir(dotGit, true) {
			return false
		}
		if f, err := files.ResolveDirAt(int(w.root.Fd()), w.root.Name(), dotGit); errors.Is(err, unix.EXDEV) {
			return true
		} else if err == nil {
			_ = f.Close()
		}
		return w.isGitDir(dotGit)
	case err != nil || data == nil:
		return false
	}
	target, ok := parseGitfile(data)
	if !ok {
		return false
	}
	gitDir, err := w.beneath(rel, target)
	if err != nil {
		return false
	}
	return !filepath.IsLocal(gitDir) || w.isGitDir(gitDir)
}

// isGitDir reports whether rel is a git directory: HEAD holds a symref
// into refs/ or an object name, and there are objects and refs in the
// common directory.
func (w *WorkTree) isGitDir(rel string) bool {
	head, err := w.readGitFile(path.Join(rel, "HEAD"), maxSmallFile)
	if err != nil || head == nil || !validHead(string(head)) {
		return false
	}
	common, err := w.commonDir(rel)
	if err != nil {
		return false
	}
	if !filepath.IsLocal(common) {
		return true
	}
	// Git follows symlinks here, as from objects to a shared store.
	return w.isDir(path.Join(common, "objects"), true) && w.isDir(path.Join(common, "refs"), true)
}

// isDir reports whether rel is a directory. With follow, symlinks on the
// way are followed beneath the top, and a final one wherever it leads, as
// that only stats its target and reads nothing.
func (w *WorkTree) isDir(rel string, follow bool) bool {
	dir, name, err := w.parentAt(rel, follow)
	if err != nil {
		return false
	}
	defer func() { _ = dir.Close() }()
	flags := unix.AT_SYMLINK_NOFOLLOW
	if follow {
		flags = 0
	}
	var st unix.Stat_t
	err = unix.Fstatat(int(dir.Fd()), name, &st, flags)
	return err == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR
}

// parseGitfile returns the git directory a .git file names, as Git reads
// it: "gitdir: " and the path, up to line endings.
func parseGitfile(data []byte) (string, bool) {
	target, ok := strings.CutPrefix(strings.TrimRight(string(data), "\r\n"), "gitdir: ")
	return target, ok && target != ""
}

// commonDir returns where the git directory gitDir shares its objects and
// refs, from the top: the directory its commondir file names, or gitDir.
func (w *WorkTree) commonDir(gitDir string) (string, error) {
	data, err := w.readGitFile(path.Join(gitDir, "commondir"), maxSmallFile)
	if err != nil || data == nil {
		return gitDir, err
	}
	target := strings.TrimRight(string(data), "\r\n")
	if target == "" {
		return "", errors.New("empty commondir")
	}
	return w.beneath(gitDir, target)
}

func (w *WorkTree) parent(rel string) (*os.File, string, error) {
	return w.parentAt(rel, false)
}

// parentAt opens the directory of rel, following symlinks on the way that
// stay beneath the top if follow, and returns it and rel's last name.
func (w *WorkTree) parentAt(rel string, follow bool) (*os.File, string, error) {
	dir, name := path.Split(rel)
	if rel == "" {
		name = "."
	}
	if checkName(name) != nil && name != "." {
		return nil, "", unix.EINVAL
	}
	open := files.OpenDirAt
	if follow {
		open = func(root int, rel string) (*os.File, error) {
			return files.ResolveDirAt(root, w.root.Name(), rel)
		}
	}
	f, err := open(int(w.root.Fd()), strings.TrimSuffix(dir, "/"))
	return f, name, err
}

// following is which symlinks a read beneath the top follows. Any it
// follows must stay beneath the top.
type following int

const (
	// followNone follows none, as for work-tree files and a .gitignore.
	followNone following = iota
	// followDirs follows directories on the way, as Git reaches a
	// nested repository's files, as through a symlinked refs.
	followDirs
	// followAll also follows the file itself, as Git opens an excludes
	// file.
	followAll
)

// readFile reads a fixed administrative file without following symlinks.
// A missing file is returned as nil.
func (w *WorkTree) readFile(rel string, limit int64) ([]byte, error) {
	return w.readFileAt(rel, limit, followNone)
}

// readGitFile reads a file in a nested repository's git directory.
func (w *WorkTree) readGitFile(rel string, limit int64) ([]byte, error) {
	return w.readFileAt(rel, limit, followDirs)
}

func (w *WorkTree) readFileAt(rel string, limit int64, follow following) ([]byte, error) {
	// Only symlinks resolved one at a time tell where ".." leads.
	if follow != followAll && !filepath.IsLocal(rel) {
		return nil, unix.EXDEV
	}
	var f *os.File
	var err error
	if follow == followAll {
		f, err = files.OpenRegularBeneath(int(w.root.Fd()), w.root.Name(), rel)
	} else {
		var dir *os.File
		var name string
		dir, name, err = w.parentAt(rel, follow == followDirs)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		f, err = files.OpenRegularAt(int(dir.Fd()), name, false)
		_ = dir.Close()
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readBounded(f, limit)
}

// readIgnore reads the ignore file at rel. Git reads a .gitignore without
// following a symlink, or from the index where it is skip-worktree and
// missing from the disk. It opens an excludes file through symlinks.
func (w *WorkTree) readIgnore(rel string, gitignore bool) ([]ignorePattern, error) {
	follow := followAll
	if gitignore {
		follow = followNone
	}
	data, err := w.readFileAt(rel, maxIgnoreFile, follow)
	if id, ok := w.index.skipped[rel]; ok && gitignore && data == nil && err == nil {
		if m := w.index.files[rel]; m == ModeFile || m == ModeExecutable {
			data, err = w.repo.blob(id, maxIgnoreFile)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", w.Path(rel), err)
	}
	return parseIgnore(string(data), w.settings.ignoreCase), nil
}

// skipped returns the object of the skip-worktree entry at rel, which Git
// compares in place of the disk. In a sparse checkout, Git clears the flag
// of a file present on disk.
func (w *WorkTree) skipped(rel string) (ID, bool) {
	id, ok := w.index.skipped[rel]
	if !ok || w.settings.sparse && w.exists(rel) {
		return ID{}, false
	}
	return id, true
}

func (w *WorkTree) exists(rel string) bool {
	dir, name, err := w.parent(rel)
	if err != nil {
		return false
	}
	defer func() { _ = dir.Close() }()
	var st unix.Stat_t
	return unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil
}

// indexStat returns the skip-worktree entry at rel as the index records it.
func (w *WorkTree) indexStat(rel string, id ID) (*stat.Stat, error) {
	mode := w.index.files[rel]
	st := &stat.Stat{Mode: unixMode(mode)}
	var err error
	switch mode {
	case ModeSubmodule:
		st.Target = id.String()
	case ModeSymlink:
		var target []byte
		target, err = w.repo.blob(id, maxTarget)
		st.Target, st.Size = string(target), int64(len(target))
	default:
		st.Size, err = w.repo.Size(id)
	}
	if err != nil {
		return nil, &fs.PathError{Op: "lstat", Path: w.Path(rel), Err: err}
	}
	return st, nil
}

// openIndexed opens the skip-worktree file at rel from its blob.
func (w *WorkTree) openIndexed(rel string, id ID) (source.File, error) {
	f, err := w.repo.openBlob(id, w.index.files[rel], path.Base(rel))
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: w.Path(rel), Err: err}
	}
	return f, nil
}

func (w *WorkTree) submoduleHead(rel string) (ID, error) {
	gitDir := path.Join(rel, ".git")
	data, err := w.readFile(gitDir, maxSmallFile)
	switch {
	case errors.Is(err, files.ErrNotRegular) || errors.Is(err, unix.ELOOP):
		// The submodule may have an embedded .git directory, or a
		// symlink to one, read through below.
	case err != nil:
		return ID{}, err
	case data == nil:
		// An uninitialized submodule's directory stands for its
		// index entry, as it does in Git's work-tree comparison.
		return w.index.gitlinks[rel], nil
	default:
		target, ok := parseGitfile(data)
		if !ok {
			return ID{}, errors.New("invalid submodule git directory")
		}
		if gitDir, err = w.beneath(rel, target); err != nil {
			return ID{}, err
		}
	}
	// A submodule checked out as a linked work tree has most of its refs
	// in the directory its commondir file names.
	common, err := w.commonDir(gitDir)
	if err != nil {
		return ID{}, err
	}
	name := "HEAD"
	for range maxSymref + 1 {
		dir := common
		if worktreeRef(name) {
			dir = gitDir
		}
		data, err := w.readGitFile(path.Join(dir, name), maxSmallFile)
		if err != nil {
			return ID{}, err
		}
		if data == nil {
			packed, err := w.readGitFile(path.Join(common, "packed-refs"), maxPackedRefs)
			if err != nil {
				return ID{}, err
			}
			refs, err := parsePackedRefs(string(packed), w.repo.format)
			if err != nil {
				return ID{}, err
			}
			if id, ok := refs[name]; ok {
				return id, nil
			}
			// Git compares a submodule with no commits as unchanged
			// from its index entry, and an untracked one has none.
			return w.index.gitlinks[rel], nil
		}
		id, next, err := w.repo.parseRef(name, string(data))
		if err != nil || next == "" {
			return id, err
		}
		name = next
	}
	return ID{}, fmt.Errorf("symrefs nested more than %d deep", maxSymref)
}

// beneath returns the path from the top of target, a path from the
// directory dir or absolute. readFile refuses one outside the top.
func (w *WorkTree) beneath(dir, target string) (string, error) {
	if !filepath.IsAbs(target) {
		return path.Join(dir, target), nil
	}
	rel, err := filepath.Rel(w.root.Name(), target)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
