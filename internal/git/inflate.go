// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bufio"
	"compress/zlib"
	"io"
	"sync"
)

// inflater inflates a zlib stream. Inflaters are kept in a pool between
// streams, as making one allocates its 32 KiB window, which in a walk of
// history, reading many small objects, would take most of the time.
type inflater struct {
	br *bufio.Reader
	z  io.ReadCloser
}

var inflaters sync.Pool

// newInflater returns an inflater of the zlib stream in r, to be released
// once read.
func newInflater(r io.Reader) (*inflater, error) {
	inf, _ := inflaters.Get().(*inflater)
	if inf == nil {
		inf = &inflater{br: bufio.NewReaderSize(r, 4096)}
	} else {
		inf.br.Reset(r)
	}
	var err error
	if inf.z == nil {
		inf.z, err = zlib.NewReader(inf.br)
	} else {
		err = inf.z.(zlib.Resetter).Reset(inf.br, nil)
	}
	if err != nil {
		inf.release()
		return nil, err
	}
	return inf, nil
}

func (inf *inflater) Read(p []byte) (int, error) { return inf.z.Read(p) }

// release returns inf to the pool; it must not be read after.
func (inf *inflater) release() {
	inf.br.Reset(nil)
	inflaters.Put(inf)
}
