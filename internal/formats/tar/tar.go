// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package tar reads a tar archive as a side of the comparison. The
// archive is read once, as a stream, which may be a pipe: its entries are
// kept in memory, and the contents of its files in a blob, a temporary
// file of their contents one after another, which the root reads from
// then. Changes to the archive after it is read are not seen.
package tar

import (
	stdtar "archive/tar"
	"bufio"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/formats/archive"
)

// Read reads the tar archive in r, compressed or not, as a root shown as
// name. The contents of its files are written to blob, a new, empty file,
// which the root then owns and Close closes. If p is set, Read reports to
// it how far it has got.
func Read(r io.Reader, blob *os.File, name string, p *archive.Progress) (*archive.Root, error) {
	if p == nil {
		p = new(archive.Progress)
	}
	root := archive.New(name, "tar", blob, archive.FollowedByTar)
	if err := readArchive(root, p.Count(r), p); err != nil {
		return nil, err
	}
	return root, nil
}

// magic are the starts of streams compressed by each format, as Linux
// tells them (lib/decompress.c): those read, and those that aren't, named
// for the error.
var magic = []struct {
	is     func([]byte) bool
	format string
}{
	{prefix("\x1f\x8b"), "gzip"},
	{prefix("\x1f\x9e"), "gzip's old format"},
	{bzip2Start, "bzip2"},
	{prefix("\xfd7zXZ\x00"), "xz"},
	{prefix("\x28\xb5\x2f\xfd"), "zstd"},
	{lzmaStart, "lzma"},
	{prefix("\x04\x22\x4d\x18"), "lz4"},
	{prefix("\x02\x21\x4c\x18"), "lz4's legacy format"},
	{prefix("\x89LZO"), "lzop"},
	{prefix("\x1f\x9d"), "compress"},
}

// prefix returns whether a stream starts with start.
func prefix(start string) func([]byte) bool {
	return func(b []byte) bool { return bytes.HasPrefix(b, []byte(start)) }
}

// lzmaStart reports whether b starts with an lzma header, which has no
// magic, as xz tells one: properties no greater than the largest, a
// dictionary of 2ⁿ or 2ⁿ + 2ⁿ⁻¹ bytes, or the largest, and a size that is
// unknown or less than 256 GiB.
func lzmaStart(b []byte) bool {
	if len(b) < 13 || b[0] > (4*5+4)*9+8 {
		return false
	}
	dict := binary.LittleEndian.Uint32(b[1:5])
	d := dict - 1
	d |= d >> 2
	d |= d >> 3
	d |= d >> 4
	d |= d >> 8
	d |= d >> 16
	if d+1 != dict && dict != math.MaxUint32 {
		return false
	}
	size := binary.LittleEndian.Uint64(b[5:13])
	return size == math.MaxUint64 || size < 1<<38
}

// skippable reports whether b starts with a skippable frame, which zstd
// and lz4 both allow before a frame, holding nothing to decompress.
func skippable(b []byte) bool {
	return len(b) >= 8 && b[0]&0xf0 == 0x50 && string(b[1:4]) == "\x2a\x4d\x18"
}

// skipFrames discards the skippable frames r starts with, of any size,
// and reports whether there were any.
func skipFrames(r *bufio.Reader) (bool, error) {
	skipped := false
	for {
		b, err := r.Peek(8)
		if err != nil && err != io.EOF {
			return skipped, err
		}
		if !skippable(b) {
			return skipped, nil
		}
		n := int(binary.LittleEndian.Uint32(b[4:8]))
		if _, err := r.Discard(8 + n); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return skipped, err
		}
		skipped = true
	}
}

// bzip2Start reports whether b starts as a bzip2 stream does: "BZh", the
// block size, from 1 to 9, and the magic of a block or, if empty, of the
// end, rather than just "BZh", which much else starts with.
func bzip2Start(b []byte) bool {
	if len(b) < 10 || !bytes.HasPrefix(b, []byte("BZh")) || b[3] < '1' || b[3] > '9' {
		return false
	}
	m := string(b[4:10])
	return m == "1AY&SY" || m == "\x17rE8P\x90"
}

// decompress returns r decompressed if it starts as gzip, bzip2, xz, lzma,
// zstd or lz4 does, after any skippable frames, and the format, or as it
// is and "". A tar header,
// which starts with an entry's name, as a file named BZhello.txt, or tar's
// end is never taken for compression.
func decompress(r *bufio.Reader) (io.ReadCloser, string, error) {
	// What is peeked at may be short, of a short stream, but an error
	// reading is given once.
	start, err := r.Peek(blockSize)
	if err != nil && err != io.EOF {
		return nil, "", err
	}
	if isHeader(start) || isEnd(start) {
		return io.NopCloser(r), "", nil
	}
	skipped, err := skipFrames(r)
	if err != nil {
		return nil, "", err
	}
	if skipped {
		if start, err = r.Peek(blockSize); err != nil && err != io.EOF {
			return nil, "", err
		}
	}
	format := compression(start)
	if skipped && format != "zstd" && format != "lz4" && format != "lz4's legacy format" {
		return nil, "", errors.New("skippable frames, as of zstd or lz4, followed by neither")
	}
	switch format {
	case "":
		return io.NopCloser(r), "", nil
	case "gzip":
		z, err := gzip.NewReader(r)
		if err != nil {
			return nil, "", err
		}
		z.Multistream(false)
		return &gzipMembers{z: z, r: r}, format, nil
	case "bzip2":
		return io.NopCloser(newBzip2Streams(r)), format, nil
	case "xz":
		z, err := xz.NewReader(r)
		return io.NopCloser(z), format, err
	case "lzma":
		z, err := lzma.NewReader(r)
		return io.NopCloser(z), format, err
	case "zstd":
		z, err := zstd.NewReader(r)
		if err != nil {
			return nil, "", err
		}
		return z.IOReadCloser(), format, nil
	case "lz4", "lz4's legacy format":
		return io.NopCloser(lz4.NewReader(r)), format, nil
	}
	return nil, "", fmt.Errorf("compressed with %s, which binate can't read: only with gzip, bzip2, xz, lzma, zstd or lz4", format)
}

// gzipMembers reads the members of a gzip stream one after another, as
// gzip does, and ignores zeros after the last, as a tape's blocks are
// padded with, as gzip ignores them. Anything else after it is an error,
// as gzip's makes tar fail.
type gzipMembers struct {
	z *gzip.Reader
	r *bufio.Reader
	// end is how the stream ended, io.EOF or an error, once it has: given
	// again, as one given with data may be dropped.
	end error
}

func (g *gzipMembers) Read(b []byte) (int, error) {
	if g.end != nil {
		return 0, g.end
	}
	for {
		n, err := g.z.Read(b)
		if err != io.EOF {
			return n, err
		}
		// An error reading is given once, with what was read before it.
		next, err := g.r.Peek(2)
		switch {
		case err != nil && err != io.EOF:
			g.end = err
		case len(next) == 0:
			g.end = io.EOF
		case bytes.Equal(next, []byte{0x1f, 0x8b}):
			if err := g.z.Reset(g.r); err != nil {
				g.end = err
			}
			g.z.Multistream(false)
		default:
			g.end = io.EOF
			if err := onlyZeros(g.r); err != nil {
				g.end = err
			}
		}
		if g.end != nil || n > 0 {
			return n, g.end
		}
	}
}

func (g *gzipMembers) Close() error { return g.z.Close() }

// onlyZeros reads r to its end, failing if it holds anything but zeros.
func onlyZeros(r io.Reader) error {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if slices.ContainsFunc(buf[:n], func(c byte) bool { return c != 0 }) {
			return errors.New("data after the gzip stream")
		}
		switch {
		case err == io.EOF:
			return nil
		case err != nil:
			return err
		}
	}
}

// bzip2Streams reads a bzip2 stream as compress/bzip2 does, the streams
// in it one after another, but ignores what follows the last that doesn't
// start as a stream does, with "BZh" and a block size, as bzip2 ignores
// it, with a warning, rather than fail on it.
type bzip2Streams struct {
	z io.Reader
	r *tailReader
	// end is how the stream ended, once it has (see gzipMembers).
	end error
}

func newBzip2Streams(r *bufio.Reader) *bzip2Streams {
	t := &tailReader{r: r}
	return &bzip2Streams{z: bzip2.NewReader(t), r: t}
}

func (s *bzip2Streams) Read(b []byte) (int, error) {
	if s.end != nil {
		return 0, s.end
	}
	n, err := s.z.Read(b)
	if err == nil {
		return n, nil
	}
	s.end = err
	if s.afterLast(err) {
		// Read to the end, for a pipe's writer not to fail.
		s.end = io.EOF
		if _, err := io.Copy(io.Discard, s.r.r); err != nil {
			s.end = err
		}
	}
	return n, s.end
}

// afterLast reports whether err is compress/bzip2's of what follows a
// stream not starting as one does: other than "BZ", "BZ" and other than
// "h", or "BZh" and other than a block size, which it reads only there,
// or the end after a byte other than "B", where the stream before it
// ends. The end after "B", "BZ" or "BZh" is the end of a stream cut short,
// as bzip2 takes it.
func (s *bzip2Streams) afterLast(err error) bool {
	var serr bzip2.StructuralError
	if errors.As(err, &serr) {
		switch serr {
		case "bad magic value in continuation file", "non-Huffman entropy encoding", "invalid compression level":
			return true
		}
	}
	return errors.Is(err, io.ErrUnexpectedEOF) && s.r.tail[len(s.r.tail)-1] != 'B' && s.r.streamEnded()
}

// tailReader reads r a byte at a time, as compress/bzip2 does, keeping
// the last bytes read.
type tailReader struct {
	r    *bufio.Reader
	tail [12]byte
	n    int
}

func (t *tailReader) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	c, err := t.ReadByte()
	if err != nil {
		return 0, err
	}
	b[0] = c
	return 1, nil
}

func (t *tailReader) ReadByte() (byte, error) {
	c, err := t.r.ReadByte()
	if err == nil {
		copy(t.tail[:], t.tail[1:])
		t.tail[len(t.tail)-1] = c
		t.n++
	}
	return c, err
}

// streamEnded reports whether the bytes read but the last end a bzip2
// stream: with its end's magic, its checksum, and up to 7 bits to a
// byte's end, after which compress/bzip2 reads the next stream's.
func (t *tailReader) streamEnded() bool {
	if t.n < len(t.tail) {
		return false
	}
	// The 88 bits before the last byte, the magic's 48 ending 32 + pad
	// bits before their end.
	b := t.tail[:len(t.tail)-1]
	const endMagic = 0x177245385090
	for pad := range 8 {
		var v uint64
		for i := len(b)*8 - 32 - pad - 48; i < len(b)*8-32-pad; i++ {
			v = v<<1 | uint64(b[i/8]>>(7-i%8)&1)
		}
		if v == endMagic {
			return true
		}
	}
	return false
}

// compression returns the format of the compressed stream b starts, or
// "" for none.
func compression(b []byte) string {
	for _, m := range magic {
		if m.is(b) {
			return m.format
		}
	}
	return ""
}

// blockSize is the size of a tar header, and of the blocks of an archive.
const blockSize = 512

// isEnd reports whether b starts with a block of zeros, as tar's end
// does, of an archive of nothing, which lzma's header, without magic,
// could otherwise be taken for.
func isEnd(b []byte) bool {
	return len(b) >= blockSize && bytes.Equal(b[:blockSize], make([]byte, blockSize))
}

// isHeader reports whether b starts with a tar header: a block whose
// checksum, the sum of its bytes with the checksum's own as spaces, is
// right, as unsigned or signed bytes, as old tars summed them.
func isHeader(b []byte) bool {
	if len(b) < blockSize {
		return false
	}
	const at, size = 148, 8
	want, err := strconv.ParseUint(strings.Trim(string(b[at:at+size]), " \x00"), 8, 64)
	if err != nil {
		return false
	}
	var sum uint64
	var signed int64
	for i, c := range b[:blockSize] {
		if i >= at && i < at+size {
			c = ' '
		}
		sum += uint64(c)
		signed += int64(int8(c))
	}
	return want == sum || int64(want) == signed
}

func readArchive(r *archive.Root, in io.Reader, p *archive.Progress) error {
	z, format, err := decompress(bufio.NewReaderSize(in, 1<<16))
	if err != nil {
		return notTar(err)
	}
	// As zstd's, which decodes in goroutines until closed.
	defer func() { _ = z.Close() }()
	// What is decompressed is counted, to tell nothing from an archive of
	// nothing, which still has its end.
	var size atomic.Int64
	rd := bufio.NewReaderSize(archive.Count(z, &size), blockSize)
	if err := checkStart(rd, format); err != nil {
		return err
	}
	tr := stdtar.NewReader(rd)
	// The records of global PAX headers so far, which apply to the entries
	// after them.
	globals := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		// With GODEBUG=tarinsecurepath=0, archive/tar gives a name with a
		// leading "/" or ".." with an error, and the header, which is read
		// below as tar reads it.
		if err != nil && (hdr == nil || !errors.Is(err, stdtar.ErrInsecurePath)) {
			return notTar(err)
		}
		if hdr.Typeflag == stdtar.TypeXGlobalHeader {
			for k, v := range hdr.PAXRecords {
				if v == "" {
					// An empty value removes the record.
					delete(globals, k)
				} else {
					globals[k] = v
				}
			}
			continue
		}
		withGlobals(hdr, globals)
		rel, inside := archive.Clean(hdr.Name)
		if !inside {
			rel, _ = archive.Clean(afterDotDot(hdr.Name))
		}
		p.Begin(r.Path(rel))
		e, ok := newEntry(hdr)
		if !ok {
			// Not a file, as a volume label.
			continue
		}
		if !inside {
			// GNU tar refuses it, naming it without all up to its last "..":
			// it is shown there, unless something else is.
			e.Err = fmt.Errorf("the name %q has \"..\" in it, which tar refuses", hdr.Name)
			r.Mark(rel, e)
			continue
		}
		if hdr.Typeflag == typeMultiVolume {
			e.Err = errors.New("continued from another volume, which tar refuses")
			r.Mark(rel, e)
			continue
		}
		if hdr.Typeflag == stdtar.TypeSymlink && hdr.Linkname == "" {
			// No symlink can lead to "", as symlink(2) refuses it, and tar
			// makes nothing, leaving what is there.
			continue
		}
		if !r.Puts(rel, e) {
			// Left out for what is there, as tar fails to replace it: its
			// contents are not stored.
			continue
		}
		switch {
		case hdr.Typeflag == stdtar.TypeLink:
			target, ok := archive.Clean(hdr.Linkname)
			if !ok {
				// GNU tar links to it without all up to its last "..".
				target, _ = archive.Clean(afterDotDot(hdr.Linkname))
			}
			r.HardLink(e, target)
		case e.Stat.Mode&unix.S_IFMT == unix.S_IFREG:
			if err := r.Store(e, tr); err != nil {
				return notTar(err)
			}
		}
		r.Put(rel, e)
	}
	// Read to the end, as a compressed stream's checksum is, and for a
	// pipe's writer not to fail writing what is left.
	if _, err := io.Copy(io.Discard, rd); err != nil {
		return notTar(err)
	}
	if size.Load() == 0 {
		// As from a command that failed before writing anything, which an
		// empty tree would hide.
		return errors.New("empty, not a tar archive")
	}
	return r.Finish()
}

// checkStart refuses what r, decompressed from format, or "" if plain,
// starts with if it isn't a tar archive's start: a header, or its end, or
// of a short one, a header's start with ustar's magic, taken for one cut
// short. Otherwise the tar reader would take anything shorter than a
// header for an archive cut short, and not name the compression.
func checkStart(r *bufio.Reader, format string) error {
	start, err := r.Peek(blockSize)
	switch {
	case err != nil && err != io.EOF:
		return notTar(err)
	case len(start) == 0 || isHeader(start) || isEnd(start):
		// Nothing is refused as empty once read.
		return nil
	case len(start) < blockSize && len(start) >= magicAt+len("ustar") && string(start[magicAt:magicAt+len("ustar")]) == "ustar":
		return errors.New("the archive ends early")
	case format != "":
		return fmt.Errorf("compressed with %s, but not a tar archive", format)
	case len(start) < blockSize:
		return errors.New("not a tar archive, shorter than a header")
	}
	return errors.New("not a tar archive, or a damaged one")
}

// magicAt is where in a header ustar's magic is.
const magicAt = 257

// afterDotDot returns name without all up to and including its last
// "..", as GNU tar strips it.
func afterDotDot(name string) string {
	parts := strings.Split(name, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == ".." {
			return strings.Join(parts[i+1:], "/")
		}
	}
	return name
}

// globalKeys are the records of global PAX headers applied to the
// entries after them, as GNU tar extracts them: their owners and times.
var globalKeys = []string{"uid", "gid", "uname", "gname", "mtime", "atime", "ctime"}

// withGlobals sets the records globals holds in hdr where its own PAX
// records don't, as archive/tar leaves them in the global header. They
// parse, as archive/tar drops a global header with one that doesn't.
func withGlobals(hdr *stdtar.Header, globals map[string]string) {
	for _, k := range globalKeys {
		v, ok := globals[k]
		if !ok {
			continue
		}
		if _, own := hdr.PAXRecords[k]; own {
			continue
		}
		if hdr.PAXRecords == nil {
			hdr.PAXRecords = map[string]string{}
		}
		hdr.PAXRecords[k] = v
		switch k {
		case "uid":
			hdr.Uid = paxID(v)
		case "gid":
			hdr.Gid = paxID(v)
		case "uname":
			hdr.Uname = v
		case "gname":
			hdr.Gname = v
		case "mtime":
			hdr.ModTime, _ = paxTime(v)
		case "atime":
			hdr.AccessTime, _ = paxTime(v)
		case "ctime":
			hdr.ChangeTime, _ = paxTime(v)
		}
	}
}

// paxID parses a PAX record's user or group ID, as archive/tar does.
func paxID(v string) int {
	id, _ := strconv.ParseInt(v, 10, 64)
	return int(id)
}

// paxTime parses a PAX record's time, as archive/tar does: seconds since
// the epoch, maybe negative, and maybe a fraction, of which nanoseconds
// are kept.
func paxTime(v string) (time.Time, error) {
	secs, frac, _ := strings.Cut(v, ".")
	s, err := strconv.ParseInt(secs, 10, 64)
	if err != nil || strings.Trim(frac, "0123456789") != "" {
		return time.Time{}, errors.New("not a time")
	}
	frac = (frac + "000000000")[:9]
	ns, _ := strconv.ParseInt(frac, 10, 64)
	if strings.HasPrefix(secs, "-") {
		ns = -ns
	}
	return time.Unix(s, ns), nil
}

// notTar returns err of reading the archive, as it is or, for a header
// that isn't a tar header, saying so.
func notTar(err error) error {
	switch {
	case errors.Is(err, stdtar.ErrHeader):
		return errors.New("not a tar archive, or a damaged one")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("the archive ends early")
	}
	return err
}

// Types of entries that archive/tar doesn't name, as GNU tar reads them.
const (
	// typeVolume is a GNU volume label.
	typeVolume = 'V'
	// typeMultiVolume is a file continued from another volume, which GNU
	// tar refuses to extract.
	typeMultiVolume = 'M'
	// typeSolarisExtended is Solaris's extended header, as PAX's.
	typeSolarisExtended = 'X'
)

// newEntry returns the entry hdr describes, false for a type that is no
// file. A hard link is made its target by hardLink.
func newEntry(hdr *stdtar.Header) (*archive.Entry, bool) {
	var typ uint32
	e := &archive.Entry{}
	switch hdr.Typeflag {
	case stdtar.TypeReg, '\x00', stdtar.TypeCont, stdtar.TypeGNUSparse:
		typ = unix.S_IFREG
		e.Stat.Size = hdr.Size
	case stdtar.TypeLink:
		typ = unix.S_IFREG
	case stdtar.TypeSymlink:
		typ, e.Stat.Target = unix.S_IFLNK, hdr.Linkname
		e.Stat.Size = int64(len(hdr.Linkname))
	case stdtar.TypeChar:
		typ = unix.S_IFCHR
	case stdtar.TypeBlock:
		typ = unix.S_IFBLK
	case stdtar.TypeDir, 'D':
		typ = unix.S_IFDIR
	case stdtar.TypeFifo:
		typ = unix.S_IFIFO
	case typeVolume, typeSolarisExtended:
		// A volume label names no file, and GNU tar takes Solaris's
		// extended header for the entry after it, whose records are not
		// applied here.
		return nil, false
	default:
		// An unknown type, which GNU tar extracts as a file, or one
		// continued from another volume, which readArchive marks.
		typ = unix.S_IFREG
		e.Stat.Size = hdr.Size
	}
	e.Stat.Mode = typ | uint32(hdr.Mode)&0o7777
	e.Stat.Uid, e.Stat.Gid = uint32(hdr.Uid), uint32(hdr.Gid)
	e.Stat.User, e.Stat.Group = hdr.Uname, hdr.Gname
	e.Stat.Mtime, e.Stat.Atime, e.Stat.Ctime = hdr.ModTime, hdr.AccessTime, hdr.ChangeTime
	// Only a PAX record holds a fraction of a second. One that does may
	// be written from floating point, as Python's tarfile writes it, some
	// hundred nanoseconds off: it is compared to the microsecond.
	e.Stat.MtimePrecision = time.Second
	if v, ok := hdr.PAXRecords["mtime"]; ok && strings.Contains(v, ".") {
		e.Stat.MtimePrecision = time.Microsecond
	}
	if typ == unix.S_IFCHR || typ == unix.S_IFBLK {
		e.Stat.Rdev = unix.Mkdev(uint32(hdr.Devmajor), uint32(hdr.Devminor))
	}
	return e, true
}
