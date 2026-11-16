// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
)

// maxHeader is the most a loose object's header may take: the longest
// type name, a space, the digits of the largest int64 and a NUL.
const maxHeader = len("commit") + 1 + 19 + 1

// loosePath returns the path of the loose object id in the objects
// directory dir.
func loosePath(dir string, id ID) string {
	h := id.String()
	return filepath.Join(dir, h[:2], h[2:])
}

// openLoose opens the loose object id in dir, returning ErrNotFound if
// there is none. The reader returns the object's content, and fails at its
// end if the object's size or checksum is wrong.
func openLoose(dir string, id ID) (Object, error) {
	f, err := files.OpenRegular(loosePath(dir, id), false)
	if errors.Is(err, unix.ENOENT) {
		return Object{}, ErrNotFound
	}
	if err != nil {
		return Object{}, err
	}
	z, err := zlib.NewReader(bufio.NewReader(f))
	if err != nil {
		_ = f.Close()
		return Object{}, corrupt(id, "%v", err)
	}
	r := bufio.NewReaderSize(z, 4096)
	head, err := r.Peek(maxHeader)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = f.Close()
		return Object{}, corrupt(id, "%v", err)
	}
	typ, size, n, err := parseLooseHeader(head)
	if err != nil {
		_ = f.Close()
		return Object{}, corrupt(id, "%v", err)
	}
	_, _ = r.Discard(n)
	return Object{
		Type: typ, Size: size,
		ReadCloser: &sizedReader{r: r, left: size, id: id, close: f.Close},
	}, nil
}

// parseLooseHeader parses the header "type size\x00" at the start of b,
// returning the type, the size and the header's length.
func parseLooseHeader(b []byte) (Type, int64, int, error) {
	sp := bytes.IndexByte(b, ' ')
	if sp < 0 {
		return 0, 0, 0, errors.New("no header")
	}
	typ, ok := parseType(b[:sp])
	if !ok {
		return 0, 0, 0, errors.New("unknown type")
	}
	end := bytes.IndexByte(b[sp+1:], 0)
	if end < 0 {
		return 0, 0, 0, errors.New("header not ended")
	}
	size, ok := parseSize(b[sp+1 : sp+1+end])
	if !ok {
		return 0, 0, 0, errors.New("invalid size")
	}
	return typ, size, sp + 1 + end + 1, nil
}

// parseSize parses a size in decimal, as git writes it: digits only, no
// leading zeros.
func parseSize(b []byte) (int64, bool) {
	if len(b) == 0 || len(b) > 1 && b[0] == '0' {
		return 0, false
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	return n, err == nil
}

// sizedReader returns exactly left bytes from r, failing if r ends before
// or goes on after: reading r to its end also checks a zlib stream's
// checksum.
type sizedReader struct {
	r     io.Reader
	left  int64
	id    ID
	close func() error
	err   error
}

func (s *sizedReader) Read(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	if s.left == 0 {
		s.err = s.end()
		return 0, s.err
	}
	if int64(len(p)) > s.left {
		p = p[:s.left]
	}
	n, err := s.r.Read(p)
	s.left -= int64(n)
	switch {
	case errors.Is(err, io.EOF) && s.left > 0:
		s.err = corrupt(s.id, "shorter than its size")
	case err != nil && !errors.Is(err, io.EOF):
		s.err = corrupt(s.id, "%v", err)
	}
	if n > 0 {
		return n, nil
	}
	return 0, s.err
}

// end checks that r ends where the object does.
func (s *sizedReader) end() error {
	var b [1]byte
	for {
		n, err := s.r.Read(b[:])
		if n > 0 {
			return corrupt(s.id, "longer than its size")
		}
		if errors.Is(err, io.EOF) {
			return io.EOF
		}
		if err != nil {
			return corrupt(s.id, "%v", err)
		}
	}
}

func (s *sizedReader) Close() error {
	if s.close == nil {
		return nil
	}
	err := s.close()
	s.close = nil
	return err
}
