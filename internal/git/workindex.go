// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path"
	"strings"
)

const (
	maxWorkIndex   = 256 << 20
	indexStatBytes = 40
)

// pathTree is the directories leading to a set of paths, as a listing of
// each one's entries by its path, "" for the top. The zero pathTree is
// empty.
type pathTree struct {
	names map[string][]string
	// added holds the paths added, and the directories leading to them.
	added map[string]bool
}

// add adds rel and the directories leading to it, each once and in the
// order first added.
func (t *pathTree) add(rel string) {
	if t.names == nil {
		t.names = make(map[string][]string)
		t.added = make(map[string]bool)
	}
	for child := rel; !t.added[child]; {
		t.added[child] = true
		dir := parentDir(child)
		t.names[dir] = append(t.names[dir], path.Base(child))
		if dir == "" {
			break
		}
		child = dir
	}
}

// isDir reports whether rel is a directory below the top with paths below
// it.
func (t *pathTree) isDir(rel string) bool {
	return rel != "" && len(t.names[rel]) > 0
}

// parentDir returns the directory of rel, "" for the top.
func parentDir(rel string) string {
	if dir := path.Dir(rel); dir != "." {
		return dir
	}
	return ""
}

// workIndex records membership and the ancestors needed to find tracked
// entries under an ignored directory.
type workIndex struct {
	files    map[string]Mode
	gitlinks map[string]ID
	// skipped holds the objects of skip-worktree entries, which Git takes
	// from the index rather than the disk. skipTree is the directories
	// leading to them.
	skipped  map[string]ID
	skipTree pathTree
	// tree is the directories leading to the entries.
	tree pathTree
}

func parseWorkIndex(f *Format, data []byte) (workIndex, error) {
	index := workIndex{files: make(map[string]Mode)}
	if len(data) < 12+f.size || string(data[:4]) != "DIRC" {
		return workIndex{}, errors.New("invalid index header")
	}
	version := binary.BigEndian.Uint32(data[4:])
	if version < 2 || version > 4 {
		return workIndex{}, fmt.Errorf("unsupported index version %d", version)
	}
	end := len(data) - f.size
	checksum := data[end:]
	data = data[:end]
	if !bytes.Equal(checksum, make([]byte, f.size)) {
		h := f.new()
		_, _ = h.Write(data)
		if !bytes.Equal(h.Sum(nil), checksum) {
			return workIndex{}, errors.New("invalid index checksum")
		}
	}
	count := binary.BigEndian.Uint32(data[8:])
	data = data[12:]
	var prev string
	var expanded int
	var unnamed bool
	for range count {
		name, mode, stage, skip, used, err := readIndexEntry(f, version, data, prev)
		if err != nil {
			return workIndex{}, err
		}
		entry := data[:used]
		data, prev = data[used:], name
		expanded += len(name)
		if expanded > maxWorkIndex {
			return workIndex{}, errors.New("expanded index paths too large")
		}
		// Split-index replacements can have empty names. Read their
		// entries so that the extension below can report the format.
		if name == "" {
			unnamed = true
			continue
		}
		if !validIndexPath(name) {
			return workIndex{}, fmt.Errorf("invalid index path %q", name)
		}
		if _, tracked := index.files[name]; !tracked || stage == 0 {
			index.files[name] = mode
			id, _ := IDFromBytes(f, entry[indexStatBytes:indexStatBytes+f.size])
			if mode == ModeSubmodule {
				if index.gitlinks == nil {
					index.gitlinks = make(map[string]ID)
				}
				index.gitlinks[name] = id
			}
			if skip && stage == 0 {
				if index.skipped == nil {
					index.skipped = make(map[string]ID)
				}
				index.skipped[name] = id
				index.skipTree.add(name)
			}
		}
		index.tree.add(name)
	}
	for len(data) > 0 {
		if len(data) < 8 {
			return workIndex{}, errors.New("truncated index extension")
		}
		signature := string(data[:4])
		size := binary.BigEndian.Uint32(data[4:])
		data = data[8:]
		if uint64(size) > uint64(len(data)) {
			return workIndex{}, errors.New("truncated index extension")
		}
		switch signature {
		case "link":
			return workIndex{}, errors.New("split index is not supported")
		case "sdir":
			return workIndex{}, errors.New("sparse index is not supported")
		}
		if signature[0] < 'A' || signature[0] > 'Z' {
			return workIndex{}, fmt.Errorf("unsupported index extension %q", signature)
		}
		data = data[size:]
	}
	if unnamed {
		return workIndex{}, errors.New("empty path in index")
	}
	return index, nil
}

// readIndexEntry reads an entry and whether it is skip-worktree.
func readIndexEntry(f *Format, version uint32, data []byte, prev string) (name string, mode Mode, stage uint16, skip bool, used int, err error) {
	off := indexStatBytes + f.size + 2
	if len(data) < off {
		return "", 0, 0, false, 0, errors.New("truncated index entry")
	}
	mode = Mode(binary.BigEndian.Uint32(data[24:]))
	if mode&0o170000 == ModeTree {
		return "", 0, 0, false, 0, errors.New("sparse index is not supported")
	}
	flags := binary.BigEndian.Uint16(data[off-2:])
	if flags&0x4000 != 0 {
		if version == 2 || len(data) < off+2 {
			return "", 0, 0, false, 0, errors.New("invalid extended index entry flags")
		}
		skip = binary.BigEndian.Uint16(data[off:])&0x4000 != 0
		off += 2
	}
	prefix := ""
	if version == 4 {
		drop, n, err := indexPathPrefix(data[off:], len(prev))
		if err != nil {
			return "", 0, 0, false, 0, err
		}
		off += n
		prefix = prev[:len(prev)-drop]
	}
	n := bytes.IndexByte(data[off:], 0)
	if n < 0 {
		return "", 0, 0, false, 0, errors.New("unterminated index path")
	}
	if min(len(prefix)+n, 0xfff) != int(flags&0xfff) {
		return "", 0, 0, false, 0, errors.New("invalid index path length")
	}
	name = prefix + string(data[off:off+n])
	used = off + n + 1
	if version < 4 {
		padded := (used + 7) &^ 7
		if padded > len(data) || len(bytes.Trim(data[used:padded], "\x00")) != 0 {
			return "", 0, 0, false, 0, errors.New("invalid index entry padding")
		}
		used = padded
	}
	return name, mode, (flags >> 12) & 3, skip, used, nil
}

// indexPathPrefix decodes the number of bytes removed from the previous
// name. Git stores it using the same encoding as a pack's delta offset.
func indexPathPrefix(data []byte, maxDrop int) (int, int, error) {
	var drop int
	for i, b := range data {
		drop += int(b & 0x7f)
		if drop > maxDrop {
			break
		}
		if b&0x80 == 0 {
			return drop, i + 1, nil
		}
		if drop >= maxDrop>>7 {
			break
		}
		drop = (drop + 1) << 7
	}
	return 0, 0, errors.New("invalid index path prefix")
}

func validIndexPath(name string) bool {
	for part := range strings.SplitSeq(name, "/") {
		if checkName(part) != nil || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}
