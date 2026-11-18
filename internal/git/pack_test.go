// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// history commits revisions of a few files, each a little changed, which
// git packs as chains of deltas, and returns the last commit.
func history(r *testRepo, revisions int) ID {
	r.t.Helper()
	var last ID
	for i := range revisions {
		var b strings.Builder
		for j := range 2000 {
			fmt.Fprintf(&b, "line %d of a file that changes a little\n", j)
			if j%(97+i) == 0 {
				fmt.Fprintf(&b, "revision %d\n", i)
			}
		}
		r.write(map[string]string{
			"big.txt":       b.String(),
			"dir/small.txt": fmt.Sprintf("small %d\n", i),
			"keep.txt":      "the same in every revision\n",
		})
		last = r.commit()
	}
	return last
}

// verifyPack returns git's description of the repository's packs.
func (r *testRepo) verifyPack() string {
	r.t.Helper()
	idx, _ := filepath.Glob(filepath.Join(r.dir, ".git", "objects", "pack", "*.idx"))
	if len(idx) == 0 {
		r.t.Fatal("no packs")
	}
	return r.git(append([]string{"verify-pack", "-v"}, idx...)...)
}

func TestPack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format *Format
		repack []string
	}{
		{"offset deltas", SHA1, []string{"repack", "-adf", "--depth=50", "--window=50"}},
		{"named deltas", SHA1, []string{"-c", "repack.useDeltaBaseOffset=false", "repack", "-adf", "--depth=50", "--window=50"}},
		{"sha256", SHA256, []string{"repack", "-adf", "--depth=50", "--window=50"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRepo(t, tc.format)
			history(r, 30)
			r.git(tc.repack...)
			if loose, _ := filepath.Glob(filepath.Join(r.dir, ".git", "objects", "??")); len(loose) != 0 {
				// Packed objects are no longer loose after repack -d, but
				// prune only goes by age: remove them so the pack is read.
				for _, d := range loose {
					_ = os.RemoveAll(d)
				}
			}
			if v := r.verifyPack(); !strings.Contains(v, "chain length = 3") {
				t.Fatalf("no delta chains three long:\n%s", v)
			}
			repo := r.open()
			defer repo.Close()
			checkAll(t, r, repo)
			for _, o := range r.all() {
				if size, err := repo.Size(o.id); err != nil || size != int64(len(o.data)) {
					t.Errorf("%s: size %d, %v", o.id, size, err)
				}
			}
		})
	}
}

func TestPackAndLoose(t *testing.T) {
	r := newTestRepo(t, SHA1)
	history(r, 5)
	r.git("repack", "-ad")
	r.write(map[string]string{"new.txt": "loose\n"})
	r.commit()
	repo := r.open()
	defer repo.Close()
	checkAll(t, r, repo)
}

func TestLargeOffsets(t *testing.T) {
	r := newTestRepo(t, SHA1)
	history(r, 5)
	r.git("repack", "-ad")
	// Index the pack again, with every offset past 0x40 in the table of
	// large offsets, as if past 2 GiB.
	packs, _ := filepath.Glob(filepath.Join(r.dir, ".git", "objects", "pack", "*.pack"))
	idx := strings.TrimSuffix(packs[0], ".pack") + ".idx"
	if err := os.Remove(idx); err != nil {
		t.Fatal(err)
	}
	r.git("index-pack", "--index-version=2,0x40", packs[0])
	repo := r.open()
	defer repo.Close()
	if len(repo.packList()[0].idx.off64) == 0 {
		t.Fatal("no large offsets")
	}
	checkAll(t, r, repo)
}

func TestIndexTruncated(t *testing.T) {
	r := newTestRepo(t, SHA1)
	history(r, 2)
	r.git("repack", "-ad")
	repo := r.open()
	defer repo.Close()
	id := r.id("HEAD")
	if _, _, err := repo.Info(id); err != nil {
		t.Fatal(err)
	}
	idx, _ := filepath.Glob(filepath.Join(r.dir, ".git", "objects", "pack", "*.idx"))
	if err := os.Chmod(idx[0], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(idx[0], 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.Info(id); !errors.Is(err, errFault) {
		t.Errorf("read through a truncated index: %v", err)
	}
}

func TestPackMissing(t *testing.T) {
	r := newTestRepo(t, SHA1)
	history(r, 2)
	r.git("repack", "-ad")
	repo := r.open()
	defer repo.Close()
	missing, _ := ParseID(SHA1, strings.Repeat("00", 20))
	if _, err := repo.Open(missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("open: %v", err)
	}
	if _, err := repo.Size(missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("size: %v", err)
	}
}

func TestApplyDelta(t *testing.T) {
	base := []byte("0123456789")
	for _, tc := range []struct {
		name, delta, want string
		ok                bool
	}{
		{"copy", "\x0a\x04\x91\x02\x04", "2345", true},
		{"insert", "\x0a\x03\x03abc", "abc", true},
		{"both", "\x0a\x05\x02xy\x90\x03", "xy012", true},
		{"base size", "\x09\x01\x01a", "", false},
		{"short", "\x0a\x05\x01a", "", false},
		{"long", "\x0a\x01\x02ab", "", false},
		{"beyond base", "\x0a\x04\x91\x08\x04", "", false},
		{"reserved", "\x0a\x00\x00", "", false},
		{"cut copy", "\x0a\x04\x91", "", false},
		{"cut insert", "\x0a\x03\x03ab", "", false},
		{"no header", "", "", false},
	} {
		got, err := applyDelta(base, []byte(tc.delta), 1<<20)
		if (err == nil) != tc.ok || string(got) != tc.want {
			t.Errorf("%s: %q, %v", tc.name, got, err)
		}
	}
	if _, err := applyDelta(base, []byte("\x0a\x80\x80\x04"), 1000); err == nil {
		t.Error("made a result over the limit")
	}
}

func FuzzApplyDelta(f *testing.F) {
	f.Add([]byte("0123456789"), []byte("\x0a\x05\x02xy\x90\x03"))
	f.Add([]byte("abc"), []byte("\x03\x03\x91\x00\x03"))
	f.Fuzz(func(t *testing.T, base, delta []byte) {
		out, err := applyDelta(base, delta, 1<<20)
		if err != nil {
			return
		}
		_, size, _ := deltaSizes(&sliceReader{delta})
		if int64(len(out)) != size {
			t.Errorf("result of %d bytes, not %d", len(out), size)
		}
	})
}

func FuzzParseEntry(f *testing.F) {
	f.Add([]byte{0x35})
	f.Add([]byte{0xb5, 0x10})
	f.Add([]byte{0x65, 0x81, 0x00})
	f.Add(append([]byte{0x75}, bytes.Repeat([]byte{1}, 20)...))
	f.Fuzz(func(t *testing.T, b []byte) {
		e, err := parseEntry(b, SHA1)
		if err != nil {
			return
		}
		if e.size < 0 || e.data > int64(len(b)) || e.data < 1 || e.typ == 0 || e.typ == 5 || e.typ > 7 {
			t.Errorf("%x: %+v", b, e)
		}
		if e.typ == ofsDelta && e.base < 0 {
			t.Errorf("%x: base %d", b, e.base)
		}
	})
}

func FuzzIndex(f *testing.F) {
	// An index of one object, at offset 12.
	var idx bytes.Buffer
	idx.Write(idxMagic)
	idx.Write([]byte{0, 0, 0, 2})
	for i := range 256 {
		if i < 0x11 {
			idx.Write([]byte{0, 0, 0, 0})
		} else {
			idx.Write([]byte{0, 0, 0, 1})
		}
	}
	idx.Write(bytes.Repeat([]byte{0x11}, 20))
	idx.Write([]byte{0, 0, 0, 0})
	idx.Write([]byte{0, 0, 0, 12})
	idx.Write(make([]byte, 40))
	f.Add(idx.Bytes(), bytes.Repeat([]byte{0x11}, 20))
	f.Fuzz(func(t *testing.T, data, name []byte) {
		x, err := newIndex(data, SHA1.size)
		if err != nil {
			return
		}
		id, err := IDFromBytes(SHA1, name)
		if err != nil {
			return
		}
		if off, ok, err := x.find(id); err == nil && ok && off < 0 {
			t.Errorf("offset %d", off)
		}
	})
}

func TestFailedPackUnmapsIndex(t *testing.T) {
	r := newTestRepo(t, SHA1)
	history(r, 2)
	r.git("repack", "-ad")
	indexes, err := filepath.Glob(filepath.Join(r.dir, ".git", "objects", "pack", "*.idx"))
	if err != nil || len(indexes) != 1 {
		t.Fatalf("indexes: %v, %v", indexes, err)
	}
	idx := indexes[0]
	packPath := strings.TrimSuffix(idx, ".idx") + ".pack"
	for _, broken := range []string{"missing", "invalid"} {
		t.Run(broken, func(t *testing.T) {
			if broken == "missing" {
				if err := os.Remove(packPath); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(packPath, []byte("invalid"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := openPack(&Repo{format: SHA1}, idx); err == nil {
				t.Fatal("opened broken pack")
			}
			maps, err := os.ReadFile("/proc/self/maps")
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(maps, []byte(idx)) {
				t.Fatal("failed pack left index mapped")
			}
		})
	}
}

func TestSmallObjectSkipsDeltaBlobs(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(fmt.Sprintf("named=%t", named), func(t *testing.T) {
			r := newTestRepo(t, SHA1)
			history(r, 10)
			r.git("-c", fmt.Sprintf("repack.useDeltaBaseOffset=%t", !named), "repack", "-adf", "--depth=50", "--window=50")
			repo := r.open()
			defer repo.Close()
			deltas := 0
			for _, o := range r.all() {
				if o.typ != Blob.String() {
					continue
				}
				p, off, err := repo.find(o.id)
				if err != nil || p == nil {
					t.Fatalf("find packed blob: %v", err)
				}
				e, err := p.entry(off)
				if err != nil {
					t.Fatal(err)
				}
				if e.typ == ofsDelta || e.typ == refDelta {
					deltas++
				}
				typ, data, err := repo.readSmallObject(o.id)
				if err != nil || typ != Blob || data != nil {
					t.Fatalf("blob type: %v, %d bytes, %v", typ, len(data), err)
				}
				if repo.cache.size != 0 {
					t.Fatal("type lookup reconstructed blob delta bases")
				}
			}
			if deltas == 0 {
				t.Fatal("fixture has no delta blobs")
			}
		})
	}
}

func TestSmallObjectCorruptPackedCommit(t *testing.T) {
	for _, format := range []*Format{SHA1, SHA256} {
		t.Run(format.name, func(t *testing.T) {
			r := newTestRepo(t, format)
			r.write(map[string]string{"file": "contents\n"})
			id := r.commit()
			r.git("repack", "-adf", "--window=0")
			repo := openTest(t, r, "")
			p, off, err := repo.find(id)
			if err != nil || p == nil {
				t.Fatalf("find packed commit: %v", err)
			}
			e, err := p.entry(off)
			if err != nil {
				t.Fatal(err)
			}
			if e.typ != uint8(Commit) {
				t.Fatalf("expected non-delta commit, got %d", e.typ)
			}
			if err := os.Chmod(p.f.Name(), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(p.f.Name(), os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			// An invalid zlib header makes opening the object fail before reading it.
			_, err = f.WriteAt([]byte{0, 0}, e.data)
			closeErr := f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			_, _, err = repo.readSmallObject(id)
			var corruptErr *CorruptError
			if !errors.As(err, &corruptErr) {
				t.Fatalf("expected corrupt object error, got %v", err)
			}
			if _, err := repo.Resolve("HEAD"); !errors.As(err, &corruptErr) {
				t.Fatalf("resolve: expected corrupt object error, got %v", err)
			}
		})
	}
}

// TestPacksAdded checks that objects packed after the repository was
// opened are read, as after git gc, and the packs open before still are
// though deleted, by each way of reading them.
func TestPacksAdded(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(repo *Repo, id ID) (int64, error)
	}{
		{"Size", func(repo *Repo, id ID) (int64, error) { return repo.Size(id) }},
		{"Info", func(repo *Repo, id ID) (int64, error) {
			typ, size, err := repo.Info(id)
			if err == nil && typ != Blob {
				err = fmt.Errorf("a %v", typ)
			}
			return size, err
		}},
		{"Read", func(repo *Repo, id ID) (int64, error) {
			_, data, err := repo.Read(id, 100)
			return int64(len(data)), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRepo(t, SHA1)
			r.write(map[string]string{"old": "old\n"})
			r.commit()
			r.git("gc", "-q")
			repo := r.open()
			defer repo.Close()
			r.write(map[string]string{"new": "new\n"})
			r.commit()
			r.git("gc", "-q", "--prune=now")
			for _, name := range []string{"new", "old"} {
				if size, err := tc.read(repo, r.id("HEAD:"+name)); err != nil || size != 4 {
					t.Errorf("%s: %d, %v", name, size, err)
				}
			}
			missing, err := ParseID(SHA1, strings.Repeat("ab", 20))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tc.read(repo, missing); !errors.Is(err, ErrNotFound) {
				t.Errorf("a missing object: %v", err)
			}
		})
	}
}

// TestPacksAddedConcurrently checks that readers missing an object at once,
// after git gc, all find it, not only the one that opens the new pack.
func TestPacksAddedConcurrently(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"old": "old\n"})
	r.commit()
	r.git("gc", "-q")
	repo := r.open()
	defer repo.Close()
	r.write(map[string]string{"new": "new\n"})
	r.commit()
	r.git("gc", "-q", "--prune=now")
	id := r.id("HEAD:new")
	const readers = 128
	errs := make([]error, readers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range readers {
		wg.Go(func() {
			<-start
			_, errs[i] = repo.Size(id)
		})
	}
	close(start)
	wg.Wait()
	failed := 0
	for _, err := range errs {
		if err != nil {
			failed++
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d readers failed: %v", failed, readers, errors.Join(errs...))
	}
}

// TestLoosePackedMeanwhile checks that an object read loose, after the
// packs were searched while it was, is found where git gc packed it and
// pruned the loose copy in between.
func TestLoosePackedMeanwhile(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"old": "old\n"})
	r.commit()
	r.git("gc", "-q")
	repo := r.open()
	defer repo.Close()
	r.write(map[string]string{"new": "new\n"})
	r.commit()
	id := r.id("HEAD:new")
	if p, _, err := repo.find(id); p != nil || err != nil {
		t.Fatalf("packed before git gc: %v", err)
	}
	r.git("gc", "-q", "--prune=now")
	obj, err := repo.openLoose(id)
	if err != nil {
		t.Fatal(err)
	}
	_ = obj.Close()
	if obj.Type != Blob || obj.Size != 4 {
		t.Errorf("%v of %d bytes", obj.Type, obj.Size)
	}
}
