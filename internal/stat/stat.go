// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package stat reads what the panels below the panes show about an entry:
// its type, size, permissions, owner, links, inode and times.
package stat

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
)

// Stat is an entry's metadata, read without following a symlink.
type Stat struct {
	// Mode is the type and permission bits, as st_mode.
	Mode     uint32
	Size     int64
	Uid, Gid uint32
	Nlink    uint64
	Ino      uint64
	// Rdev is a device's number.
	Rdev                uint64
	Mtime, Ctime, Atime time.Time
	// MtimePrecision is how finely Mtime is recorded, as to the second in
	// most tar archives, or 0 if exactly: it is compared that finely (see
	// scan.SameMtime).
	MtimePrecision time.Duration
	// Btime is when the entry was created, or zero where the file system
	// doesn't record it.
	Btime time.Time
	// Target is a symlink's target.
	Target string
	// Implied is set for a directory an archive holds entries of but
	// doesn't list, which records no mode, owner or times.
	Implied bool
	// User and Group name the owner as recorded with the entry, as in an
	// archive, where they may differ from this system's names of its ids,
	// or are "" to look the names up.
	User, Group string
}

// mask is what is asked of statx: the basic fields and the birth time.
const mask = unix.STATX_BASIC_STATS | unix.STATX_BTIME

// Read reads the entry at path, without following a symlink.
func Read(path string) (*Stat, error) {
	st, err := ReadAt(unix.AT_FDCWD, path)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// ReadAt reads the entry at path relative to an open directory. If reading
// a symlink's target fails, it returns the symlink's metadata and the error.
func ReadAt(dir int, path string) (*Stat, error) {
	var stx unix.Statx_t
	if err := unix.Statx(dir, path, unix.AT_SYMLINK_NOFOLLOW, mask, &stx); err != nil {
		return nil, &os.PathError{Op: "statx", Path: path, Err: err}
	}
	st := fromStatx(&stx)
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		target, err := files.ReadlinkAt(dir, path)
		st.Target = target
		return st, err
	}
	return st, nil
}

func fromStatx(stx *unix.Statx_t) *Stat {
	ts := func(t unix.StatxTimestamp) time.Time { return time.Unix(t.Sec, int64(t.Nsec)) }
	st := &Stat{
		Mode:  uint32(stx.Mode),
		Size:  int64(stx.Size),
		Uid:   stx.Uid,
		Gid:   stx.Gid,
		Nlink: uint64(stx.Nlink),
		Ino:   stx.Ino,
		Rdev:  unix.Mkdev(stx.Rdev_major, stx.Rdev_minor),
		Mtime: ts(stx.Mtime),
		Ctime: ts(stx.Ctime),
		Atime: ts(stx.Atime),
	}
	if stx.Mask&unix.STATX_BTIME != 0 {
		st.Btime = ts(stx.Btime)
	}
	return st
}

// Names maps user and group ids to names.
type Names struct {
	users, groups map[uint32]string
}

// LoadNames reads the names in /etc/passwd and /etc/group. It must be
// called before the process is restricted, which denies reading them. A
// file that can't be read leaves its ids without names.
func LoadNames() *Names {
	return &Names{users: readIDs("/etc/passwd"), groups: readIDs("/etc/group")}
}

func readIDs(path string) map[uint32]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	return parseIDs(f)
}

// parseIDs reads lines of "name:password:id:...", as in /etc/passwd and
// /etc/group. The first name given an id is its name.
func parseIDs(r io.Reader) map[uint32]string {
	ids := map[uint32]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, ":", 4)
		if len(fields) < 3 || fields[0] == "" {
			continue
		}
		id, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			continue
		}
		if _, ok := ids[uint32(id)]; !ok {
			ids[uint32(id)] = fields[0]
		}
	}
	return ids
}

// User returns the name of a user id, or "".
func (n *Names) User(uid uint32) string {
	if n == nil {
		return ""
	}
	return n.users[uid]
}

// Group returns the name of a group id, or "".
func (n *Names) Group(gid uint32) string {
	if n == nil {
		return ""
	}
	return n.groups[gid]
}
