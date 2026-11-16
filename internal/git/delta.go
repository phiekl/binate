// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"io"
)

// deltaSizes reads the two sizes a delta starts with: of its base and of
// its result.
func deltaSizes(r io.ByteReader) (base, result int64, err error) {
	if base, err = readVarint(r); err != nil {
		return 0, 0, err
	}
	if result, err = readVarint(r); err != nil {
		return 0, 0, err
	}
	return base, result, nil
}

// readVarint reads a delta's size: seven bits a byte, least significant
// first, the high bit set on all but the last.
func readVarint(r io.ByteReader) (int64, error) {
	var n uint64
	for shift := uint(0); shift < 63; shift += 7 {
		c, err := r.ReadByte()
		if err != nil {
			return 0, errors.New("delta header cut short")
		}
		n |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			if n > 1<<62 {
				break
			}
			return int64(n), nil
		}
	}
	return 0, errors.New("delta size too large")
}

// sliceReader reads bytes from a slice, for deltaSizes.
type sliceReader struct {
	b []byte
}

func (s *sliceReader) ReadByte() (byte, error) {
	if len(s.b) == 0 {
		return 0, io.EOF
	}
	c := s.b[0]
	s.b = s.b[1:]
	return c, nil
}

// applyDelta returns the object delta makes of base. The result may be
// at most max bytes.
func applyDelta(base, delta []byte, max int64) ([]byte, error) {
	r := &sliceReader{delta}
	baseSize, size, err := deltaSizes(r)
	if err != nil {
		return nil, err
	}
	if baseSize != int64(len(base)) {
		return nil, errors.New("delta of a base of another size")
	}
	if size > max {
		return nil, errors.New("delta result too large")
	}
	out := make([]byte, 0, size)
	d := r.b
	for len(d) > 0 {
		op := d[0]
		d = d[1:]
		switch {
		case op&0x80 != 0:
			// Copy from the base: the bits of op say which bytes of the
			// offset and size follow.
			var off, n uint64
			for i := range 4 {
				if op&(1<<i) != 0 {
					if len(d) == 0 {
						return nil, errors.New("delta copy cut short")
					}
					off |= uint64(d[0]) << (8 * i)
					d = d[1:]
				}
			}
			for i := range 3 {
				if op&(0x10<<i) != 0 {
					if len(d) == 0 {
						return nil, errors.New("delta copy cut short")
					}
					n |= uint64(d[0]) << (8 * i)
					d = d[1:]
				}
			}
			if n == 0 {
				n = 0x10000
			}
			if off+n > uint64(len(base)) {
				return nil, errors.New("delta copy beyond its base")
			}
			if int64(len(out))+int64(n) > size {
				return nil, errors.New("delta result longer than its size")
			}
			out = append(out, base[off:off+n]...)
		case op != 0:
			// Insert the next op bytes.
			n := int(op)
			if n > len(d) {
				return nil, errors.New("delta insert cut short")
			}
			if int64(len(out))+int64(n) > size {
				return nil, errors.New("delta result longer than its size")
			}
			out = append(out, d[:n]...)
			d = d[n:]
		default:
			return nil, errors.New("delta with a reserved instruction")
		}
	}
	if int64(len(out)) != size {
		return nil, errors.New("delta result shorter than its size")
	}
	return out, nil
}
