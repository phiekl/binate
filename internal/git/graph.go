// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"iter"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/files"
)

// A commitGraph is a chain of mapped graphs, oldest first. A standalone
// commit-graph is a chain of one. Parent positions are relative to the chain.
type commitGraph []*graphLayer

const (
	graphNoParent = 0x70000000
	graphExtra    = 0x80000000
)

type graphLayer struct {
	data               []byte
	format             *Format
	start, end         uint32 // range of positions in the chain
	fanout, oids, rows []byte
	edges              []byte
	bloom              graphBloom
}

type graphDir struct {
	info, split int
}

// historyGraph shares a completed load. Canceling a load leaves later walks
// free to retry, and a waiting walk can cancel independently.
func (r *Repo) historyGraph(ctx context.Context) (commitGraph, error) {
	if len(r.shallow) != 0 {
		return nil, ctx.Err()
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r.graphMu.Lock()
		if r.graphReady {
			g := r.graph
			r.graphMu.Unlock()
			return g, nil
		}
		if done := r.graphLoading; done != nil {
			r.graphMu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		r.graphLoading = done
		r.graphMu.Unlock()

		g := openCommitGraph(ctx, r.dirs, r.format)
		err := ctx.Err()
		if err != nil {
			g.close()
			g = nil
		}
		r.graphMu.Lock()
		r.graph, r.graphReady = g, err == nil
		r.graphLoading = nil
		close(done)
		r.graphMu.Unlock()
		return g, err
	}
}

func openCommitGraph(ctx context.Context, dirs []string, f *Format) commitGraph {
	// The objects directories are already allowed. Keep all graph lookups
	// beneath them, without following symlinks in info.
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	var graphDirs []graphDir
	defer func() {
		for _, dir := range graphDirs {
			if dir.split >= 0 {
				_ = unix.Close(dir.split)
			}
			_ = unix.Close(dir.info)
		}
	}()
	for _, dir := range dirs {
		if ctx.Err() != nil {
			return nil
		}
		info, err := unix.Open(filepath.Join(dir, "info"), flags, 0)
		if err == nil {
			split, err := unix.Openat(info, "commit-graphs", flags, 0)
			if err != nil {
				split = -1
			}
			graphDirs = append(graphDirs, graphDir{info: info, split: split})
		}
	}
	for _, dir := range graphDirs {
		if ctx.Err() != nil {
			return nil
		}
		if l := mapGraphLayer(ctx, dir.info, "commit-graph", f, 0, nil, ID{}); l != nil {
			return commitGraph{l}
		}
		if dir.split >= 0 {
			if g := readGraphChain(ctx, dir.split, graphDirs, f); len(g) != 0 {
				return g
			}
		}
	}
	return nil
}

func readGraphChain(ctx context.Context, split int, dirs []graphDir, f *Format) commitGraph {
	chain, err := files.OpenRegularAt(split, "commit-graph-chain", false)
	if err != nil {
		return nil
	}
	defer func() { _ = chain.Close() }()
	// The header can name at most 255 base graphs.
	limit := 256 * (2*f.size + 1)
	data, err := io.ReadAll(io.LimitReader(chain, int64(limit+1)))
	if err != nil || len(data) > limit {
		return nil
	}
	var g commitGraph
	var bases []ID
	var start uint32
	for line := range strings.Lines(string(data)) {
		sum, err := ParseID(f, strings.TrimSuffix(line, "\n"))
		if err != nil {
			break
		}
		name := "graph-" + sum.String() + ".graph"
		var l *graphLayer
		for _, dir := range dirs {
			if ctx.Err() != nil {
				break
			}
			if dir.split >= 0 {
				l = mapGraphLayer(ctx, dir.split, name, f, start, bases, sum)
				if l != nil {
					break
				}
			}
		}
		if l == nil {
			break
		}
		g = append(g, l)
		start = l.end
		bases = append(bases, sum)
	}
	// A writer may replace the tip while we open it. Earlier layers
	// still form a complete graph for the commits they cover.
	return g
}

func mapGraphLayer(ctx context.Context, dir int, name string, f *Format, start uint32, bases []ID, want ID) *graphLayer {
	file, err := files.OpenRegularAt(dir, name, false)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()
	fi, err := file.Stat()
	if err != nil {
		return nil
	}
	size := fi.Size()
	if size < int64(8+12+f.size) || size > 1<<40 || int64(int(size)) != size {
		return nil
	}
	data, err := unix.Mmap(int(file.Fd()), 0, int(size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil
	}
	var l *graphLayer
	if err := safely(func() {
		body, trailer := data[:len(data)-f.size], data[len(data)-f.size:]
		if !want.IsZero() && !bytes.Equal(trailer, want.Bytes()) {
			return
		}
		l = parseGraphLayer(data, f, start, bases)
		if l != nil {
			h := f.new()
			const blockSize = 1 << 20
			for len(body) != 0 {
				if ctx.Err() != nil {
					l = nil
					return
				}
				n := min(len(body), blockSize)
				_, _ = h.Write(body[:n])
				body = body[n:]
			}
			if !bytes.Equal(h.Sum(nil), trailer) {
				l = nil
			}
		}
	}); err != nil || l == nil {
		_ = unix.Munmap(data)
		return nil
	}
	return l
}

// parseGraphLayer checks the layout and splits the file into its chunks.
// The format is documented in gitformat-commit-graph(5).
func parseGraphLayer(data []byte, f *Format, start uint32, bases []ID) *graphLayer {
	hs := f.size
	hashVersion := byte(1)
	if f == SHA256 {
		hashVersion = 2
	}
	if len(data) < 8+12+hs || string(data[:4]) != "CGPH" || data[4] != 1 ||
		data[5] != hashVersion || int(data[7]) != len(bases) {
		return nil
	}
	chunks := int(data[6])
	tableEnd, bodyEnd := 8+12*(chunks+1), len(data)-hs
	if tableEnd > bodyEnd {
		return nil
	}
	table := data[8:tableEnd]
	offset := binary.BigEndian.Uint64(table[4:])
	if offset < uint64(tableEnd) || offset > uint64(bodyEnd) {
		return nil
	}
	parts := make(map[string][]byte, chunks)
	for i := range chunks {
		tag := string(table[12*i : 12*i+4])
		next := binary.BigEndian.Uint64(table[12*(i+1)+4:])
		if tag == "\x00\x00\x00\x00" || parts[tag] != nil || next < offset || next > uint64(bodyEnd) {
			return nil
		}
		parts[tag] = data[offset:next]
		offset = next
	}
	if string(table[12*chunks:12*chunks+4]) != "\x00\x00\x00\x00" || offset != uint64(bodyEnd) {
		return nil
	}
	base := parts["BASE"]
	if len(base) != len(bases)*hs {
		return nil
	}
	for i, sum := range bases {
		if !bytes.Equal(base[i*hs:(i+1)*hs], sum.Bytes()) {
			return nil
		}
	}
	fanout := parts["OIDF"]
	if len(fanout) != 256*4 {
		return nil
	}
	var count uint32
	for i := range 256 {
		next := binary.BigEndian.Uint32(fanout[4*i:])
		if next < count {
			return nil
		}
		count = next
	}
	oids, rows, edges := parts["OIDL"], parts["CDAT"], parts["EDGE"]
	if uint64(start)+uint64(count) > graphNoParent || oids == nil || rows == nil ||
		uint64(len(oids)) != uint64(count)*uint64(hs) ||
		uint64(len(rows)) != uint64(count)*uint64(hs+16) || len(edges)%4 != 0 {
		return nil
	}
	return &graphLayer{
		data: data, format: f, start: start, end: start + count,
		fanout: fanout, oids: oids, rows: rows, edges: edges,
		bloom: parseGraphBloom(count, parts["BIDX"], parts["BDAT"]),
	}
}

func (g commitGraph) find(id ID) (*graphLayer, uint32) {
	for i := len(g) - 1; i >= 0; i-- {
		if row, ok := g[i].find(id); ok {
			return g[i], row
		}
	}
	return nil, 0
}

func (l *graphLayer) find(id ID) (uint32, bool) {
	want := id.Bytes()
	hs := l.format.size
	if len(want) != hs {
		return 0, false
	}
	b := int(want[0])
	var lo uint32
	if b != 0 {
		lo = binary.BigEndian.Uint32(l.fanout[4*(b-1):])
	}
	hi := binary.BigEndian.Uint32(l.fanout[4*b:])
	if hi > l.end-l.start {
		return 0, false
	}
	for lo < hi {
		mid := lo + (hi-lo)/2
		off := int(mid) * hs
		switch c := bytes.Compare(l.oids[off:off+hs], want); {
		case c < 0:
			lo = mid + 1
		case c > 0:
			hi = mid
		default:
			return mid, true
		}
	}
	return 0, false
}

func (g commitGraph) close() {
	for _, l := range g {
		_ = unix.Munmap(l.data)
	}
}

func (l *graphLayer) id(row uint32) ID {
	hs := l.format.size
	off := int(row) * hs
	id, _ := IDFromBytes(l.format, l.oids[off:off+hs])
	return id
}

func (l *graphLayer) tree(row uint32) ID {
	hs := l.format.size
	off := int(row) * (hs + 16)
	id, _ := IDFromBytes(l.format, l.rows[off:off+hs])
	return id
}

// parents lists the parent positions in order, yielding graphNoParent
// for a malformed list.
func (l *graphLayer) parents(row uint32) iter.Seq[uint32] {
	return func(yield func(uint32) bool) {
		var first, second uint32
		if err := safely(func() {
			hs := l.format.size
			off := int(row)*(hs+16) + hs
			data := l.rows[off : off+8]
			first = binary.BigEndian.Uint32(data)
			second = binary.BigEndian.Uint32(data[4:])
		}); err != nil {
			yield(graphNoParent)
			return
		}
		if first == graphNoParent {
			if second != graphNoParent {
				yield(graphNoParent)
			}
			return
		}
		if !yield(first) || second == graphNoParent {
			return
		}
		if second&graphExtra == 0 {
			yield(second)
			return
		}
		for i := uint64(second&^graphExtra) * 4; i < uint64(len(l.edges)); i += 4 {
			var edge uint32
			if err := safely(func() { edge = binary.BigEndian.Uint32(l.edges[i:]) }); err != nil {
				break
			}
			if !yield(edge&^graphExtra) || edge&graphExtra != 0 {
				return
			}
		}
		yield(graphNoParent)
	}
}
