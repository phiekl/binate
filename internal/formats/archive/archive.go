// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package archive holds the entries of an archive, as a tar or zip file,
// placed as extracting it would place them, as a side of the comparison,
// their files' contents in a temporary file.
package archive

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/stat"
)

// Reader reads an archive of one format from r, as a root shown as name,
// its files' contents written to blob, a new, empty file, which the root
// then owns, and reports how far it has got to p if set. The archive is
// read from r as a stream, or through ReaderAt.
type Reader func(r io.Reader, blob *os.File, name string, p *Progress) (*Root, error)

// Format is a format of archives: how one is read, and how its start tells
// it from others, for an archive given without its format.
type Format struct {
	Read Reader
	// Detect reports whether the archive r reads from its start is of the
	// format, reading as little of it as it needs to tell, which may be
	// all of a compressed block.
	Detect func(r io.Reader) bool
}

// DetectLimit is the most of a pipe's start Format.Detect is given, kept
// to read the pipe from its start again.
const DetectLimit = 16 << 20

// Root is an archive as a side of the comparison, built as its entries
// are read, by Put, HardLink or Link and Store, then Finish. Its methods are safe
// for concurrent use once it is finished: it is not changed after.
type Root struct {
	name, format string
	blob         *os.File
	entries      map[string]*Entry
	// follow reports whether a member is put through a symlink to target
	// put before it, to the directory it leads to, as extracting does.
	follow func(target string) bool
	// off is where the next file's contents go in the blob, and buf what
	// they are copied through. copied is how much of the blob's start the
	// archive was copied to (see ReaderAt).
	off, copied int64
	buf         []byte
	// noModes and noOwners are set where some entry records no mode, or
	// no owner (see Unrecorded).
	noModes, noOwners bool
	// gitModes is set opposite a side of git (see SetGitModes).
	gitModes bool
	// keepDirs is set where a directory stays, empty too, in place of an
	// entry of another type (see KeepDirectories).
	keepDirs bool
}

// KeepDirectories keeps a directory, even an empty one, where a later
// entry of another type has its name, as unzip keeps it. Else only one
// with entries stays, as tar can't remove it.
func (r *Root) KeepDirectories() {
	r.keepDirs = true
}

// SetGitModes gives the entries the modes git records, as a directory
// has opposite a side of git (see source.GitModes).
func (r *Root) SetGitModes() {
	r.gitModes = true
}

// SetUnrecorded says whether some entry records no mode, or no owner:
// then none is compared, or shown, as none the archive records can be
// trusted to be what extracting it makes.
func (r *Root) SetUnrecorded(modes, owners bool) {
	r.noModes, r.noOwners = modes, owners
}

// Unrecorded reports what SetUnrecorded set, for source.Recorder.
func (r *Root) Unrecorded() (modes, owners bool) {
	return r.noModes, r.noOwners
}

// ReaderAt returns the archive in src to read from at any offset, as a
// format with its directory at its end is read, and its size: src itself
// if a regular file, or else src copied to the start of the blob, as a
// pipe. Files are then stored after it, and Finish frees its space. It
// must be called before any file is stored, and reports to p, if set, how
// much of the archive is read, each byte once.
func (r *Root) ReaderAt(src io.Reader, p *Progress) (io.ReaderAt, int64, error) {
	if p == nil {
		p = new(Progress)
	}
	if f, ok := src.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode().IsRegular() {
			return p.CountAt(f), info.Size(), nil
		}
	}
	w := &blobWriter{w: io.NewOffsetWriter(r.blob, 0)}
	n, err := io.Copy(w, p.Count(src))
	switch {
	case w.err != nil:
		return nil, 0, fmt.Errorf("copying the archive to %s: %w", r.blob.Name(), w.err)
	case err != nil:
		return nil, 0, err
	}
	r.off, r.copied = n, n
	return io.NewSectionReader(r.blob, 0, n), n, nil
}

// blobWriter keeps an error writing w, to tell it from one reading.
type blobWriter struct {
	w   io.Writer
	err error
}

func (b *blobWriter) Write(p []byte) (int, error) {
	n, err := b.w.Write(p)
	if err != nil {
		b.err = err
	}
	return n, err
}

// New returns the root, empty, of an archive of format, as "tar", shown
// as name, whose files' contents are written to blob, a new, empty file,
// which the root then owns, and Close closes. A member is put through a
// symlink put before it if follow reports true for its target (see
// FollowedByTar), or never if follow is nil.
func New(name, format string, blob *os.File, follow func(target string) bool) *Root {
	r := &Root{name: name, format: format, blob: blob, entries: map[string]*Entry{}, follow: follow}
	r.entries[""] = implied()
	return r
}

// Entry is an entry of the archive, or a directory it implies, as one
// holding a file listed without its directory.
type Entry struct {
	// Stat is what the archive records of it, but for its links, which
	// Finish counts.
	Stat stat.Stat
	// children are a directory's entries by name as it is read, and names
	// the same once read. extracted counts those not marked as left out
	// (see Mark).
	children  map[string]struct{}
	names     []string
	extracted int
	// off is where a regular file's contents start in the blob.
	off int64
	// links, shared by an entry and its hard links, counts their names in
	// the archive, or is nil for an entry with none. link is set for a
	// hard link not yet put, which counts once it is, and placed for an
	// entry in the tree, which a name counts for.
	links        *uint64
	link, placed bool
	// mark is set for an entry extracting leaves out (see Mark).
	mark bool
	// Err is why the entry can't be read, as a hard link to nothing.
	Err error
}

// ErrFollow is the error of following a symlink, which binate doesn't do
// in an archive.
var ErrFollow = errors.New("symlinks are not followed in an archive")

// NewBlob returns a temporary file in dir for the contents of an
// archive's files, with no name, so that it is gone once closed, even if
// binate is killed. Where the file system can't create one without a
// name, it is created with one, removed at once. The file's Name is dir.
func NewBlob(dir string) (*os.File, error) {
	fd, err := unix.Open(dir, unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
	if err == nil {
		return os.NewFile(uintptr(fd), dir), nil
	}
	if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.EISDIR) && !errors.Is(err, unix.EINVAL) {
		return nil, &os.PathError{Op: "create a temporary file in", Path: dir, Err: err}
	}
	f, err := os.CreateTemp(dir, ".binate-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if err := os.Remove(f.Name()); err != nil {
		return nil, err
	}
	fd, err = unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "create a temporary file in", Path: dir, Err: err}
	}
	return os.NewFile(uintptr(fd), dir), nil
}

// Progress is how far reading an archive has got. Its methods may be
// called as it reads, from any goroutine.
type Progress struct {
	read, entries atomic.Int64
	current       atomic.Pointer[string]
}

// Read returns how much of the archive has been read, as given, before
// it is decompressed.
func (p *Progress) Read() int64 {
	return p.read.Load()
}

// Entries returns the number of entries read.
func (p *Progress) Entries() int64 {
	return p.entries.Load()
}

// Current returns the path, as Root.Path gives it, of the entry being
// read, or "" before the first.
func (p *Progress) Current() string {
	if c := p.current.Load(); c != nil {
		return *c
	}
	return ""
}

// Begin counts an entry, as one being read, at path, as Root.Path gives
// it.
func (p *Progress) Begin(path string) {
	p.current.Store(&path)
	p.entries.Add(1)
}

// Count returns r, counting what is read from it as read of the archive.
func (p *Progress) Count(r io.Reader) io.Reader {
	return &counter{r: r, n: &p.read}
}

// CountAt returns r, counting what is read from it as read of the
// archive, each byte once, however often it is read, as archive/zip reads
// some again, for what is read not to exceed the archive's size.
func (p *Progress) CountAt(r io.ReaderAt) io.ReaderAt {
	return &counterAt{r: r, n: &p.read}
}

// counterAt counts the bytes read from r in n, each once.
type counterAt struct {
	r  io.ReaderAt
	n  *atomic.Int64
	mu sync.Mutex
	// read are the ranges read, from their starts to their ends, sorted
	// and apart.
	read [][2]int64
}

func (c *counterAt) ReadAt(b []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(b, off)
	if n > 0 {
		c.n.Add(c.add(off, off+int64(n)))
	}
	return n, err
}

// add adds the range from start to end to those read, and returns how
// much of it wasn't.
func (c *counterAt) add(start, end int64) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The ranges it overlaps or touches, from i to j.
	i, _ := slices.BinarySearchFunc(c.read, start, func(r [2]int64, start int64) int { return cmp.Compare(r[1], start) })
	j, _ := slices.BinarySearchFunc(c.read, end, func(r [2]int64, end int64) int {
		if r[0] <= end {
			return -1
		}
		return 1
	})
	added := end - start
	merged := [2]int64{start, end}
	for _, r := range c.read[i:j] {
		added -= max(0, min(end, r[1])-max(start, r[0]))
		merged = [2]int64{min(merged[0], r[0]), max(merged[1], r[1])}
	}
	c.read = slices.Replace(c.read, i, j, merged)
	return added
}

// Count returns r, adding what is read from it to n.
func Count(r io.Reader, n *atomic.Int64) io.Reader {
	return &counter{r: r, n: n}
}

// counter counts the bytes read from r in n.
type counter struct {
	r io.Reader
	n *atomic.Int64
}

func (c *counter) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n.Add(int64(n))
	return n, err
}

// Close closes the blob, which frees it.
func (r *Root) Close() error {
	return r.blob.Close()
}

// copyBuffer is how much of a file store reads at a time, and zeroBlock
// the blocks of zeros in it it leaves as holes.
const (
	copyBuffer = 1 << 20
	zeroBlock  = 64 << 10
)

// zeros is a block of zeros, to compare with.
var zeros = make([]byte, zeroBlock)

// Store copies the contents of e, a regular file, from src to the blob,
// after those stored before. An error reading src is returned as it is,
// and one writing the blob saying so. Blocks of zeros are left as holes,
// which take no space, as a sparse file's, which the archive would
// otherwise make take as much as it claims, however small itself.
func (r *Root) Store(e *Entry, src io.Reader) error {
	if r.buf == nil {
		r.buf = make([]byte, copyBuffer)
	}
	e.off = r.off
	n, err := r.store(src, r.off, r.buf)
	r.off += n
	return r.storeError(err)
}

// store copies from src to the blob at off, using buf, and returns how
// much it copied.
func (r *Root) store(src io.Reader, off int64, buf []byte) (int64, error) {
	var n int64
	for {
		k, rerr := readFull(src, buf)
		for i := 0; i < k; {
			// Write the blocks up to the next of zeros at once.
			j := i
			for j < k && !bytes.Equal(buf[j:min(j+zeroBlock, k)], zeros[:min(zeroBlock, k-j)]) {
				j = min(j+zeroBlock, k)
			}
			if j > i {
				if _, err := r.blob.WriteAt(buf[i:j], off+n+int64(i)); err != nil {
					return n, err
				}
			}
			i = min(j+zeroBlock, k)
		}
		n += int64(k)
		switch {
		case rerr == io.EOF:
			return n, nil
		case rerr != nil:
			return n, rerr
		}
	}
}

// readFull reads from r until buf is full, r ends, with io.EOF, or an
// error, which it returns, unlike io.ReadFull, as it is.
func readFull(r io.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		k, err := r.Read(buf[n:])
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// storeError returns err of copying a file to the blob, which may be of
// reading the archive, or of writing the blob, saying where.
func (r *Root) storeError(err error) error {
	var pathErr *fs.PathError
	if err == nil || !errors.As(err, &pathErr) || pathErr.Op != "write" {
		return err
	}
	return fmt.Errorf("storing the archive's files in %s: %w", r.blob.Name(), pathErr.Err)
}

// Clean returns name, of an entry, as a path relative to the root, ""
// for the root itself, and false if it has a ".." in it, which each
// format takes as its tools do: tar refuses the entry, and unzip drops
// the "..". A leading "/" is dropped, as tar and unzip drop it
// extracting.
func Clean(name string) (string, bool) {
	name = strings.TrimLeft(name, "/")
	if slices.Contains(strings.Split(name, "/"), "..") {
		return "", false
	}
	rel := path.Clean(name)
	if rel == "." {
		rel = ""
	}
	return rel, true
}

// implied returns a directory the archive holds entries of but doesn't
// list, made with mode 0755 as tar and unzip make one, but recording
// none.
func implied() *Entry {
	return &Entry{Stat: stat.Stat{Mode: unix.S_IFDIR | 0o755, Implied: true}, children: map[string]struct{}{}}
}

// HardLink makes e, a hard link, the entry at target as read so far, as
// tar links to the file there when it extracts the link: of any type but
// a directory, which can't be linked to (see Link).
func (r *Root) HardLink(e *Entry, target string) {
	t, ok := r.resolve(target)
	if !ok || isDir(t) {
		e.Err = fmt.Errorf("a hard link to %s, where the archive has no file before it", target)
		return
	}
	Link(e, t)
}

// Link makes e a hard link to t, an entry put before it, its contents
// stored, however the format names it: by its path, as tar does (see
// HardLink), or else as by an inode number. Their names count the link,
// once Put puts it, and t's only while it is in the tree. A directory
// can't be linked to.
func Link(e, t *Entry) {
	switch {
	case isDir(t):
		e.Err = errors.New("a hard link to a directory")
		return
	case t.Err != nil:
		e.Err = t.Err
		return
	}
	if t.links == nil {
		t.links = new(uint64)
		if t.placed {
			*t.links = 1
		}
	}
	*e = *t
	e.link, e.placed = true, false
}

// maxSymlinks is how many symlinks walk follows, as Linux's 40, before it
// takes them for a loop.
const maxSymlinks = 40

// walk returns the path of the directory at rel, following symlinks on
// the way as extracting does, in the archive as read so far, and whether
// it is there: each part a directory, or a symlink that leads to one. A
// symlink that leads anywhere else, or nowhere, is no directory, and
// neither is one not followed (see New), nor one in a loop.
func (r *Root) walk(rel string) (string, bool) {
	hops := 0
	return r.walkFrom(rel, &hops)
}

// walkFrom is walk, counting the symlinks followed in hops, as one
// leads to another.
func (r *Root) walkFrom(rel string, hops *int) (string, bool) {
	if rel == "" {
		return "", true
	}
	dir, found := "", true
	for _, name := range strings.Split(rel, "/") {
		p := join(dir, name)
		e, ok := r.entries[p]
		switch {
		case !ok || e.mark:
			found = false
		case e.Stat.Mode&unix.S_IFMT == unix.S_IFLNK && r.follow != nil && r.follow(e.Stat.Target) && *hops < maxSymlinks:
			*hops++
			target := path.Join(dir, e.Stat.Target)
			if target == "." {
				target = ""
			}
			if to, ok := r.walkFrom(target, hops); ok {
				dir = to
				continue
			}
			found = false
		case !isDir(e):
			found = false
		}
		dir = p
	}
	return dir, found
}

// FollowedByTar reports whether a symlink to target is followed in
// extracting a tar archive, as GNU tar makes it at once: not to an
// absolute path, or through "..", which it makes only once all else is
// extracted, so that nothing extracted follows it, nor to nothing, as
// symlink(2) makes no symlink to "".
func FollowedByTar(target string) bool {
	return target != "" && !path.IsAbs(target) && !slices.Contains(strings.Split(target, "/"), "..")
}

// resolve returns the entry at rel, following symlinks in its directories
// but not at its end, as link(2) does tar's hard link (see walk).
func (r *Root) resolve(rel string) (*Entry, bool) {
	dir, ok := r.walk(dirOf(rel))
	if !ok {
		return nil, false
	}
	e, ok := r.entries[join(dir, path.Base(rel))]
	return e, ok
}

// Mark shows e, an entry extracting the archive leaves out, at rel, as
// one that can't be read, by e.Err, changing nothing else: in its
// directories as they are, through symlinks to them, or if one isn't
// there, as a directory in its place that can't be read either. Where an
// entry, or a file in place of a directory, is there, it isn't shown.
// Any entry put later takes its place, as does a directory for one in it.
func (r *Root) Mark(rel string, e *Entry) {
	if rel == "" {
		return
	}
	parts := strings.Split(rel, "/")
	dir := ""
	for i, name := range parts {
		p := join(dir, name)
		if _, ok := r.entries[p]; !ok {
			if i < len(parts)-1 {
				e = &Entry{Stat: stat.Stat{Mode: unix.S_IFDIR | 0o755, Implied: true}, Err: e.Err}
			}
			e.mark = true
			r.entries[dir].children[name] = struct{}{}
			r.entries[p] = e
			return
		}
		to, ok := r.walk(p)
		if i == len(parts)-1 || !ok {
			return
		}
		dir = to
	}
}

// Put adds e at rel, as extracting it would: in its directories, through
// symlinks to them (see walk), made if not listed before, in place of a
// file in their place, and in place of an entry at rel, but for a
// directory there. Another directory takes its place keeping its entries,
// and another type doesn't if it has any, or else if KeepDirectories was
// called.
func (r *Root) Put(rel string, e *Entry) {
	rel = r.place(rel)
	if old, ok := r.entries[rel]; ok {
		switch {
		case old.mark:
		case isDir(old) && isDir(e):
			old.Stat, old.Err = e.Stat, e.Err
			return
		case r.stays(rel, old):
			return
		}
		r.remove(rel)
	}
	if e.link {
		*e.links++
		e.link = false
	}
	if isDir(e) && e.children == nil {
		e.children = map[string]struct{}{}
	}
	r.addChild(rel)
	r.entries[rel] = e
	e.placed = true
}

// Puts reports whether Put puts e at rel, rather than leave what is there:
// if not, a file's contents needn't be stored.
func (r *Root) Puts(rel string, e *Entry) bool {
	rel = r.place(rel)
	old, ok := r.entries[rel]
	return !ok || old.mark || isDir(old) && isDir(e) || !r.stays(rel, old)
}

// place returns where Put puts an entry named rel: through symlinks to
// directories, else where the archive names it, a symlink or a file in
// the way replaced by a directory.
func (r *Root) place(rel string) string {
	if rel == "" {
		return rel
	}
	dir, _ := r.walk(dirOf(rel))
	return join(dir, path.Base(rel))
}

// stays reports whether old, at rel, stays in place of an entry of
// another type: the root, which stays a directory, and a directory with
// entries extracted, as tar fails to replace one, not those marked as
// left out (see Mark), or any if KeepDirectories was called, as unzip
// fails to.
func (r *Root) stays(rel string, old *Entry) bool {
	if rel == "" || isDir(old) && r.keepDirs {
		return true
	}
	return isDir(old) && old.extracted > 0
}

// addChild adds the entry at rel, not marked, to its directory, made if
// missing (see dir).
func (r *Root) addChild(rel string) {
	parent := r.dir(dirOf(rel))
	parent.children[path.Base(rel)] = struct{}{}
	parent.extracted++
}

// dir returns the directory at rel, made if missing or in place of a
// file, and those it is in.
func (r *Root) dir(rel string) *Entry {
	if e, ok := r.entries[rel]; ok {
		if isDir(e) && !e.mark {
			return e
		}
		r.remove(rel)
	}
	e := implied()
	r.addChild(rel)
	r.entries[rel] = e
	return e
}

// remove removes the entry at rel, and what is beneath it if a directory.
func (r *Root) remove(rel string) {
	e, ok := r.entries[rel]
	if !ok {
		return
	}
	delete(r.entries, rel)
	e.placed = false
	if parent, ok := r.entries[dirOf(rel)]; ok {
		delete(parent.children, path.Base(rel))
		if !e.mark {
			parent.extracted--
		}
	}
	if e.links != nil {
		*e.links--
	}
	for name := range e.children {
		r.remove(join(rel, name))
	}
}

// join returns the path of name in the directory at rel.
func join(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

func isDir(e *Entry) bool {
	return e.Stat.Mode&unix.S_IFMT == unix.S_IFDIR
}

// Finish ends the blob after the files stored, and lists each
// directory's entries, and counts the links to each entry, as a file
// system would: a directory's from itself and those in it, a file's from
// each of its names.
func (r *Root) Finish() error {
	// Holes at the end are made by its size.
	if err := r.blob.Truncate(r.off); err != nil {
		return r.storeError(&fs.PathError{Op: "write", Path: r.blob.Name(), Err: files.UnwrapPath(err)})
	}
	if r.copied > 0 {
		// The archive copied is no longer needed: its space is freed, where
		// the file system can, as a hole.
		_ = unix.Fallocate(int(r.blob.Fd()), unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, 0, r.copied)
	}
	r.finish()
	return nil
}

func (r *Root) finish() {
	for rel, e := range r.entries {
		switch {
		case isDir(e):
			e.Stat.Nlink = 2
			e.names = make([]string, 0, len(e.children))
			for name := range e.children {
				e.names = append(e.names, name)
				if child := r.entries[join(rel, name)]; isDir(child) && !child.mark {
					e.Stat.Nlink++
				}
			}
			slices.Sort(e.names)
		case e.links != nil:
			e.Stat.Nlink = *e.links
		default:
			e.Stat.Nlink = 1
		}
	}
	for _, e := range r.entries {
		e.children = nil
	}
}

// dirOf returns the directory of rel, "" for the root.
func dirOf(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[:i]
	}
	return ""
}

// Path returns the path of the entry at rel as the archive's name, a
// colon and rel, or the name for the root.
func (r *Root) Path(rel string) string {
	if rel == "" {
		return r.name
	}
	return r.name + ":" + rel
}

// Archive names the format, for source.Archive.
func (r *Root) Archive() string { return r.format }

// fileMode returns the permissions of a regular file of mode as Stat
// gives them, as an fs.FileMode, with setuid, setgid and sticky.
func (r *Root) fileMode(mode uint32) fs.FileMode {
	if r.gitModes {
		mode = source.GitMode(mode)
	}
	m := fs.FileMode(mode & 0o777)
	for _, bit := range []struct {
		unix uint32
		fs   fs.FileMode
	}{{unix.S_ISUID, fs.ModeSetuid}, {unix.S_ISGID, fs.ModeSetgid}, {unix.S_ISVTX, fs.ModeSticky}} {
		if mode&bit.unix != 0 {
			m |= bit.fs
		}
	}
	return m
}

func (r *Root) pathError(op, rel string, err error) error {
	return &fs.PathError{Op: op, Path: r.Path(rel), Err: err}
}

// lookup returns the entry at rel.
func (r *Root) lookup(op, rel string) (*Entry, error) {
	e, ok := r.entries[rel]
	switch {
	case !ok:
		return nil, r.pathError(op, rel, unix.ENOENT)
	case e.Err != nil:
		return nil, r.pathError(op, rel, e.Err)
	}
	return e, nil
}

func (r *Root) Entry(rel string, follow bool) (source.Entry, error) {
	e, err := r.lookup("lstat", rel)
	if err != nil {
		return source.Entry{}, err
	}
	if follow && e.Stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return source.Entry{}, r.pathError("stat", rel, ErrFollow)
	}
	st := e.Stat
	if r.gitModes {
		st.Mode = source.GitMode(st.Mode)
	}
	return source.Entry{
		Mode: st.Mode, Size: st.Size, Uid: st.Uid, Gid: st.Gid, Rdev: st.Rdev,
		Mtime: st.Mtime, MtimePrecision: st.MtimePrecision, Target: st.Target,
		Implied: st.Implied,
	}, nil
}

func (r *Root) Stat(rel string) (*stat.Stat, error) {
	e, err := r.lookup("lstat", rel)
	if err != nil {
		return nil, err
	}
	st := e.Stat
	if r.gitModes {
		st.Mode = source.GitMode(st.Mode)
	}
	return &st, nil
}

func (r *Root) ReadDir(rel string) ([]string, error) {
	e, err := r.lookup("open", rel)
	if err != nil {
		return nil, err
	}
	if !isDir(e) {
		return nil, r.pathError("open", rel, unix.ENOTDIR)
	}
	return e.names, nil
}

func (r *Root) Open(rel string, follow bool) (source.File, error) {
	e, err := r.lookup("open", rel)
	if err != nil {
		return nil, err
	}
	switch e.Stat.Mode & unix.S_IFMT {
	case unix.S_IFREG:
	case unix.S_IFLNK:
		if follow {
			return nil, r.pathError("open", rel, ErrFollow)
		}
		return nil, r.pathError("open", rel, files.ErrNotRegular)
	default:
		return nil, r.pathError("open", rel, files.ErrNotRegular)
	}
	info := fileInfo{name: path.Base(r.Path(rel)), size: e.Stat.Size, mode: r.fileMode(e.Stat.Mode), mtime: e.Stat.Mtime}
	return &file{SectionReader: io.NewSectionReader(r.blob, e.off, e.Stat.Size), info: info}, nil
}

// file is a regular file of the archive, open for reading.
type file struct {
	*io.SectionReader
	info fileInfo
}

func (f *file) Stat() (fs.FileInfo, error) { return f.info, nil }

// Close does nothing: the blob stays open for the root.
func (f *file) Close() error { return nil }

// fileInfo describes a regular file of the archive.
type fileInfo struct {
	name  string
	size  int64
	mode  fs.FileMode
	mtime time.Time
}

func (fi fileInfo) Name() string       { return fi.name }
func (fi fileInfo) Size() int64        { return fi.size }
func (fi fileInfo) Mode() fs.FileMode  { return fi.mode }
func (fi fileInfo) ModTime() time.Time { return fi.mtime }
func (fi fileInfo) IsDir() bool        { return false }
func (fi fileInfo) Sys() any           { return nil }

var (
	_ source.Archive  = (*Root)(nil)
	_ source.Recorder = (*Root)(nil)
	_ source.File     = (*file)(nil)
)
