// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime/debug"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
)

// packIndex is a pack's index, version 2, mapped into memory: the names of
// the pack's objects, sorted, with where each is in the pack.
type packIndex struct {
	data   []byte
	hs     int
	n      int
	fanout []byte // 256 counts
	names  []byte // n names
	off32  []byte // n offsets
	off64  []byte // the large offsets
	// packSum is the checksum of the pack the index is of.
	packSum []byte
}

// idxMagic starts an index of version 2 or later.
var idxMagic = []byte{0xff, 't', 'O', 'c'}

// openIndex maps and checks the index at path, of format f. Its contents
// are read through it while mapped, so that one changed meanwhile can't do
// more than make lookups fail: see safely.
func openIndex(path string, f *Format) (*packIndex, error) {
	file, err := files.OpenRegular(path, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	fi, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("pack index not a regular file")
	}
	size := fi.Size()
	hs := f.size
	if size < int64(8+256*4+2*hs) || size > 1<<40 {
		return nil, errors.New("pack index of an invalid size")
	}
	data, err := unix.Mmap(int(file.Fd()), 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, err
	}
	x, err := newIndex(data, hs)
	if err != nil {
		_ = unix.Munmap(data)
		return nil, err
	}
	return x, nil
}

// newIndex returns the index whose contents, of object names of hs bytes,
// are data, checked.
func newIndex(data []byte, hs int) (*packIndex, error) {
	if len(data) < 8+256*4+2*hs {
		return nil, errors.New("pack index of an invalid size")
	}
	x := &packIndex{data: data, hs: hs}
	var perr error
	if err := safely(func() { perr = x.parse() }); err != nil || perr != nil {
		return nil, cmp.Or(err, perr)
	}
	return x, nil
}

// parse checks the index's layout and finds its tables.
func (x *packIndex) parse() error {
	d, hs := x.data, x.hs
	if !bytes.Equal(d[:4], idxMagic) {
		return errors.New("pack index of version 1, which is not supported")
	}
	if v := binary.BigEndian.Uint32(d[4:8]); v != 2 {
		return fmt.Errorf("pack index of unknown version %d", v)
	}
	x.fanout = d[8 : 8+256*4]
	prev := uint32(0)
	for i := range 256 {
		c := binary.BigEndian.Uint32(x.fanout[4*i:])
		if c < prev {
			return errors.New("pack index with a decreasing fanout")
		}
		prev = c
	}
	n := int64(prev)
	rest := int64(len(d)) - int64(8+256*4) - int64(2*hs)
	if n*int64(hs+8) > rest || (rest-n*int64(hs+8))%8 != 0 {
		return errors.New("pack index of the wrong size for its objects")
	}
	x.n = int(n)
	at := int64(8 + 256*4)
	x.names = d[at : at+n*int64(hs)]
	at += n * int64(hs)
	at += n * 4 // the CRCs
	x.off32 = d[at : at+n*4]
	at += n * 4
	end := int64(len(d)) - int64(2*hs)
	x.off64 = d[at:end]
	x.packSum = d[end : end+int64(hs)]
	return nil
}

// unmap unmaps the index, which must not be read after.
func unmap(x *packIndex) error {
	return unix.Munmap(x.data)
}

// find returns where the object id is in the pack, and whether it is
// there at all.
func (x *packIndex) find(id ID) (off int64, ok bool, err error) {
	err = safely(func() {
		want := id.Bytes()
		b := int(want[0])
		lo := 0
		if b > 0 {
			lo = int(binary.BigEndian.Uint32(x.fanout[4*(b-1):]))
		}
		hi := int(binary.BigEndian.Uint32(x.fanout[4*b:]))
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			switch c := bytes.Compare(x.names[mid*x.hs:(mid+1)*x.hs], want); {
			case c == 0:
				off, ok = x.offset(mid), true
				return
			case c < 0:
				lo = mid + 1
			default:
				hi = mid
			}
		}
	})
	if err == nil && ok && off < 0 {
		return 0, false, fmt.Errorf("%s: invalid offset in the pack index", id)
	}
	return off, ok, err
}

// offset returns the offset of the i-th object, or -1 if invalid.
func (x *packIndex) offset(i int) int64 {
	o := binary.BigEndian.Uint32(x.off32[4*i:])
	if o&0x80000000 == 0 {
		return int64(o)
	}
	j := int(o & 0x7fffffff)
	if j >= len(x.off64)/8 {
		return -1
	}
	big := binary.BigEndian.Uint64(x.off64[8*j:])
	if big > 1<<62 {
		return -1
	}
	return int64(big)
}

// checksum returns the pack checksum the index ends with.
func (x *packIndex) checksum() ([]byte, error) {
	var sum []byte
	err := safely(func() { sum = bytes.Clone(x.packSum) })
	return sum, err
}

// errFault is the error reading a mapped file that shrank meanwhile.
var errFault = errors.New("pack index changed while read")

// safely runs f, which reads mapped memory, returning errFault if a page
// it reads is gone, as when the file was truncated, instead of crashing.
func safely(f func()) (err error) {
	defer debug.SetPanicOnFault(debug.SetPanicOnFault(true))
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(interface{ Addr() uintptr }); ok {
				err = errFault
				return
			}
			panic(r)
		}
	}()
	f()
	return nil
}
