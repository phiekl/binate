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
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
)

// Repo reads the objects of a repository. It is safe for concurrent use.
type Repo struct {
	format *Format
	// shallow marks commits whose parents are outside the available history.
	shallow map[ID]bool
	// dirs are the objects directories searched, the repository's first.
	dirs []string
	// packs are the packs in dirs, as found when opened or since an
	// object was missing from them. packsMu orders the searches for more.
	packs   atomic.Pointer[[]*pack]
	packsMu sync.Mutex
	cache   baseCache

	// trees are the trees read as a Root's directories, by ID, shared
	// between the roots, as two revisions' trees are mostly the same.
	trees treeCache

	graphMu      sync.Mutex
	graphLoading chan struct{}
	graphReady   bool
	graph        commitGraph
}

// OpenRepo returns the repository whose objects directory is dir, of
// format f, with the objects directories it names as alternates. The
// packs are those there now, and those added later are looked for when an
// object is in none of them, as after git gc.
func OpenRepo(dir string, f *Format) (*Repo, error) {
	dirs, err := (*beneath)(nil).objectDirs(dir)
	if err != nil {
		return nil, err
	}
	return openRepo(dirs, f)
}

// openRepo returns the repository whose objects directories, alternates
// and all, are dirs.
func openRepo(dirs []string, f *Format) (*Repo, error) {
	r := &Repo{format: f, dirs: dirs, cache: baseCache{limit: defaultCacheLimit}, trees: treeCache{limit: defaultTreeCacheLimit}}
	var packs []*pack
	for _, d := range r.dirs {
		var err error
		if packs, err = r.openPacks(d, packs, nil); err != nil {
			r.packs.Store(&packs)
			r.Close()
			return nil, err
		}
	}
	r.packs.Store(&packs)
	return r, nil
}

// packList returns the packs open.
func (r *Repo) packList() []*pack {
	if p := r.packs.Load(); p != nil {
		return *p
	}
	return nil
}

// reloadPacks opens the packs added to the objects directories since they
// were listed, as git gc and git fetch write, and returns all those open.
// Those open before stay open and read, though deleted since, as by git
// gc. A pack that can't be opened, as one being written, is left out as
// before.
func (r *Repo) reloadPacks() []*pack {
	r.packsMu.Lock()
	defer r.packsMu.Unlock()
	old := r.packList()
	open := make(map[string]bool, len(old))
	for _, p := range old {
		open[p.path] = true
	}
	packs := slices.Clone(old)
	for _, d := range r.dirs {
		packs, _ = r.openPacks(d, packs, open)
	}
	if len(packs) > len(old) {
		r.packs.Store(&packs)
	}
	return packs
}

// Dirs returns the objects directories read: the repository's, and its
// alternates.
func (r *Repo) Dirs() []string {
	return slices.Clone(r.dirs)
}

// maxAlternateDepth is how deep alternates of alternates are followed, as
// git follows them.
const maxAlternateDepth = 5

// objectDirs returns dir and its alternates, each once, in the order git
// searches them.
func (b *beneath) objectDirs(dir string) ([]string, error) {
	dir = filepath.Clean(dir)
	if err := b.check(dir); err != nil {
		return nil, err
	}
	dirs := []string{dir}
	seen := map[string]bool{dir: true}
	var add func(d string, depth int) error
	add = func(d string, depth int) error {
		alts, err := readAlternates(d)
		if err != nil {
			return err
		}
		for _, alt := range alts {
			if seen[alt] {
				continue
			}
			if err := b.check(alt); err != nil {
				return fmt.Errorf("%s: alternate: %w", d, err)
			}
			if _, err := os.Stat(alt); errors.Is(err, fs.ErrNotExist) {
				// Skipped, as git skips it.
				continue
			}
			if depth >= maxAlternateDepth {
				return fmt.Errorf("%s: alternates nested more than %d deep", d, maxAlternateDepth)
			}
			seen[alt] = true
			dirs = append(dirs, alt)
			if err := add(alt, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := add(dir, 0); err != nil {
		return nil, err
	}
	return dirs, nil
}

// readAlternates returns the objects directories the objects directory
// dir names in info/alternates: one a line, relative to dir if not
// absolute, quoted as a C string if it starts with a quote; empty lines
// and those starting with "#" are skipped.
func readAlternates(dir string) ([]string, error) {
	path := filepath.Join(dir, "info", "alternates")
	f, err := files.OpenRegular(path, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var alts []string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '"' {
			unq, err := strconv.Unquote(line)
			if err != nil {
				return nil, fmt.Errorf("%s: invalid quoted path %s", path, line)
			}
			line = unq
		}
		if !filepath.IsAbs(line) {
			line = filepath.Join(dir, line)
		}
		alts = append(alts, filepath.Clean(line))
	}
	return alts, nil
}

// openPacks appends to packs those in the objects directory dir, but for
// the indexes open names. An index without its pack, as one being
// written, is left out, as git does. On an error, it returns packs with
// those opened before it, unless reloading, with open set, where a pack
// that can't be opened is left out and the rest opened.
func (r *Repo) openPacks(dir string, packs []*pack, open map[string]bool) ([]*pack, error) {
	names, err := readDirNames(filepath.Join(dir, "pack"))
	if errors.Is(err, fs.ErrNotExist) {
		return packs, nil
	}
	if err != nil {
		return packs, err
	}
	slices.Sort(names)
	for _, name := range names {
		if !strings.HasSuffix(name, ".idx") {
			continue
		}
		idx := filepath.Join(dir, "pack", name)
		if open[idx] {
			continue
		}
		if _, err := os.Lstat(strings.TrimSuffix(idx, ".idx") + ".pack"); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		p, err := openPack(r, idx)
		if err != nil && open != nil {
			continue
		}
		if err != nil {
			return packs, fmt.Errorf("%s: %w", idx, err)
		}
		packs = append(packs, p)
	}
	return packs, nil
}

func readDirNames(dir string) ([]string, error) {
	// Not through a symlink, which could lead outside the repository.
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: dir, Err: err}
	}
	f := os.NewFile(uintptr(fd), dir)
	defer func() { _ = f.Close() }()
	return f.Readdirnames(-1)
}

// Close closes the packs and commit graph. Call it after all reads finish.
func (r *Repo) Close() {
	for _, p := range r.packList() {
		_ = p.f.Close()
		_ = unmap(p.idx)
	}
	r.packs.Store(nil)
	r.graph.close()
	r.graph = nil
}

// Format returns the repository's object format.
func (r *Repo) Format() *Format { return r.format }

// Object is an object being read: its type, its size, and a reader of its
// content, which fails at its end if the object turns out corrupt.
type Object struct {
	Type Type
	Size int64
	io.ReadCloser
}

// find returns the open pack holding id and where, or nil if none does,
// for the object to be read loose (see openLoose).
func (r *Repo) find(id ID) (*pack, int64, error) {
	return findIn(r.packList(), id)
}

// findIn returns the pack of packs holding id and where, or nil if none
// does.
func findIn(packs []*pack, id ID) (*pack, int64, error) {
	for _, p := range packs {
		off, ok, err := p.idx.find(id)
		if err != nil {
			return nil, 0, err
		}
		if ok {
			return p, off, nil
		}
	}
	return nil, 0, nil
}

// Open opens the object id.
func (r *Repo) Open(id ID) (Object, error) {
	p, off, err := r.find(id)
	if err != nil {
		return Object{}, err
	}
	if p != nil {
		return p.open(off, id)
	}
	return r.openLoose(id)
}

// openLoose searches the loose objects after packs have been searched.
// Where id isn't loose either, packs added since are searched, as Git
// does: git gc may have packed it, and pruned its loose copy since the
// packs were. All are, as another reader may have opened the new ones.
func (r *Repo) openLoose(id ID) (Object, error) {
	for _, dir := range r.dirs {
		obj, err := openLoose(dir, id)
		if !errors.Is(err, ErrNotFound) {
			return obj, err
		}
	}
	p, off, err := findIn(r.reloadPacks(), id)
	if err != nil {
		return Object{}, err
	}
	if p != nil {
		return p.open(off, id)
	}
	return Object{}, fmt.Errorf("%s: %w", id, ErrNotFound)
}

// read returns the type and content of the object id, a delta's base
// depth deltas down, kept in the cache if in a pack: it must not be
// changed.
func (r *Repo) read(id ID, depth int) (Type, []byte, error) {
	p, off, err := r.find(id)
	if err != nil {
		return 0, nil, err
	}
	if p != nil {
		return p.read(off, id, depth)
	}
	return r.Read(id, maxObject)
}

// info returns the type and size of the object id, a delta's base depth
// deltas down.
func (r *Repo) info(id ID, depth int) (Type, int64, error) {
	p, off, err := r.find(id)
	if err != nil {
		return 0, 0, err
	}
	if p != nil {
		return p.info(off, id, depth)
	}
	obj, err := r.openLoose(id)
	if err != nil {
		return 0, 0, err
	}
	_ = obj.Close()
	return obj.Type, obj.Size, nil
}

// Size returns the size of the object id: what Info returns, but faster
// for an object made of deltas, as its type is not looked up.
func (r *Repo) Size(id ID) (int64, error) {
	p, off, err := r.find(id)
	if err != nil {
		return 0, err
	}
	if p != nil {
		return p.size(off, id)
	}
	obj, err := r.openLoose(id)
	if err != nil {
		return 0, err
	}
	_ = obj.Close()
	return obj.Size, nil
}

// Info returns the type and size of the object id.
func (r *Repo) Info(id ID) (Type, int64, error) {
	return r.info(id, 0)
}

// Read returns the type and content of the object id, which must not be
// larger than max bytes.
func (r *Repo) Read(id ID, max int64) (Type, []byte, error) {
	obj, err := r.Open(id)
	if err != nil {
		return 0, nil, err
	}
	return readObject(id, obj, max)
}

// readObject reads and closes an already opened object.
func readObject(id ID, obj Object, max int64) (Type, []byte, error) {
	defer func() { _ = obj.Close() }()
	if obj.Size > max {
		return 0, nil, fmt.Errorf("%s: %s of %d bytes, larger than %d", id, obj.Type, obj.Size, max)
	}
	data := make([]byte, obj.Size)
	if _, err := io.ReadFull(obj, data); err != nil {
		return 0, nil, err
	}
	// Read to the end, which checks the object.
	if _, err := obj.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		if err == nil {
			err = corrupt(id, "longer than its size")
		}
		return 0, nil, err
	}
	return obj.Type, data, nil
}

// maxTree is the largest tree, commit or tag read, as a corrupt or hostile
// repository could claim any size. Git's own largest trees are a few
// megabytes.
const maxTree = 256 << 20

// ReadTree returns the entries of the tree id.
func (r *Repo) ReadTree(id ID) ([]TreeEntry, error) {
	typ, data, err := r.Read(id, maxTree)
	if err != nil {
		return nil, err
	}
	if typ != Tree {
		return nil, fmt.Errorf("%s is a %s, not a tree", id, typ)
	}
	entries, err := ParseTree(r.format, data)
	if err != nil {
		return nil, corrupt(id, "%v", err)
	}
	return entries, nil
}

// maxPeel is how many tags are followed to a commit or tree, past which a
// chain is taken to be a loop.
const maxPeel = 32

// PeelTree returns the tree id names: id itself if a tree, a commit's
// tree, or that of what a tag points to.
func (r *Repo) PeelTree(id ID) (ID, error) {
	return r.peelTo(id, Tree)
}

// header returns the value of the header field name of a commit or tag:
// the lines before the first empty one.
func header(data []byte, name string) (string, bool) {
	for line := range headerLines(data) {
		if k, v, ok := strings.Cut(line, " "); ok && k == name {
			return v, true
		}
	}
	return "", false
}
