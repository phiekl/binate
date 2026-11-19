// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/source"
)

// readIndex reads the index at rel and when it was written, or returns
// nil for one missing.
func (w *WorkTree) readIndex(rel string) ([]byte, unix.Timespec, error) {
	f, err := w.openFileAt(rel, followNone)
	if f == nil {
		return nil, unix.Timespec{}, err
	}
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return nil, unix.Timespec{}, err
	}
	data, err := readBounded(f, maxWorkIndex)
	return data, st.Mtim, err
}

// readHead returns the tree of HEAD, empty before the first commit.
func (w *WorkTree) readHead() (*Root, error) {
	// As after a commit since the last scan, and git pack-refs.
	repo := w.repo.withFreshRefs()
	target, err := repo.Resolve("HEAD")
	if err != nil {
		if unborn, uerr := repo.Unborn(); uerr == nil && unborn {
			return NewTargetRoot(w.repo.Repo, Target{Mode: ModeTree}, "HEAD"), nil
		}
		return nil, err
	}
	return NewTargetRoot(w.repo.Repo, target, "HEAD"), nil
}

// unmergedStatus is the status of an unmerged path by the stages it has,
// bit n for stage n, as git status --short shows it.
var unmergedStatus = map[uint8]string{
	1 << 1:             "DD",
	1 << 2:             "AU",
	1<<1 | 1<<2:        "UD",
	1 << 3:             "UA",
	1<<1 | 1<<3:        "DU",
	1<<2 | 1<<3:        "AA",
	1<<1 | 1<<2 | 1<<3: "UU",
}

// letters returns the status of two letters.
func letters(s string) source.Status {
	return source.Status{Staged: s[0], Unstaged: s[1]}
}

// Status returns the status of the entry at rel, as listed d or nil if
// not, as git status --short gives it from HEAD, the index and the disk.
// An untracked entry has "??", as does a directory with no tracked
// entries below it, while a tracked directory has none. Renames are not
// looked for, and a file renamed is deleted at its old path and added at
// the new.
func (w *WorkTree) Status(ctx context.Context, rel string, d *source.Entry) (source.Status, error) {
	if rel == "" {
		return source.Status{}, nil
	}
	if err := w.loaded(); err != nil {
		return source.Status{}, err
	}
	e, tracked := w.index.entries[rel]
	if tracked && e.stages != 0 {
		return letters(unmergedStatus[e.stages]), nil
	}
	head, err := w.head.lookup(rel)
	inHead := err == nil && head.Mode != ModeTree
	if err != nil && !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.ENOTDIR) {
		return source.Status{}, err
	}
	if w.index.tree.isDir(rel) && (d == nil || d.Mode&unix.S_IFMT == unix.S_IFDIR) {
		// A tracked directory has no status, but one staged in place of
		// a file in HEAD has the file's deletion. A file in its place is
		// untracked, as below.
		if inHead {
			return letters("D "), nil
		}
		return source.Status{}, nil
	}
	staged := byte(' ')
	switch {
	case !tracked || e.intentToAdd:
		if inHead {
			staged = 'D'
		}
	case !inHead:
		staged = 'A'
	case kindOf(head.Mode) != kindOf(e.mode):
		staged = 'T'
	case head.Mode != e.mode || head.ID != e.id:
		staged = 'M'
	}
	if !tracked {
		if d != nil && staged == ' ' {
			// Neither in HEAD nor the index.
			return letters("??"), nil
		}
		// A deletion staged. One kept on disk git lists as untracked too.
		return source.Status{Staged: staged, Unstaged: ' '}, nil
	}
	unstaged := byte(' ')
	switch _, skipped := w.skipped(rel); {
	case e.assumeUnchanged:
		// Git takes it at its word, even where deleted from disk.
	case d == nil:
		unstaged = 'D'
	case e.intentToAdd:
		// Added with git add -N, which stages nothing yet.
		unstaged = 'A'
	case !skipped:
		var err error
		if unstaged, err = w.unstaged(ctx, rel, e, d); err != nil {
			return source.Status{}, err
		}
	}
	return source.Status{Staged: staged, Unstaged: unstaged}, nil
}

// kindOf returns a mode's type, one of ModeFile, ModeSymlink,
// ModeSubmodule and ModeTree. A change between two is a type change.
func kindOf(m Mode) Mode {
	if m == ModeExecutable {
		return ModeFile
	}
	return m
}

// unstaged returns what changed on disk at rel, read as st, since its
// index entry e was staged: 'M', 'T' for another type, 'D' for none, or
// ' '.
func (w *WorkTree) unstaged(ctx context.Context, rel string, e workEntry, st *source.Entry) (byte, error) {
	var mode Mode
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		mode = ModeFile
		if st.Mode&0o100 != 0 {
			mode = ModeExecutable
		}
	case unix.S_IFLNK:
		mode = ModeSymlink
	case source.ModeGitlink:
		mode = ModeSubmodule
	default:
		// A directory in place of a file, as git sees it, is the file
		// deleted and an untracked directory.
		return 'D', nil
	}
	switch {
	case kindOf(mode) != kindOf(e.mode):
		return 'T', nil
	case mode == ModeSubmodule:
		// A submodule's checked out commit, not its files.
		if st.Target != e.id.String() {
			return 'M', nil
		}
		return ' ', nil
	case mode != e.mode:
		return 'M', nil
	}
	// As git does, a size that differs from one recorded tells without
	// reading, but an empty file's size may be one git cleared to read the
	// file again.
	if uint32(st.Size) != e.stat.size && e.stat.size != 0 {
		return 'M', nil
	}
	if w.statMatches(st, e.stat, w.mayFilter(rel)) {
		return ' ', nil
	}
	same, err := w.sameContents(ctx, rel, mode, st, e.id)
	if err != nil {
		return 0, err
	}
	if same {
		return ' ', nil
	}
	return 'M', nil
}

// ContentID returns the ID of the regular file at rel where it is known
// without reading the file: that of its staged blob, if its stat data,
// read now, tells it is unchanged since staged, or if it is read from the
// index in place of the disk. A blob Git may have filtered as it staged it
// may hold other bytes than the disk: a file has none where attributes
// that filter apply now, or where its blob's size is not its own, as
// filters that applied when it was staged, and no longer, would change.
func (w *WorkTree) ContentID(rel string) (source.ContentID, bool) {
	if rel == "" || w.loaded() != nil || !w.visiblePath(rel) {
		return source.ContentID{}, false
	}
	e, ok := w.index.entries[rel]
	if !ok || e.stages != 0 || e.intentToAdd || kindOf(e.mode) != ModeFile || w.mayFilter(rel) {
		return source.ContentID{}, false
	}
	if id, skipped := w.skipped(rel); skipped {
		return blobContentID(w.repo.format, id), true
	}
	st, err := w.diskStat(rel)
	if err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return source.ContentID{}, false
	}
	if d := entryOf(st); !w.statMatches(&d, e.stat, false) {
		return source.ContentID{}, false
	}
	if size, err := w.repo.Size(e.id); err != nil || size != st.Size {
		return source.ContentID{}, false
	}
	return blobContentID(w.repo.format, e.id), true
}

// workAttributes are the attributes files that aren't a directory's,
// each taking precedence over the next but a directory's between the
// first and the rest, with only what may make Git filter (see
// attrFile.filtering), and the macros all define.
type workAttributes struct {
	info   *attrFile
	others []*attrFile
	macros map[string][]attrAssign
	// err is set if one couldn't be read.
	err bool
}

// mayFilter reports whether Git may filter the file at rel as it stages
// it, as its attributes and core.autoCRLF tell (see filters). Where an
// attributes file that may apply can't be read, it may.
func (w *WorkTree) mayFilter(rel string) bool {
	w.attrOnce.Do(func() {
		fold := w.settings.ignoreCase
		info, err := w.attributesFile(path.Join(w.common, "info", "attributes"), "")
		top, topErr := w.dirAttributes("")
		user, userErr := w.global.userAttributes, w.global.userAttributesErr
		if w.settings.hasAttributesFile {
			user, userErr = w.repoAttributesFile()
		}
		files := []*attrFile{parseAttributes(info, "", fold, true), top}
		for _, text := range []string{user, w.global.systemAttributes} {
			if text != "" {
				files = append(files, parseAttributes(text, "", fold, true))
			}
		}
		err = err || topErr || userErr || w.global.systemAttributesErr
		a := workAttributes{macros: macrosOf(slices.DeleteFunc(slices.Clone(files), func(f *attrFile) bool { return f == nil })), err: err}
		a.info = files[0].filtering(a.macros)
		for _, f := range files[2:] {
			if f = f.filtering(a.macros); f != nil {
				a.others = append(a.others, f)
			}
		}
		w.attrs = a
	})
	if w.attrs.err {
		return true
	}
	var files []*attrFile
	if w.attrs.info != nil {
		files = append(files, w.attrs.info)
	}
	for dir := parentDir(rel); ; dir = parentDir(dir) {
		d := w.directory(dir)
		d.filterOnce.Do(func() {
			f, _ := w.dirAttributes(dir)
			d.filterAttrs = f.filtering(w.attrs.macros)
		})
		if d.attrsErr {
			return true
		}
		if d.filterAttrs != nil {
			files = append(files, d.filterAttrs)
		}
		if dir == "" {
			break
		}
	}
	files = append(files, w.attrs.others...)
	if len(files) == 0 {
		return w.settings.autocrlf
	}
	return filters(attributesOf(rel, files, w.attrs.macros), w.settings.autocrlf, func(name string) bool { return w.settings.drivers[name] })
}

// repoAttributesFile returns the text of the attributes file the
// repository's configuration sets, read beneath the top as its excludes
// file is, and whether it couldn't be read. One outside the top is out of
// reach after confinement, and with its rules unknown, any file may be
// filtered.
func (w *WorkTree) repoAttributesFile() (string, bool) {
	name := w.settings.attributesFile
	if name == "" || name == os.DevNull {
		return "", false
	}
	data, err := w.readFileAt(expandHome(name, w.global.home), maxIgnoreFile, followAll)
	return string(data), err != nil
}

// dirAttributes returns the .gitattributes file of the directory dir, ""
// for the top, nil if none, and whether it couldn't be read. The top's
// may define macros.
func (w *WorkTree) dirAttributes(dir string) (*attrFile, bool) {
	d := w.directory(dir)
	d.attrOnce.Do(func() {
		name := path.Join(dir, ".gitattributes")
		text, err := w.attributesFile(name, name)
		prefix := ""
		if dir != "" {
			prefix = dir + "/"
		}
		if text != "" {
			d.attrs = parseAttributes(text, prefix, w.settings.ignoreCase, dir == "")
		}
		d.attrsErr = err
	})
	return d.attrs, d.attrsErr
}

// attributesFile returns the text of the attributes file at rel, read
// from disk, or from the index at tracked, if set, where missing, and
// whether it couldn't be read.
func (w *WorkTree) attributesFile(rel, tracked string) (string, bool) {
	data, err := w.readFile(rel, maxIgnoreFile)
	if data == nil && err == nil && tracked != "" {
		e, ok := w.index.entries[tracked]
		if !ok {
			return "", false
		}
		data, err = w.repo.blob(e.id, maxIgnoreFile)
	}
	return string(data), err != nil
}

// parentDir returns the directory of rel, "" for the top.
func parentDir(rel string) string {
	if dir := path.Dir(rel); dir != "." {
		return dir
	}
	return ""
}

// statMatches reports whether st is as the index recorded it, and wasn't
// changed in the same time slot as the index was written, after which the
// two may match whatever the contents. The slot is the second, as in Git,
// or where filtered, as for a file Git may filter as it stages it, the
// nanosecond: such a file's bytes, hashed unfiltered, can't tell it is
// unchanged, and Git, which filters, would find it so.
func (w *WorkTree) statMatches(st *source.Entry, s indexStat, filtered bool) bool {
	mtime := st.Mtime
	if uint32(mtime.Unix()) != s.mtimeSec || uint32(mtime.Nanosecond()) != s.mtimeNsec ||
		uint32(st.Ctime.Unix()) != s.ctimeSec || uint32(st.Ctime.Nanosecond()) != s.ctimeNsec ||
		uint32(st.Ino) != s.ino || st.Uid != s.uid || st.Gid != s.gid || uint32(st.Size) != s.size {
		return false
	}
	index := w.index.mtime
	if index.Sec == 0 && index.Nsec == 0 {
		return true
	}
	if filtered {
		return time.Unix(index.Unix()).After(mtime)
	}
	// In whole seconds, as Git built without USE_NSEC, the usual, tells,
	// which reads what one built with it would too.
	return index.Sec > mtime.Unix()
}

// sameContents reports whether the file or symlink at rel, of mode, hashes
// to the blob id, reading it until ctx is canceled.
func (w *WorkTree) sameContents(ctx context.Context, rel string, mode Mode, st *source.Entry, id ID) (bool, error) {
	h := w.repo.format.new()
	if mode == ModeSymlink {
		_, _ = fmt.Fprintf(h, "blob %d\x00%s", len(st.Target), st.Target)
		return string(h.Sum(nil)) == string(id.Bytes()), nil
	}
	f, err := w.Open(rel, false)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	size := info.Size()
	_, _ = io.WriteString(h, "blob "+strconv.FormatInt(size, 10)+"\x00")
	buf := make([]byte, 256<<10)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		m, err := f.Read(buf)
		h.Write(buf[:m])
		n += int64(m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false, files.UnwrapPath(err)
		}
	}
	if n != size {
		// Changed while read.
		return false, nil
	}
	return string(h.Sum(nil)) == string(id.Bytes()), nil
}

// Recorded returns the entries of the directory at rel in the index and
// in HEAD, which may have statuses though not on disk.
func (w *WorkTree) Recorded(rel string) []source.Recorded {
	if w.loaded() != nil {
		return nil
	}
	var out []source.Recorded
	// seen is each name's place in out, plus one.
	seen := map[string]int{}
	for _, name := range w.index.tree.names[rel] {
		child := path.Join(rel, name)
		mode := w.index.files[child]
		below := w.index.tree.isDir(child)
		if below {
			mode = ModeTree
		}
		out = append(out, source.Recorded{Name: name, Mode: unixMode(mode), Below: below})
		seen[name] = len(out)
	}
	if d, err := w.head.dir(rel); err == nil {
		for _, name := range d.names {
			mode := d.byName[name].Mode
			// A file staged in place of a directory in HEAD has the
			// directory's entries below it still.
			if i := seen[name]; i > 0 {
				out[i-1].Below = out[i-1].Below || mode == ModeTree
				continue
			}
			out = append(out, source.Recorded{Name: name, Mode: unixMode(mode), Below: mode == ModeTree})
		}
	}
	return out
}

var _ source.Statuses = (*WorkTree)(nil)
