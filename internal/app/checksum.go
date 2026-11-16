// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

// The code shared by the checksum actions: sha256.go, sha384.go and
// sha512.go.

import (
	"context"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"strings"

	"pxy.se/go/binate/internal/files"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/view"
)

// checksum returns the action named name computing a file's checksum with
// newHash, linked to the line its GNU coreutils command prints.
func checksum(key rune, name string, newHash func() hash.Hash) action {
	return action{
		key: key, name: name, what: "checksum of each side",
		accepts: func(k scan.Kind) bool { return k == scan.File },
		needs:   "a regular file",
		cache:   true,
		run: func(ctx context.Context, root source.Root, rel string, side *sideRun) view.ActionSide {
			sum, err := hashFile(ctx, root, rel, newHash(), side)
			if err != nil {
				return view.ActionSide{Err: err.Error()}
			}
			return view.ActionSide{Value: sum, Link: sumLine(sum, rel)}
		},
	}
}

// hashChunk is how much hashFile reads at a time, between checking for
// cancellation and reporting progress.
const hashChunk = 1 << 20

// hashFile returns the hex digest of h over the regular file at rel in
// root, without following a symlink, reporting the file it reads to side.
func hashFile(ctx context.Context, root source.Root, rel string, h hash.Hash, side *sideRun) (string, error) {
	f, err := root.Open(rel, false)
	if err != nil {
		return "", files.UnwrapPath(err)
	}
	defer f.Close()
	side.Read(f)
	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	buf := make([]byte, hashChunk)
	var done int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		done += int64(n)
		if size > 0 {
			side.Progress(min(float64(done)/float64(size), 1))
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", files.UnwrapPath(err)
		}
	}
	side.Read(f)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sumLine returns the line sha256sum and the like print for a file named
// name with the digest sum: as GNU coreutils does, a name holding a
// backslash, newline or carriage return has them escaped, and the line
// starts with a backslash.
func sumLine(sum, name string) string {
	if strings.ContainsAny(name, "\\\n\r") {
		name = strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`).Replace(name)
		return `\` + sum + "  " + name
	}
	return sum + "  " + name
}
