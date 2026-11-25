// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package zip reads a zip archive as a side of the comparison. Its entries
// are read once, by its central directory, as unzip reads them: they are
// kept in memory, and the contents of its files in a blob, a temporary
// file, which the root reads from then. Changes to the archive after it
// is read are not seen.
package zip

import (
	stdzip "archive/zip"
	"bytes"
	"compress/bzip2"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
	"golang.org/x/sys/unix"
	"golang.org/x/text/encoding/charmap"

	"pxy.se/go/binate/internal/formats/archive"
)

// Read reads the zip archive in r, as a root shown as name. A zip
// archive's directory is at its end: a file is read where its entries
// are, and anything else, as a pipe, is copied to blob first (see
// archive.Root.ReaderAt). The contents of its files are written to blob,
// a new, empty file, which the root then owns and Close closes. If p is
// set, Read reports to it how far it has got.
func Read(r io.Reader, blob *os.File, name string, p *archive.Progress) (*archive.Root, error) {
	if p == nil {
		p = new(archive.Progress)
	}
	root := archive.New(name, "zip", blob, nil)
	root.KeepDirectories()
	ra, size, err := root.ReaderAt(r, p)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		// As from a command that failed before writing anything.
		return nil, errors.New("empty, not a zip archive")
	}
	zr, err := stdzip.NewReader(ra, size)
	if err != nil && !errors.Is(err, stdzip.ErrInsecurePath) {
		return nil, notZip(err)
	}
	for method, open := range decompressors {
		zr.RegisterDecompressor(method, open)
	}
	if err := readEntries(root, zr, p); err != nil {
		return nil, err
	}
	if err := root.Finish(); err != nil {
		return nil, err
	}
	return root, nil
}

// Detect reports whether r is a zip archive by its start: an entry, the
// marker of an archive split into one part, or, of nothing, the end of its
// directory, or that of zip64. An archive after something else, as a
// program extracting it, isn't told.
func Detect(r io.Reader) bool {
	start := make([]byte, 4)
	if _, err := io.ReadFull(r, start); err != nil {
		return false
	}
	switch string(start) {
	case "PK\x03\x04", "PK\x07\x08", "PK\x05\x06", "PK\x06\x06":
		return true
	}
	return false
}

// withoutDotDot returns name without its ".." parts, as unzip drops
// them.
func withoutDotDot(name string) string {
	parts := strings.Split(name, "/")
	return strings.Join(slices.DeleteFunc(parts, func(p string) bool { return p == ".." }), "/")
}

// notZip returns err of reading the archive, as it is or, for an archive
// that isn't a zip archive, saying so.
func notZip(err error) error {
	switch {
	case errors.Is(err, stdzip.ErrFormat):
		return errors.New("not a zip archive, or a damaged one")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("the archive ends early")
	}
	return err
}

// decompressors are the compression methods read beyond store and
// deflate, which archive/zip reads itself: bzip2, zstd and xz. lzma is
// read by openLZMA.
var decompressors = map[uint16]stdzip.Decompressor{
	12: func(r io.Reader) io.ReadCloser { return io.NopCloser(bzip2.NewReader(r)) },
	// zstd's method, and the one APPNOTE first gave it, which older tools
	// write, and Python's zipfile reads too.
	93: zstdDecompressor,
	20: zstdDecompressor,
	95: func(r io.Reader) io.ReadCloser {
		z, err := xz.NewReader(r)
		if err != nil {
			return errReader{err}
		}
		return io.NopCloser(z)
	},
}

func zstdDecompressor(r io.Reader) io.ReadCloser {
	z, err := zstd.NewReader(r)
	if err != nil {
		return errReader{err}
	}
	return z.IOReadCloser()
}

// methods name the compression methods not read, for the error.
var methods = map[uint16]string{
	1: "shrink", 2: "reduce", 3: "reduce", 4: "reduce", 5: "reduce", 6: "implode",
	9: "deflate64", 10: "IBM TERSE", 18: "IBM TERSE", 19: "IBM LZ77",
	96: "JPEG", 97: "WavPack", 98: "PPMd",
}

// errReader fails every read with err.
type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
func (e errReader) Close() error             { return nil }

// Creators, in a file's CreatorVersion, whose external attributes hold
// a Unix mode.
const (
	creatorUnix  = 3
	creatorMacOS = 19
)

// creatorFAT is the creator of MS-DOS and of Windows on FAT, and of many
// tools on Windows.
const creatorFAT = 0

// readEntries puts each entry of zr in root, as unzip extracts it.
func readEntries(root *archive.Root, zr *stdzip.Reader, p *archive.Progress) error {
	var noModes, noOwners bool
	for _, zf := range zr.File {
		name := entryName(zf)
		// unzip drops each "..", and extracts the rest.
		rel, _ := archive.Clean(withoutDotDot(name))
		p.Begin(root.Path(rel))
		e, hasMode, hasOwner := newEntry(zf, name)
		noModes = noModes || !hasMode
		noOwners = noOwners || !hasOwner
		if !root.Puts(rel, e) {
			// Left out for what is there, as unzip fails to replace it:
			// its contents are not stored.
			continue
		}
		switch e.Stat.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			if err := store(root, e, zf); err != nil {
				return err
			}
		case unix.S_IFLNK:
			target, err := readTarget(zf)
			if err != nil {
				e.Err = err
				break
			}
			if target == "" {
				// No symlink can lead to "", as symlink(2) refuses it, and
				// unzip makes nothing, leaving what is there.
				continue
			}
			e.Stat.Target, e.Stat.Size = target, int64(len(target))
		}
		root.Put(rel, e)
	}
	root.SetUnrecorded(noModes, noOwners)
	return nil
}

// store copies the contents of zf, a regular file, to root for e. One
// that can't be read, as it is encrypted, compressed by a method not
// read, or damaged, is kept with why, for its panel, as unzip goes on to
// the next. Only failing to write the blob is returned.
func store(root *archive.Root, e *archive.Entry, zf *stdzip.File) error {
	if zf.Flags&1 != 0 {
		e.Err = errors.New("encrypted, which binate can't read")
		return nil
	}
	rc, err := open(zf)
	if err != nil {
		e.Err = openError(zf, err)
		return nil
	}
	defer func() { _ = rc.Close() }()
	src := &sourceReader{r: rc}
	if err := root.Store(e, src); err != nil {
		if src.err == nil {
			return err
		}
		e.Err = fmt.Errorf("damaged: %w", src.err)
	}
	return nil
}

// openError returns why zf can't be opened.
func openError(zf *stdzip.File, err error) error {
	if errors.Is(err, stdzip.ErrAlgorithm) {
		name, ok := methods[zf.Method]
		if !ok {
			name = fmt.Sprintf("method %d", zf.Method)
		}
		return fmt.Errorf("compressed with %s, which binate can't read", name)
	}
	return fmt.Errorf("damaged: %w", err)
}

// open opens zf's contents, a file's or a symlink's target.
func open(zf *stdzip.File) (io.ReadCloser, error) {
	if zf.Method == methodLZMA {
		return openLZMA(zf)
	}
	rc, err := zf.Open()
	if err != nil || zf.CRC32 != 0 || zf.Flags&dataDescriptorFlag != 0 {
		return rc, err
	}
	// archive/zip takes a checksum of 0 for none, where no data
	// descriptor follows, and checks nothing, but unzip checks it.
	return &crcReader{r: rc, want: 0, crc: crc32.NewIEEE(), closer: rc}, nil
}

// dataDescriptorFlag is the general purpose flag of a file whose sizes
// and checksum follow its contents.
const dataDescriptorFlag = 0x8

// methodLZMA is the lzma compression method.
const methodLZMA = 14

// openLZMA opens zf, compressed with lzma. archive/zip can't read it, as
// it hands a decompressor no size, and the end of an lzma stream may be
// known only by the size of the file. The stream follows a header of a
// version and the size of the lzma properties, which come next.
func openLZMA(zf *stdzip.File) (io.ReadCloser, error) {
	raw, err := zf.OpenRaw()
	if err != nil {
		return nil, err
	}
	var h [4 + 5]byte
	if _, err := io.ReadFull(raw, h[:]); err != nil {
		return nil, fmt.Errorf("lzma header: %w", err)
	}
	if n := binary.LittleEndian.Uint16(h[2:]); n != 5 {
		return nil, fmt.Errorf("lzma properties of %d bytes, not 5", n)
	}
	// The classic header, of the properties and the size, which also
	// bounds the dictionary however large the properties make it. The
	// stream may end with an end marker anyway.
	classic := make([]byte, lzma.HeaderLen)
	copy(classic, h[4:])
	binary.LittleEndian.PutUint64(classic[5:], zf.UncompressedSize64)
	c := lzma.ReaderConfig{DictCap: lzma.MaxDictCap}
	r, err := c.NewReader(io.MultiReader(bytes.NewReader(classic), raw))
	if err != nil {
		return nil, err
	}
	return &crcReader{r: r, want: zf.CRC32, crc: crc32.NewIEEE()}, nil
}

// crcReader fails reading r at its end if what was read doesn't match
// the checksum want, as archive/zip does for what it opens. Close closes
// closer, if set.
type crcReader struct {
	r      io.Reader
	want   uint32
	crc    hash.Hash32
	closer io.Closer
}

func (c *crcReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.crc.Write(b[:n])
	if err == io.EOF && c.crc.Sum32() != c.want {
		err = stdzip.ErrChecksum
	}
	return n, err
}

func (c *crcReader) Close() error {
	if c.closer == nil {
		return nil
	}
	return c.closer.Close()
}

// sourceReader keeps an error reading r, to tell it from one writing.
type sourceReader struct {
	r   io.Reader
	err error
}

func (s *sourceReader) Read(b []byte) (int, error) {
	n, err := s.r.Read(b)
	if err != nil && err != io.EOF {
		s.err = err
	}
	return n, err
}

// maxTarget is the longest symlink target read, as Linux's PATH_MAX.
const maxTarget = 4096

// readTarget returns the target of zf, a symlink, its contents.
func readTarget(zf *stdzip.File) (string, error) {
	if zf.Flags&1 != 0 {
		return "", errors.New("encrypted, which binate can't read")
	}
	rc, err := open(zf)
	if err != nil {
		return "", openError(zf, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxTarget+1))
	switch {
	case err != nil:
		return "", fmt.Errorf("damaged: %w", err)
	case len(b) > maxTarget:
		return "", errors.New("a symlink whose target is too long")
	}
	return string(b), nil
}

// Extra fields' IDs: NTFS's, with times to 100 nanoseconds, PKWARE's Unix
// field and Info-ZIP's old one, each with a time to the second and maybe
// an owner, Info-ZIP's extended timestamp, to the second, its new Unix
// field, with an owner, and its Unicode path, a name in UTF-8.
const (
	ntfsExtra        = 0x000a
	pkwareUnixExtra  = 0x000d
	extTimeExtra     = 0x5455
	oldUnixExtra     = 0x5855
	unixExtra        = 0x7875
	unicodePathExtra = 0x7075
)

// utf8Flag is the general purpose flag of a name in UTF-8.
const utf8Flag = 0x800

// entryName returns the name of zf, as decodedName gives it, but with
// each backslash a "/" if made on MS-DOS or Windows FAT and holding no
// "/", as some tools there wrote names, and as unzip extracts them.
func entryName(zf *stdzip.File) string {
	name := decodedName(zf)
	if zf.CreatorVersion>>8 == creatorFAT && !strings.Contains(name, "/") {
		name = strings.ReplaceAll(name, "\\", "/")
	}
	return name
}

// decodedName returns the name of zf: as it is if flagged as UTF-8, else
// that of a Unicode path field made from it. Else it is as it is if made
// on Unix, in the bytes of the system that made it, as unzip extracts it
// there, as macOS's Finder writes UTF-8 without the flag. One made
// anywhere else, as on DOS or Windows, is in code page 437, the format's
// default, as Python's zipfile reads it, though its bytes may form UTF-8.
func decodedName(zf *stdzip.File) string {
	if zf.Flags&utf8Flag != 0 {
		return zf.Name
	}
	for id, data := range extras(zf.Extra) {
		// A version, 1, the CRC-32 of the name it stands for, then itself.
		if id == unicodePathExtra && len(data) > 5 && data[0] == 1 &&
			binary.LittleEndian.Uint32(data[1:]) == crc32.ChecksumIEEE([]byte(zf.Name)) && utf8.Valid(data[5:]) {
			return string(data[5:])
		}
	}
	if creator := zf.CreatorVersion >> 8; creator == creatorUnix || creator == creatorMacOS {
		return zf.Name
	}
	name, err := charmap.CodePage437.NewDecoder().String(zf.Name)
	if err != nil {
		return zf.Name
	}
	return name
}

// newEntry returns the entry zf, named name, describes, and whether it
// records a Unix mode, and an owner. One made elsewhere than on Unix has
// none: it gets the mode of unzip's with the usual umask, 0755 for a
// directory, as one whose name, as entryName gives it, ends in "/", and
// 0644 for a file, and owner 0.
func newEntry(zf *stdzip.File, name string) (e *archive.Entry, hasMode, hasOwner bool) {
	e = &archive.Entry{}
	creator := zf.CreatorVersion >> 8
	hasMode = (creator == creatorUnix || creator == creatorMacOS) && zf.ExternalAttrs>>16 != 0
	mode := zf.Mode()
	if hasMode {
		e.Stat.Mode = unixType(mode) | uint32(mode.Perm())
		if mode&fs.ModeSetuid != 0 {
			e.Stat.Mode |= unix.S_ISUID
		}
		if mode&fs.ModeSetgid != 0 {
			e.Stat.Mode |= unix.S_ISGID
		}
		if mode&fs.ModeSticky != 0 {
			e.Stat.Mode |= unix.S_ISVTX
		}
	} else if mode.IsDir() || strings.HasSuffix(name, "/") {
		e.Stat.Mode = unix.S_IFDIR | 0o755
	} else {
		e.Stat.Mode = unix.S_IFREG | 0o644
	}
	if e.Stat.Mode&unix.S_IFMT == unix.S_IFREG {
		e.Stat.Size = int64(zf.UncompressedSize64)
	}
	for id, data := range extras(zf.Extra) {
		switch id {
		case pkwareUnixExtra, oldUnixExtra:
			// Times, then the owner, which Info-ZIP's old field holds only
			// in the local header.
			if len(data) >= 12 {
				e.Stat.Uid = uint32(binary.LittleEndian.Uint16(data[8:]))
				e.Stat.Gid = uint32(binary.LittleEndian.Uint16(data[10:]))
				hasOwner = true
			}
		case unixExtra:
			if uid, gid, ok := unixOwner(data); ok {
				e.Stat.Uid, e.Stat.Gid, hasOwner = uid, gid, true
			}
		}
	}
	e.Stat.Mtime, e.Stat.MtimePrecision = mtimeOf(zf)
	return e, hasMode, hasOwner
}

// dos2038 is 2038-01-18 as a DOS date and time, of zf's ModifiedDate
// above ModifiedTime, from which 32 bits of seconds overflow a signed
// number.
const dos2038 = (2038-1980)<<25 | 1<<21 | 18<<16

// unixTime returns the time at the start of b, of zf: 32 bits of seconds
// since 1970, as Info-ZIP's and PKWARE's fields record it. With the top
// bit set, they are after 2038 if zf's DOS time is, as unzip tells them.
// Else they are before 1970, and not ok, as unzip takes the DOS time
// then, which zip records as 1980.
func unixTime(b []byte, zf *stdzip.File) (t time.Time, ok bool) {
	secs := binary.LittleEndian.Uint32(b)
	dos := uint32(zf.ModifiedDate)<<16 | uint32(zf.ModifiedTime)
	if secs&(1<<31) != 0 && dos < dos2038 {
		return time.Time{}, false
	}
	return time.Unix(int64(secs), 0), true
}

// ntfsToUnix is the seconds from 1601, which NTFS's times count from in
// 100 nanoseconds, to 1970.
const ntfsToUnix = 11644473600

// mtimeOf returns the mtime of zf, and how finely it is recorded: from
// the extra field that records it most finely, NTFS's to 100 nanoseconds,
// or one of the others to the second, or else from its DOS time, to two
// seconds, which is local, as unzip takes it.
func mtimeOf(zf *stdzip.File) (time.Time, time.Duration) {
	var mtime time.Time
	var precision time.Duration
	found := func(t time.Time, p time.Duration) {
		if mtime.IsZero() || p < precision {
			mtime, precision = t, p
		}
	}
	for id, data := range extras(zf.Extra) {
		switch id {
		case ntfsExtra:
			// Reserved, then attributes by tag and size: 1 holds the mtime,
			// the access and the creation time.
			for attrs := data[min(4, len(data)):]; len(attrs) >= 4; {
				tag, n := binary.LittleEndian.Uint16(attrs), int(binary.LittleEndian.Uint16(attrs[2:]))
				if 4+n > len(attrs) {
					break
				}
				if tag == 1 && n >= 8 {
					ticks := binary.LittleEndian.Uint64(attrs[4:])
					found(time.Unix(int64(ticks/1e7)-ntfsToUnix, int64(ticks%1e7)*100), 100*time.Nanosecond)
				}
				attrs = attrs[4+n:]
			}
		case extTimeExtra:
			// Flags, then the mtime if the first is set.
			if len(data) >= 5 && data[0]&1 != 0 {
				if t, ok := unixTime(data[1:], zf); ok {
					found(t, time.Second)
				}
			}
		case pkwareUnixExtra, oldUnixExtra:
			// The access time, then the mtime.
			if len(data) >= 8 {
				if t, ok := unixTime(data[4:], zf); ok {
					found(t, time.Second)
				}
			}
		}
	}
	if !mtime.IsZero() {
		return mtime, precision
	}
	d, t := zf.ModifiedDate, zf.ModifiedTime
	return time.Date(int(d>>9)+1980, time.Month(d>>5&0xf), int(d&0x1f),
		int(t>>11), int(t>>5&0x3f), int(t&0x1f)*2, 0, time.Local), 2 * time.Second
}

// unixType returns the Unix file type of mode.
func unixType(mode fs.FileMode) uint32 {
	switch {
	case mode.IsDir():
		return unix.S_IFDIR
	case mode&fs.ModeSymlink != 0:
		return unix.S_IFLNK
	case mode&fs.ModeNamedPipe != 0:
		return unix.S_IFIFO
	case mode&fs.ModeSocket != 0:
		return unix.S_IFSOCK
	case mode&fs.ModeCharDevice != 0:
		return unix.S_IFCHR
	case mode&fs.ModeDevice != 0:
		return unix.S_IFBLK
	}
	return unix.S_IFREG
}

// extras returns the extra fields in b by their IDs, a field's data
// without its header, up to one cut short.
func extras(b []byte) func(yield func(uint16, []byte) bool) {
	return func(yield func(uint16, []byte) bool) {
		for len(b) >= 4 {
			id, n := binary.LittleEndian.Uint16(b), int(binary.LittleEndian.Uint16(b[2:]))
			if 4+n > len(b) || !yield(id, b[4:4+n]) {
				return
			}
			b = b[4+n:]
		}
	}
}

// unixOwner returns the uid and gid in data, of Info-ZIP's new Unix extra
// field: a version, 1, then each as its size and its little-endian bytes.
func unixOwner(data []byte) (uid, gid uint32, ok bool) {
	if len(data) < 1 || data[0] != 1 {
		return 0, 0, false
	}
	data = data[1:]
	var ids [2]uint32
	for i := range ids {
		if len(data) < 1 || int(data[0]) > len(data)-1 || data[0] > 8 {
			return 0, 0, false
		}
		n := int(data[0])
		var v uint64
		for j := n - 1; j >= 0; j-- {
			v = v<<8 | uint64(data[1+j])
		}
		if v > 1<<32-1 {
			return 0, 0, false
		}
		ids[i] = uint32(v)
		data = data[1+n:]
	}
	return ids[0], ids[1], true
}
