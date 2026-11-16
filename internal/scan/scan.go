// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package scan walks two directory trees at once, pairs their entries by
// name and compares each pair.
package scan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/source"
)

// Kind is the type of an entry on one side.
type Kind uint8

const (
	Missing Kind = iota
	Dir
	File
	Symlink
	FIFO
	Socket
	CharDev
	BlockDev
	Unknown // could not be read, or a type Linux doesn't name here
)

func (k Kind) String() string {
	return [...]string{"missing", "directory", "file", "symlink", "fifo", "socket",
		"character device", "block device", "unknown"}[k]
}

// Side is an entry on one side.
type Side struct {
	Kind Kind
	// Size is -1 if unknown (see source.Entry).
	Size   int64
	Mtime  time.Time
	Target string // of a symlink
	Rdev   uint64 // of a device
	Perm   uint32 // permission bits, with setuid, setgid and sticky
	Uid    uint32
	Gid    uint32
	Count  int // entries in a directory, after excludes
	// Err is why this side could not be read fully: its type, a
	// directory's entries, a symlink's target or a file's contents.
	Err error
}

// State is the result of comparing a pair.
type State uint8

const (
	Pending    State = iota // not compared yet
	Same                    // no difference found
	Differ                  // a difference was found
	Unverified              // a side could not be read, or quick mode can't tell
	LeftOnly                // present on the left side only
	RightOnly               // present on the right side only
	TypeDiff                // different entry types
	// Ignored is an entry on one side only, or of different types, when
	// those aren't compared: left out of the tree.
	Ignored
)

// Executable reports whether sd is a regular file with an execute bit.
func (sd *Side) Executable() bool {
	return sd.Kind == File && sd.Perm&0o111 != 0
}

// Mark returns the character shown for s between the two sides. A pair
// that differs has a mark by what differs instead (see Node.Mark).
func (s State) Mark() rune {
	return [...]rune{' ', '=', '|', '?', '<', '>', '!', ' '}[s]
}

// IsDiff reports whether s is a difference: one the diffs-only filter
// keeps and difference jumps stop at.
func (s State) IsDiff() bool {
	return s == Differ || s == LeftOnly || s == RightOnly || s == TypeDiff
}

// Node is a pair of entries of the same name.
type Node struct {
	Name     string
	Parent   *Node
	Children []*Node
	L, R     Side
	State    State
	// Own is the attributes compared that differ on the pair itself, not
	// below it.
	Own Attrs
	// Data is the result of comparing the data of a pair of the same
	// type, not directories, apart from their attributes: their contents,
	// target or device. It is Same, Differ, Unverified, or Pending until
	// compared, and for other pairs, whose State says all. State may be
	// Differ for attributes while Data is Unverified.
	Data State
	// content is the result of comparing a pair of files' contents, kept
	// for Reclassify, or Pending if they haven't been.
	content State
}

// BothDirs reports whether n is a directory on both sides.
func (n *Node) BothDirs() bool {
	return n.L.Kind == Dir && n.R.Kind == Dir
}

// Mark returns the character shown for n between the two sides: for a
// pair that differs, '|' for its data (Data), '~' for its attributes
// (Meta), '#' for its mtime, or '*' for more than one of them. A
// directory on both sides is marked by itself alone, not what is below
// it: by its attributes, '?' if a side could not be listed, or else '='.
func (n *Node) Mark() rune {
	if n.BothDirs() {
		switch {
		case n.Own != 0:
			return n.Own.mark()
		case n.L.Err != nil || n.R.Err != nil:
			return Unverified.Mark()
		}
		return Same.Mark()
	}
	if n.State != Differ {
		return n.State.Mark()
	}
	return n.Own.mark()
}

// mark returns the mark for differences in a: '|', '~' or '#' by kind,
// or '*' for more than one kind.
func (a Attrs) mark() rune {
	kinds, mark := 0, '|'
	for _, k := range []struct {
		attrs Attrs
		mark  rune
	}{{Mtime, '#'}, {Meta, '~'}, {Data, '|'}} {
		if a&k.attrs != 0 {
			kinds++
			mark = k.mark
		}
	}
	if kinds > 1 {
		return '*'
	}
	return mark
}

// Path returns the node's path relative to the roots, with "/" between
// names, or "" for the roots.
func (n *Node) Path() string {
	if n.Parent == nil {
		return ""
	}
	if n.Parent.Parent == nil {
		return n.Name
	}
	return n.Parent.Path() + "/" + n.Name
}

// IsDir reports whether either side is a directory, which may hold entries.
func (n *Node) IsDir() bool {
	return n.L.Kind == Dir || n.R.Kind == Dir
}

// Side returns the left (false) or right (true) side.
func (n *Node) Side(right bool) *Side {
	if right {
		return &n.R
	}
	return &n.L
}

// Find returns the node at path, relative to n, or nil.
func (n *Node) Find(p string) *Node {
	if p == "" {
		return n
	}
	cur := n
	for _, name := range strings.Split(p, "/") {
		var next *Node
		for _, c := range cur.Children {
			if c.Name == name {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// Attrs is a set of attributes to compare, and whether entries on one
// side only, or of different types, count.
type Attrs uint16

const (
	Content Attrs = 1 << iota // a regular file's size and contents
	Target                    // a symlink's target
	Device                    // a device's number
	Mode                      // permission bits, with setuid, setgid and sticky
	Owner                     // user and group
	Mtime                     // modification time
	Left                      // entries on the left side only
	Right                     // entries on the right side only
	Type                      // entries of different types

	// None is set alone to compare nothing, as 0 means DefaultAttrs in
	// Options.
	None Attrs = 1 << 15
)

// Data is what an entry holds, Meta its attributes besides the mtime.
const (
	Data = Content | Target | Device
	Meta = Mode | Owner
)

// AllAttrs are everything there is to compare.
const AllAttrs = Left | Right | Type | Content | Target | Device | Mode | Owner | Mtime

// DefaultAttrs are compared unless asked otherwise: all but the mtime.
const DefaultAttrs = Left | Right | Type | Content | Target | Device | Mode | Owner

// attrNames are the names of the attributes, in order, as ParseAttrs
// takes them.
var attrNames = []struct {
	name string
	attr Attrs
}{
	{"left", Left},
	{"right", Right},
	{"type", Type},
	{"content", Content},
	{"target", Target},
	{"device", Device},
	{"mode", Mode},
	{"owner", Owner},
	{"mtime", Mtime},
}

// AttrNames returns the names ParseAttrs takes, in order.
func AttrNames() []string {
	names := make([]string, len(attrNames))
	for i, a := range attrNames {
		names[i] = a.name
	}
	return names
}

// ParseAttrs parses a comma-separated list of attribute names. Either
// every name has a sign, "+" to add it to DefaultAttrs or "-" to take it
// away, or none has, and the list replaces them. A list left empty, such
// as by taking every default away, is None, as is "none" alone, which
// String returns for it.
func ParseAttrs(list string) (Attrs, error) {
	if list == "none" {
		return None, nil
	}
	var set Attrs
	signed := strings.HasPrefix(list, "+") || strings.HasPrefix(list, "-")
	if signed {
		set = DefaultAttrs
	}
	for item := range strings.SplitSeq(list, ",") {
		name := strings.TrimLeft(item, "+-")
		if signed != (name != item) || len(item)-len(name) > 1 {
			return 0, fmt.Errorf("%q: give every attribute a + or -, or none", item)
		}
		attr := Attrs(0)
		for _, a := range attrNames {
			if a.name == name {
				attr = a.attr
			}
		}
		if attr == 0 {
			return 0, fmt.Errorf("unknown attribute %q, expected some of %s", name, strings.Join(AttrNames(), ", "))
		}
		if item[0] == '-' {
			set &^= attr
		} else {
			set |= attr
		}
	}
	if set == 0 {
		return None, nil
	}
	return set, nil
}

// String returns the attributes' names, comma-separated, or "none".
func (a Attrs) String() string {
	var names []string
	for _, n := range attrNames {
		if a&n.attr != 0 {
			names = append(names, n.name)
		}
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ",")
}

// Options configure a scan.
type Options struct {
	// Compare is the attributes compared; 0 means DefaultAttrs, and None
	// none.
	Compare Attrs
	// Quick compares size and modification time instead of contents.
	Quick bool
	// Excludes are path.Match patterns of entry names to skip.
	Excludes []string
	// Workers is the number of entries read at once; 0 means twice the
	// CPU count. Each worker has at most two files open.
	Workers int
}

// Scan is a scan of two roots, running or done. Its tree may be read only
// while Lock is held, as the scan writes it.
type Scan struct {
	Root  *Node
	left  source.Root
	right source.Root
	opts  Options

	mu      sync.Mutex
	scanned atomic.Int64
	current atomic.Pointer[string]
	started time.Time
	elapsed atomic.Int64

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	notify func(done bool)

	qmu     sync.Mutex
	qcond   *sync.Cond
	queue   []*Node
	pending int
}

// Start starts scanning the roots left and right in the background, and
// calls notify, from another goroutine, as it makes progress (done false)
// and once done (done true).
func Start(left, right source.Root, opts Options, notify func(done bool)) *Scan {
	if opts.Workers <= 0 {
		opts.Workers = 2 * runtime.NumCPU()
	}
	if opts.Compare == 0 {
		opts.Compare = DefaultAttrs
	}
	if notify == nil {
		notify = func(bool) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Scan{
		Root:    &Node{},
		left:    left,
		right:   right,
		opts:    opts,
		started: time.Now(),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		notify:  notify,
	}
	s.qcond = sync.NewCond(&s.qmu)
	s.Root.L = statSide(left, "", true)
	s.Root.R = statSide(right, "", true)
	s.Root.Own = attrsDiffer(&s.Root.L, &s.Root.R, opts.Compare)
	s.push(s.Root)
	go s.run()
	return s
}

// Lock locks the tree against the scan.
func (s *Scan) Lock() { s.mu.Lock() }

// Unlock unlocks the tree.
func (s *Scan) Unlock() { s.mu.Unlock() }

// Done reports whether the scan has finished or was cancelled.
func (s *Scan) Done() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// Wait waits until the scan has finished or was cancelled.
func (s *Scan) Wait() { <-s.done }

// Cancel stops the scan and waits until it no longer writes the tree.
func (s *Scan) Cancel() {
	s.cancel()
	s.qmu.Lock()
	s.qcond.Broadcast()
	s.qmu.Unlock()
	s.Wait()
}

// Progress returns the entries scanned so far and the time taken, which
// stops growing once the scan is done.
func (s *Scan) Progress() (int64, time.Duration) {
	if s.Done() {
		return s.scanned.Load(), time.Duration(s.elapsed.Load())
	}
	return s.scanned.Load(), time.Since(s.started)
}

// Current returns the path, relative to the roots, of the directory or
// file the scan most recently started reading, or "" once done.
func (s *Scan) Current() string {
	if p := s.current.Load(); p != nil && !s.Done() {
		return *p
	}
	return ""
}

func (s *Scan) run() {
	var wg sync.WaitGroup
	for range s.opts.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.work()
		}()
	}
	wg.Wait()
	s.mu.Lock()
	Aggregate(s.Root)
	s.mu.Unlock()
	s.elapsed.Store(int64(time.Since(s.started)))
	close(s.done)
	s.notify(true)
}

func (s *Scan) push(n *Node) {
	s.qmu.Lock()
	s.queue = append(s.queue, n)
	s.pending++
	s.qcond.Signal()
	s.qmu.Unlock()
}

func (s *Scan) work() {
	var comparer contentComparer
	for {
		s.qmu.Lock()
		for len(s.queue) == 0 && s.pending > 0 && s.ctx.Err() == nil {
			s.qcond.Wait()
		}
		if s.pending == 0 || s.ctx.Err() != nil {
			s.qcond.Broadcast()
			s.qmu.Unlock()
			return
		}
		n := s.queue[len(s.queue)-1]
		s.queue = s.queue[:len(s.queue)-1]
		s.qmu.Unlock()

		p := n.Path()
		s.current.Store(&p)
		if n.IsDir() || n.Parent == nil {
			s.list(n)
		} else {
			s.compareFiles(n, &comparer)
		}

		s.qmu.Lock()
		s.pending--
		if s.pending == 0 {
			s.qcond.Broadcast()
		}
		s.qmu.Unlock()
	}
}

// root returns the left (false) or right (true) root.
func (s *Scan) root(right bool) source.Root {
	if right {
		return s.right
	}
	return s.left
}

// childPath returns the path of the entry name in the directory at dir,
// both relative to the roots.
func childPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// entry is a pair of directory entries while pairing.
type entry struct {
	l, r     Side
	inL, inR bool
}

// list reads both sides of a directory node, pairs the entries, and queues
// the pairs that need more work.
func (s *Scan) list(n *Node) {
	entries := map[string]*entry{}
	var emu sync.Mutex
	var lErr, rErr error
	var wg sync.WaitGroup
	dir := n.Path()
	read := func(right bool, errp *error) {
		defer wg.Done()
		side := n.Side(right)
		if side.Kind != Dir {
			return
		}
		root := s.root(right)
		names, err := root.ReadDir(dir)
		*errp = files.UnwrapPath(err)
		for _, name := range names {
			if s.excluded(name) {
				continue
			}
			sd := statSide(root, childPath(dir, name), false)
			emu.Lock()
			e := entries[name]
			if e == nil {
				e = &entry{}
				entries[name] = e
				s.scanned.Add(1)
			}
			if right {
				e.r, e.inR = sd, true
			} else {
				e.l, e.inL = sd, true
			}
			emu.Unlock()
		}
	}
	wg.Add(2)
	go read(false, &lErr)
	go read(true, &rErr)
	wg.Wait()

	// A name missing from a side that could not be listed may be there:
	// that side is unknown, not missing, as is everything below it.
	lUnlisted, rUnlisted := unlisted(&n.L, lErr), unlisted(&n.R, rErr)
	children := make([]*Node, 0, len(entries))
	for name, e := range entries {
		c := &Node{Name: name, Parent: n, L: e.l, R: e.r}
		if !e.inL && lUnlisted != nil {
			c.L = Side{Kind: Unknown, Err: lUnlisted}
		}
		if !e.inR && rUnlisted != nil {
			c.R = Side{Kind: Unknown, Err: rUnlisted}
		}
		children = append(children, c)
	}
	sortNodes(children)
	for _, c := range children {
		classify(c, s.opts)
	}
	s.mu.Lock()
	n.Children = children
	countEntries(n)
	if lErr != nil {
		n.L.Err = lErr
	}
	if rErr != nil {
		n.R.Err = rErr
	}
	s.mu.Unlock()

	for _, c := range children {
		if c.IsDir() || c.State == Pending {
			s.push(c)
		}
	}
	s.notify(false)
}

// NotListedError is the error of a side whose directory could not be
// listed, so whether the entry is there is unknown.
type NotListedError struct{ Err error }

func (e NotListedError) Error() string {
	return "failed listing its directory (" + e.Err.Error() + ")"
}

func (e NotListedError) Unwrap() error { return e.Err }

// unlisted returns the error to give the entries of a directory side that
// could not be listed, because listing it failed with err, or because the
// side itself is unknown for that reason; or nil.
func unlisted(sd *Side, err error) error {
	if err != nil {
		return NotListedError{err}
	}
	var nl NotListedError
	if sd.Kind == Unknown && errors.As(sd.Err, &nl) {
		return nl
	}
	return nil
}

// countEntries sets the entry counts of a directory node's sides.
func countEntries(n *Node) {
	n.L.Count, n.R.Count = 0, 0
	listed := func(sd *Side) bool {
		var nl NotListedError
		return sd.Kind != Missing && !errors.As(sd.Err, &nl)
	}
	for _, c := range n.Children {
		if listed(&c.L) {
			n.L.Count++
		}
		if listed(&c.R) {
			n.R.Count++
		}
	}
}

func (s *Scan) excluded(name string) bool {
	for _, p := range s.opts.Excludes {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

// sortNodes orders directories first, then everything else, each by name
// byte for byte, independent of the locale.
func sortNodes(nodes []*Node) {
	slices.SortFunc(nodes, func(a, b *Node) int {
		if a.IsDir() != b.IsDir() {
			if a.IsDir() {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// statSide reads the entry at rel in root without following a symlink,
// or following it for a root.
func statSide(root source.Root, rel string, follow bool) Side {
	e, err := root.Entry(rel, follow)
	if e.Mode == 0 {
		if errors.Is(err, unix.ENOENT) {
			return Side{Kind: Missing}
		}
		return Side{Kind: Unknown, Err: files.UnwrapPath(err)}
	}
	sd := Side{
		Size:  e.Size,
		Mtime: e.Mtime,
		Rdev:  e.Rdev,
		Perm:  e.Mode & 0o7777,
		Uid:   e.Uid,
		Gid:   e.Gid,
	}
	switch e.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		sd.Kind, sd.Size = Dir, 0
	case unix.S_IFREG:
		sd.Kind = File
	case unix.S_IFLNK:
		sd.Kind = Symlink
		sd.Target, sd.Err = e.Target, files.UnwrapPath(err)
	case unix.S_IFIFO:
		sd.Kind = FIFO
	case unix.S_IFSOCK:
		sd.Kind = Socket
	case unix.S_IFCHR:
		sd.Kind = CharDev
	case unix.S_IFBLK:
		sd.Kind = BlockDev
	default:
		sd.Kind = Unknown
	}
	return sd
}

// attrsDiffer returns the attributes in set that every type has, such as
// the mode, that differ between two sides of the same type.
func attrsDiffer(l, r *Side, set Attrs) Attrs {
	if l.Kind != r.Kind || l.Kind == Missing || l.Kind == Unknown {
		return 0
	}
	var diff Attrs
	if l.Perm != r.Perm {
		diff |= Mode
	}
	if l.Uid != r.Uid || l.Gid != r.Gid {
		diff |= Owner
	}
	if !l.Mtime.Equal(r.Mtime) {
		diff |= Mtime
	}
	return diff & set
}

// classify sets the state of a pair from what was read of it, or leaves
// it Pending if the contents must be compared, or for a directory, whose
// state comes from its entries and Own (see Aggregate).
func classify(n *Node, opts Options) {
	l, r := &n.L, &n.R
	set := opts.Compare
	n.Own = attrsDiffer(l, r, set)
	n.Data = Pending
	switch {
	case l.Kind == Missing && r.Kind == Missing:
		n.State = Same
		return
	case l.Kind == Missing:
		n.State = ifCompared(set&Right, RightOnly)
		return
	case r.Kind == Missing:
		n.State = ifCompared(set&Left, LeftOnly)
		return
	case l.Kind == Unknown || r.Kind == Unknown:
		n.State = Unverified
		return
	case l.Kind != r.Kind:
		n.State = ifCompared(set&Type, TypeDiff)
		return
	case l.Kind == Dir:
		n.State = Pending
		return
	}
	data := Same
	switch l.Kind {
	case Symlink:
		switch {
		case set&Target == 0 || l.Err == nil && r.Err == nil && l.Target == r.Target:
		case l.Err != nil || r.Err != nil:
			data = Unverified
		default:
			data = Differ
		}
	case CharDev, BlockDev:
		if set&Device != 0 && l.Rdev != r.Rdev {
			data = Differ
		}
	case File:
		switch {
		case set&Content == 0:
		case l.Size >= 0 && r.Size >= 0 && l.Size != r.Size:
			data = Differ
		case opts.Quick && l.Mtime.Equal(r.Mtime):
		case opts.Quick:
			data = Unverified
		default:
			// Pending until compared.
			data = n.content
		}
	}
	settle(n, data)
}

// ifCompared returns state if attr is set, or else Ignored.
func ifCompared(attr Attrs, state State) State {
	if attr == 0 {
		return Ignored
	}
	return state
}

// settle sets the state of a pair that isn't a directory from the result
// of comparing its data: a known difference in its attributes makes it
// differ, whether its data could be compared or not, but one still to be
// compared keeps it Pending.
func settle(n *Node, data State) {
	n.Data = data
	if data == Differ {
		switch n.L.Kind {
		case File:
			n.Own |= Content
		case Symlink:
			n.Own |= Target
		default:
			n.Own |= Device
		}
	}
	switch {
	case data == Pending:
		n.State = Pending
	case n.Own != 0:
		n.State = Differ
	default:
		n.State = data
	}
}

// compareFiles compares the contents of a pair of regular files of the
// same size.
func (s *Scan) compareFiles(n *Node, comparer *contentComparer) {
	state, lErr, rErr := comparer.compareContents(s.ctx, s.left, s.right, n.Path())
	s.mu.Lock()
	n.content = state
	settle(n, state)
	if lErr != nil {
		n.L.Err = lErr
	}
	if rErr != nil {
		n.R.Err = rErr
	}
	s.mu.Unlock()
	s.notify(false)
}

// blockSize is how much of each file is read at a time.
const blockSize = 256 << 10

// contentComparer reuses its buffers across file pairs. Each worker owns one,
// allocating only when it first reads contents; it must not be shared by
// concurrent comparisons.
type contentComparer struct {
	buf []byte
}

// compareContents reads the files at rel in left and right in blocks,
// stopping at the first difference. Only regular files are read: opening
// anything else could block or have side effects. Files whose roots name
// them by IDs of the same scheme are compared by those, without reading.
func (c *contentComparer) compareContents(ctx context.Context, left, right source.Root, rel string) (State, error, error) {
	if state, ok := compareIDs(left, right, rel); ok {
		return state, nil, nil
	}
	lf, lErr := left.Open(rel, false)
	rf, rErr := right.Open(rel, false)
	lErr, rErr = files.UnwrapPath(lErr), files.UnwrapPath(rErr)
	defer func() {
		if lf != nil {
			_ = lf.Close()
		}
		if rf != nil {
			_ = rf.Close()
		}
	}()
	if lErr != nil || rErr != nil {
		return Unverified, lErr, rErr
	}
	if c.buf == nil {
		c.buf = make([]byte, 2*blockSize)
	}
	lb, rb := c.buf[:blockSize], c.buf[blockSize:]
	for {
		if ctx.Err() != nil {
			return Pending, nil, nil
		}
		ln, lErr := readFull(lf, lb)
		rn, rErr := readFull(rf, rb)
		if lErr != nil || rErr != nil {
			return Unverified, lErr, rErr
		}
		if ln != rn || string(lb[:ln]) != string(rb[:rn]) {
			return Differ, nil, nil
		}
		if ln < blockSize {
			return Same, nil, nil
		}
	}
}

// compareIDs compares the files at rel in left and right by their content
// IDs, if both have IDs of the same scheme.
func compareIDs(left, right source.Root, rel string) (State, bool) {
	l, ok := left.(source.ContentIDs)
	if !ok {
		return Pending, false
	}
	r, ok := right.(source.ContentIDs)
	if !ok {
		return Pending, false
	}
	lID, lok := l.ContentID(rel)
	rID, rok := r.ContentID(rel)
	if !lok || !rok || lID.Scheme != rID.Scheme {
		return Pending, false
	}
	if lID.Sum == rID.Sum {
		return Same, true
	}
	return Differ, true
}

// readFull reads until buf is full or the file ends.
func readFull(f io.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := f.Read(buf[n:])
		n += m
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, files.UnwrapPath(err)
		}
		if m == 0 {
			return n, nil
		}
	}
	return n, nil
}

// Aggregate sets the state of every directory pair below and including n
// from its own attributes and its entries: different if the attributes
// compared or any entry differs, else unverified if any
// entry is unverified or a side could not be listed, else pending if any
// entry is, else the same.
func Aggregate(n *Node) State {
	if !n.IsDir() && n.Parent != nil {
		return n.State
	}
	childStates := make([]State, len(n.Children))
	for i, c := range n.Children {
		childStates[i] = Aggregate(c)
	}
	if n.Parent != nil && (n.L.Kind != Dir || n.R.Kind != Dir) {
		// One-sided, or opposite another type: classify decided.
		return n.State
	}
	state := Same
	switch {
	case n.Own != 0:
		state = Differ
	case n.L.Err != nil || n.R.Err != nil:
		state = Unverified
	}
	for _, cs := range childStates {
		switch {
		case cs.IsDiff():
			state = Differ
		case cs == Unverified && state != Differ:
			state = Unverified
		case cs == Pending && state == Same:
			state = Pending
		}
	}
	n.State = state
	return state
}

// Options returns the options the tree was compared with, Compare set.
func (s *Scan) Options() Options {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opts
}

// Reclassify compares the whole tree again with opts' Compare and Quick,
// from what the scan read, without reading anything: at once. It reports
// whether some files' contents must be read for it, which only a new scan
// does; those are left Pending. The scan must be done.
func (s *Scan) Reclassify(opts Options) (unread bool) {
	s.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if opts.Compare == 0 {
		opts.Compare = DefaultAttrs
	}
	s.opts.Compare, s.opts.Quick = opts.Compare, opts.Quick
	s.Root.Own = attrsDiffer(&s.Root.L, &s.Root.R, s.opts.Compare)
	var walk func(*Node)
	walk = func(n *Node) {
		for _, c := range n.Children {
			classify(c, s.opts)
			if c.State == Pending && !c.IsDir() {
				unread = true
			}
			walk(c)
		}
	}
	walk(s.Root)
	Aggregate(s.Root)
	return unread
}

// Recheck reads and compares the entry at p again, as after an edit, and
// updates the state of its parent directories. It waits for a running scan
// to finish first, so the two never update the same entry. The entry is
// read without holding the lock, so the tree can be drawn meanwhile, and
// Cancel stops the reading. An entry now missing on both sides is removed.
// Only one Recheck may run at a time.
func (s *Scan) Recheck(p string) {
	s.Wait()
	s.mu.Lock()
	n := s.Root.Find(p)
	opts := s.opts
	s.mu.Unlock()
	if n == nil || n.Parent == nil {
		return
	}
	var comparer contentComparer
	for {
		probe := &Node{Name: n.Name, Parent: n.Parent, L: statSide(s.left, p, false), R: statSide(s.right, p, false)}
		classify(probe, opts)
		content, lErr, rErr := Pending, error(nil), error(nil)
		if probe.State == Pending && !probe.IsDir() {
			content, lErr, rErr = comparer.compareContents(s.ctx, s.left, s.right, p)
		}

		s.mu.Lock()
		// The options may have changed while reading, as by Reclassify, and
		// with them what must be read, such as contents quick mode skipped:
		// read again with the new ones.
		if s.opts.Compare != opts.Compare || s.opts.Quick != opts.Quick {
			opts = s.opts
			s.mu.Unlock()
			if s.ctx.Err() != nil {
				return
			}
			continue
		}
		n.L, n.R, n.content = probe.L, probe.R, content
		classify(n, s.opts)
		n.L.Err, n.R.Err = errors.Join(n.L.Err, lErr), errors.Join(n.R.Err, rErr)
		break
	}
	defer s.mu.Unlock()
	if n.L.Kind == Missing && n.R.Kind == Missing {
		n.Parent.Children = slices.DeleteFunc(n.Parent.Children, func(c *Node) bool { return c == n })
	}
	countEntries(n.Parent)
	Aggregate(s.Root)
}
