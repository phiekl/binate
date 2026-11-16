// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"pxy.se/go/binate/internal/files"
)

// Pack entry types beyond the object types: deltas against a base at an
// offset in the same pack, or named.
const (
	ofsDelta = 6
	refDelta = 7
)

// pack is a pack file and its index.
type pack struct {
	repo *Repo
	f    *os.File
	// end is where the entries end: before the trailing checksum.
	end int64
	idx *packIndex
}

// openPack opens the pack whose index is at idxPath, checking that the
// two belong together.
func openPack(repo *Repo, idxPath string) (*pack, error) {
	idx, err := openIndex(idxPath, repo.format)
	if err != nil {
		return nil, err
	}
	keepIndex := false
	defer func() {
		if !keepIndex {
			_ = unmap(idx)
		}
	}()
	f, err := files.OpenRegular(strings.TrimSuffix(idxPath, ".idx")+".pack", false)
	if err != nil {
		return nil, err
	}
	p := &pack{repo: repo, f: f, idx: idx}
	if err := p.check(); err != nil {
		_ = f.Close()
		return nil, err
	}
	keepIndex = true
	return p, nil
}

// check checks the pack's header, and that it ends with the checksum its
// index has, and holds as many objects.
func (p *pack) check() error {
	fi, err := p.f.Stat()
	if err != nil {
		return err
	}
	hs := int64(p.repo.format.size)
	if fi.Size() < 12+hs {
		return errors.New("pack too short")
	}
	var head [12]byte
	if _, err := p.f.ReadAt(head[:], 0); err != nil {
		return err
	}
	if string(head[:4]) != "PACK" {
		return errors.New("not a pack")
	}
	if v := binary.BigEndian.Uint32(head[4:]); v != 2 && v != 3 {
		return fmt.Errorf("pack of unknown version %d", v)
	}
	if n := binary.BigEndian.Uint32(head[8:]); int64(n) != int64(p.idx.n) {
		return errors.New("pack and its index of different objects")
	}
	sum := make([]byte, hs)
	if _, err := p.f.ReadAt(sum, fi.Size()-hs); err != nil {
		return err
	}
	want, err := p.idx.checksum()
	if err != nil {
		return err
	}
	if !bytes.Equal(sum, want) {
		return errors.New("pack and its index of different packs")
	}
	p.end = fi.Size() - hs
	return nil
}

// entry is the header of a pack entry.
type entry struct {
	off  int64
	typ  uint8
	size int64 // of the data inflated: an object's, or a delta's
	data int64 // offset of the compressed data
	// base is a delta's base: at an offset for ofsDelta, named for
	// refDelta.
	base   int64
	baseID ID
}

// maxEntryHeader is the most an entry's header may take: the type and
// size, and a delta's base.
const maxEntryHeader = 10 + 32

// entry reads the header of the entry at off.
func (p *pack) entry(off int64) (entry, error) {
	if off < 12 || off >= p.end {
		return entry{}, fmt.Errorf("pack entry at invalid offset %d", off)
	}
	var buf [maxEntryHeader]byte
	n, err := p.f.ReadAt(buf[:min(int64(len(buf)), p.end-off)], off)
	if err != nil && !errors.Is(err, io.EOF) {
		return entry{}, err
	}
	e, err := parseEntry(buf[:n], p.repo.format)
	if err != nil {
		return entry{}, fmt.Errorf("pack entry at %d: %w", off, err)
	}
	e.off = off
	e.data += off
	if e.typ == ofsDelta {
		if e.base <= 0 || e.base >= off {
			return entry{}, fmt.Errorf("pack entry at %d: delta base out of range", off)
		}
		e.base = off - e.base
	}
	return e, nil
}

// parseEntry parses an entry's header at the start of b: its data offset
// is relative to b, and an ofsDelta's base is the distance back.
func parseEntry(b []byte, f *Format) (entry, error) {
	if len(b) == 0 {
		return entry{}, errors.New("cut short")
	}
	c := b[0]
	e := entry{typ: (c >> 4) & 7, size: int64(c & 15)}
	i := 1
	for shift := uint(4); c&0x80 != 0; shift += 7 {
		if i >= len(b) {
			return entry{}, errors.New("cut short")
		}
		if shift > 55 {
			return entry{}, errors.New("size too large")
		}
		c = b[i]
		i++
		e.size |= int64(c&0x7f) << shift
	}
	switch e.typ {
	case uint8(Commit), uint8(Tree), uint8(Blob), uint8(Tag):
	case ofsDelta:
		if i >= len(b) {
			return entry{}, errors.New("cut short")
		}
		c = b[i]
		i++
		ofs := int64(c & 0x7f)
		for c&0x80 != 0 {
			if i >= len(b) {
				return entry{}, errors.New("cut short")
			}
			if ofs >= 1<<55 {
				return entry{}, errors.New("delta base offset too large")
			}
			c = b[i]
			i++
			ofs = (ofs+1)<<7 | int64(c&0x7f)
		}
		e.base = ofs
	case refDelta:
		if len(b)-i < f.size {
			return entry{}, errors.New("cut short")
		}
		e.baseID, _ = IDFromBytes(f, b[i:i+f.size])
		i += f.size
	default:
		return entry{}, fmt.Errorf("unknown type %d", e.typ)
	}
	e.data = int64(i)
	return e, nil
}

// inflate returns a reader of e's data, of e.size bytes, failing if the
// data turns out to be of another size or corrupt.
func (p *pack) inflate(e entry, id ID) (io.Reader, error) {
	sr := io.NewSectionReader(p.f, e.data, p.end-e.data)
	z, err := zlib.NewReader(bufio.NewReaderSize(sr, 4096))
	if err != nil {
		return nil, corrupt(id, "pack entry at %d: %v", e.off, err)
	}
	return &sizedReader{r: z, left: e.size, id: id}, nil
}

// inflateAll returns e's data, which may be at most max bytes.
func (p *pack) inflateAll(e entry, id ID, max int64) ([]byte, error) {
	if e.size > max {
		return nil, fmt.Errorf("pack entry at %d of %d bytes, larger than %d", e.off, e.size, max)
	}
	r, err := p.inflate(e, id)
	if err != nil {
		return nil, err
	}
	data := make([]byte, e.size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		if err == nil {
			err = corrupt(id, "pack entry at %d longer than its size", e.off)
		}
		return nil, err
	}
	return data, nil
}

// resultSize returns the size of the object the delta e makes, from the
// start of its data.
func (p *pack) resultSize(e entry, id ID) (int64, error) {
	r, err := p.inflate(e, id)
	if err != nil {
		return 0, err
	}
	_, size, err := deltaSizes(bufio.NewReaderSize(r, 32))
	if err != nil {
		return 0, corrupt(id, "pack entry at %d: %v", e.off, err)
	}
	return size, nil
}

// maxDepth is the longest chain of deltas followed, past which it is taken
// to be a loop of named bases. Git makes none longer than 4095.
const maxDepth = 10000

// maxObject is the largest object made of deltas. Git doesn't delta files
// larger than core.bigFileThreshold, 512 MiB by default.
const maxObject = 1 << 30

// open opens the object id at off. One stored whole is read as it is
// inflated; one made of deltas is made in memory.
func (p *pack) open(off int64, id ID) (Object, error) {
	e, err := p.entry(off)
	if err != nil {
		return Object{}, corrupt(id, "%v", err)
	}
	if e.typ != ofsDelta && e.typ != refDelta {
		r, err := p.inflate(e, id)
		if err != nil {
			return Object{}, err
		}
		return Object{Type: Type(e.typ), Size: e.size, ReadCloser: io.NopCloser(r)}, nil
	}
	typ, data, err := p.resolve(e, id, 0)
	if err != nil {
		return Object{}, err
	}
	return Object{Type: typ, Size: int64(len(data)), ReadCloser: io.NopCloser(bytes.NewReader(data))}, nil
}

// read returns the type and content of the object at off, depth deltas
// down from the object asked for.
func (p *pack) read(off int64, id ID, depth int) (Type, []byte, error) {
	if typ, data, ok := p.repo.cache.get(p, off); ok {
		return typ, data, nil
	}
	e, err := p.entry(off)
	if err != nil {
		return 0, nil, corrupt(id, "%v", err)
	}
	var typ Type
	var data []byte
	if e.typ == ofsDelta || e.typ == refDelta {
		typ, data, err = p.resolve(e, id, depth)
	} else {
		typ = Type(e.typ)
		data, err = p.inflateAll(e, id, maxObject)
	}
	if err != nil {
		return 0, nil, err
	}
	p.repo.cache.put(p, off, typ, data)
	return typ, data, nil
}

// resolve makes the object of the delta e, depth deltas down from the
// object asked for, id.
func (p *pack) resolve(e entry, id ID, depth int) (Type, []byte, error) {
	if depth >= maxDepth {
		return 0, nil, corrupt(id, "deltas more than %d deep", maxDepth)
	}
	var typ Type
	var base []byte
	var err error
	if e.typ == ofsDelta {
		typ, base, err = p.read(e.base, id, depth+1)
	} else {
		typ, base, err = p.repo.read(e.baseID, depth+1)
	}
	if err != nil {
		return 0, nil, err
	}
	delta, err := p.inflateAll(e, id, maxObject)
	if err != nil {
		return 0, nil, err
	}
	data, err := applyDelta(base, delta, maxObject)
	if err != nil {
		return 0, nil, corrupt(id, "pack entry at %d: %v", e.off, err)
	}
	return typ, data, nil
}

// info returns the type and size of the object id at off, without
// inflating more than a delta's start, depth deltas down.
func (p *pack) info(off int64, id ID, depth int) (Type, int64, error) {
	e, err := p.entry(off)
	if err != nil {
		return 0, 0, corrupt(id, "%v", err)
	}
	switch e.typ {
	case ofsDelta, refDelta:
	default:
		return Type(e.typ), e.size, nil
	}
	size, err := p.resultSize(e, id)
	if err != nil {
		return 0, 0, err
	}
	// The type is the base's, at the end of the chain.
	for d := depth; ; d++ {
		if d >= maxDepth {
			return 0, 0, corrupt(id, "deltas more than %d deep", maxDepth)
		}
		if e.typ == refDelta {
			typ, _, err := p.repo.info(e.baseID, d+1)
			return typ, size, err
		}
		if e, err = p.entry(e.base); err != nil {
			return 0, 0, corrupt(id, "%v", err)
		}
		if e.typ != ofsDelta && e.typ != refDelta {
			return Type(e.typ), size, nil
		}
	}
}

// size returns the size of the object id at off: what info returns,
// without following deltas to their base for the type.
func (p *pack) size(off int64, id ID) (int64, error) {
	e, err := p.entry(off)
	if err != nil {
		return 0, corrupt(id, "%v", err)
	}
	if e.typ != ofsDelta && e.typ != refDelta {
		return e.size, nil
	}
	return p.resultSize(e, id)
}
