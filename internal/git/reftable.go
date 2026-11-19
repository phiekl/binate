// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// reftableRecord is a ref as a reftable records it: deleted, an object
// name, or a symref's target.
type reftableRecord struct {
	deleted bool
	id      ID
	target  string
}

// Sizes of a reftable's header and footer, by format version.
var (
	reftableHeader = map[byte]int{1: 24, 2: 28}
	reftableFooter = map[byte]int{1: 68, 2: 72}
)

// parseReftable returns the refs the reftable data records, by name, for
// a repository of object format f. Only its ref blocks are read, in
// order: the indexes, objects and logs after them are not needed.
func parseReftable(data []byte, f *Format) (map[string]reftableRecord, error) {
	if len(data) < 5 || string(data[:4]) != "REFT" {
		return nil, errors.New("not a reftable")
	}
	version := data[4]
	header, footer := reftableHeader[version], reftableFooter[version]
	if header == 0 {
		return nil, fmt.Errorf("reftable version %d, which is not supported", version)
	}
	if len(data) < header+footer {
		return nil, errors.New("truncated reftable")
	}
	hash := "sha1"
	if version == 2 {
		hash = string(data[24:28])
	}
	if want := map[*Format]string{SHA1: "sha1", SHA256: "s256"}[f]; hash != want {
		return nil, fmt.Errorf("reftable of object format %q, not the repository's", hash)
	}
	end := len(data) - footer
	if string(data[end:end+4]) != "REFT" {
		return nil, errors.New("invalid reftable footer")
	}
	refs := map[string]reftableRecord{}
	for off := header; off < end; {
		if data[off] == 0 {
			// Padding up to the next block.
			off++
			continue
		}
		if data[off] != 'r' {
			// Past the refs.
			break
		}
		if off+4 > end {
			return nil, errors.New("truncated reftable block")
		}
		// The first block's length counts the file header too, which
		// its restart offsets are from.
		start := off
		if off == header {
			start = 0
		}
		blockEnd := start + int(uint32(data[off+1])<<16|uint32(data[off+2])<<8|uint32(data[off+3]))
		if blockEnd > end || blockEnd < off+6 {
			return nil, errors.New("invalid reftable block length")
		}
		records := blockEnd - 2 - 3*int(binary.BigEndian.Uint16(data[blockEnd-2:]))
		if records < off+4 {
			return nil, errors.New("invalid reftable restarts")
		}
		if err := parseRefRecords(data[off+4:records], f, refs); err != nil {
			return nil, err
		}
		off = blockEnd
	}
	return refs, nil
}

// parseRefRecords adds the records of a ref block to refs. Each name
// shares a prefix with the one before it.
func parseRefRecords(data []byte, f *Format, refs map[string]reftableRecord) error {
	var last []byte
	for len(data) > 0 {
		prefix, n := reftableVarint(data)
		if n == 0 {
			return errors.New("invalid reftable record")
		}
		data = data[n:]
		suffixType, n := reftableVarint(data)
		if n == 0 {
			return errors.New("invalid reftable record")
		}
		data = data[n:]
		suffix, valueType := suffixType>>3, suffixType&7
		if prefix > uint64(len(last)) || suffix > uint64(len(data)) {
			return errors.New("invalid reftable record name")
		}
		name := append(last[:prefix:prefix], data[:suffix]...)
		data = data[suffix:]
		last = name
		// The update index, which only orders the logs.
		if _, n = reftableVarint(data); n == 0 {
			return errors.New("invalid reftable record")
		}
		data = data[n:]
		var r reftableRecord
		switch valueType {
		case 0:
			r.deleted = true
		case 1, 2:
			// An object name, and for a tag its peeled one, not needed.
			size := f.size * int(valueType)
			if len(data) < size {
				return errors.New("truncated reftable record")
			}
			r.id, _ = IDFromBytes(f, data[:f.size])
			data = data[size:]
		case 3:
			length, n := reftableVarint(data)
			if n == 0 || length > uint64(len(data)-n) {
				return errors.New("invalid reftable symref")
			}
			r.target = string(data[n : n+int(length)])
			data = data[n+int(length):]
			if err := checkRefName(r.target); err != nil {
				return fmt.Errorf("ref %s: %w", name, err)
			}
		default:
			return fmt.Errorf("reftable record of type %d", valueType)
		}
		refs[string(name)] = r
	}
	return nil
}

// reftableVarint decodes a reftable's variable-length integer, as a pack's
// delta offset is encoded, and returns it and its length, 0 if invalid.
func reftableVarint(data []byte) (uint64, int) {
	var v uint64
	for i, b := range data {
		if i > 0 {
			if v >= 1<<56 {
				return 0, 0
			}
			v = (v + 1) << 7
		}
		v |= uint64(b & 0x7f)
		if b&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}
