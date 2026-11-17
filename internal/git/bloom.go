// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"encoding/binary"
	"math/bits"
)

type graphBloom struct {
	index, data []byte
	version     uint32
	numHashes   uint32
}

func parseGraphBloom(count uint32, index, data []byte) graphBloom {
	if uint64(len(index)) != 4*uint64(count) || len(data) < 12 {
		return graphBloom{}
	}
	version := binary.BigEndian.Uint32(data)
	numHashes := binary.BigEndian.Uint32(data[4:])
	// Git uses seven hashes. Bound the work even for nonstandard filters.
	if (version != 1 && version != 2) || numHashes == 0 || numHashes > 64 || binary.BigEndian.Uint32(data[8:]) == 0 {
		return graphBloom{}
	}
	return graphBloom{index: index, data: data[12:], version: version, numHashes: numHashes}
}

type bloomKey struct{ hash, step uint32 }

type bloomKeys struct {
	enabled  bool
	unsigned bloomKey
	signed   bloomKey
}

func newBloomKeys(path string) bloomKeys {
	const seed0, seed1 = 0x293ae76f, 0x7e646e2c
	return bloomKeys{
		enabled:  true,
		unsigned: bloomKey{bloomHash(path, seed0, false), bloomHash(path, seed1, false)},
		signed:   bloomKey{bloomHash(path, seed0, true), bloomHash(path, seed1, true)},
	}
}

// excludes reports whether a filter rules out a change from the first parent.
func (b graphBloom) excludes(row uint32, keys bloomKeys) bool {
	if b.version == 0 || !keys.enabled {
		return false
	}
	off := 4 * int(row)
	var start uint32
	if off != 0 {
		start = binary.BigEndian.Uint32(b.index[off-4:])
	}
	end := binary.BigEndian.Uint32(b.index[off:])
	if start >= end || uint64(end) > uint64(len(b.data)) {
		return false
	}
	filter := b.data[start:end]
	// Version 1 depended on whether char was signed when Git was built.
	// A graph can come from either kind of build, so check both encodings.
	if b.contains(filter, keys.unsigned) {
		return false
	}
	return b.version != 1 || keys.signed == keys.unsigned || !b.contains(filter, keys.signed)
}

func (b graphBloom) contains(filter []byte, key bloomKey) bool {
	n := uint64(len(filter)) * 8
	h := key.hash
	for range b.numHashes {
		bit := uint64(h) % n
		if filter[bit/8]&(1<<(bit%8)) == 0 {
			return false
		}
		h += key.step
	}
	return true
}

// bloomHash is MurmurHash3 x86_32. The signed variant reproduces Git's v1
// sign extension when char was signed, including its effect on the tail.
func bloomHash(path string, seed uint32, signed bool) uint32 {
	word := func(b byte) uint32 {
		if signed {
			return uint32(int32(int8(b)))
		}
		return uint32(b)
	}
	mix := func(w uint32) uint32 {
		return bits.RotateLeft32(w*0xcc9e2d51, 15) * 0x1b873593
	}
	h := seed
	i := 0
	for ; i+4 <= len(path); i += 4 {
		w := word(path[i]) | word(path[i+1])<<8 | word(path[i+2])<<16 | word(path[i+3])<<24
		h = bits.RotateLeft32(h^mix(w), 13)*5 + 0xe6546b64
	}
	var tail uint32
	for j := i; j < len(path); j++ {
		tail ^= word(path[j]) << (8 * (j - i))
	}
	h ^= mix(tail) ^ uint32(len(path))
	h = (h ^ h>>16) * 0x85ebca6b
	h = (h ^ h>>13) * 0xc2b2ae35
	return h ^ h>>16
}
