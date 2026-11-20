// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func (r *testRepo) objectsDir() string {
	return filepath.Join(r.dir, ".git", "objects")
}

func (r *testRepo) addEmptyCommits(n int) ID {
	r.t.Helper()
	tree, head := r.id("HEAD^{tree}").String(), r.id("HEAD").String()
	for range n {
		head = strings.TrimSpace(r.git("commit-tree", tree, "-p", head, "-m", "no change"))
	}
	r.git("update-ref", "HEAD", head)
	return r.id("HEAD")
}

func TestLastChangeCommitGraph(t *testing.T) {
	for _, f := range []*Format{SHA1, SHA256} {
		t.Run(f.name, func(t *testing.T) {
			r := historyRepo(t, f)
			paths := []string{"á/d/f", "dir/é", "mode", "link"}
			r.write(map[string]string{paths[0]: "one", paths[1]: "one", "mode": "plain", "link": "->a"})
			r.commit()
			r.write(map[string]string{paths[0]: "two", paths[1]: "two", "mode": "*plain", "link": "->x"})
			if err := os.Chmod(filepath.Join(r.dir, "mode"), 0o755); err != nil {
				t.Fatal(err)
			}
			changed := r.commit()
			r.write(map[string]string{"unrelated": "some change"})
			unrelated := r.commit()
			tip := r.addEmptyCommits(historyGraphAfter)
			paths = append(paths, "", "a", "c", "d", "d/b", "d/e/f", "d/e/g", "o", "s", "y")

			for _, version := range []uint32{0, 1, 2} {
				t.Run(fmt.Sprintf("bloom%d", version), func(t *testing.T) {
					args := []string{"-c", "commitGraph.changedPathsVersion=" + strconv.Itoa(int(version)),
						"commit-graph", "write", "--reachable"}
					if version != 0 {
						args = append(args, "--changed-paths")
					}
					r.git(args...)
					repo := r.open()
					defer repo.Close()
					for _, p := range paths {
						checkGraphLastChange(t, r, repo, "HEAD", tip, p)
					}
					layer, row := repo.graph.find(unrelated)
					if layer == nil {
						t.Fatal("commit missing from graph")
					}
					if version == 2 && layer.bloom.version == 1 {
						t.Skip("this Git writes only version 1 Bloom filters")
					}
					if version != layer.bloom.version {
						t.Fatalf("Bloom version %d, want %d", layer.bloom.version, version)
					}
					if version != 0 {
						if !layer.bloom.excludes(row, newBloomKeys("mode")) {
							t.Error("unrelated commit should exclude mode")
						}
						changedRow, ok := layer.find(changed)
						if !ok {
							t.Fatal("changed commit missing from graph")
						}
						for _, p := range paths[:4] {
							keys := newBloomKeys(p)
							if layer.bloom.excludes(changedRow, keys) {
								t.Errorf("filter missed change to %q", p)
							}
							if version == 2 {
								// v1 graphs written with unsigned char use the v2 hash.
								unsignedV1 := layer.bloom
								unsignedV1.version = 1
								if unsignedV1.excludes(changedRow, keys) {
									t.Errorf("unsigned v1 filter missed change to %q", p)
								}
							}
						}
					}
				})
			}
		})
	}
}

func TestCommitGraphEmptySubtree(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"d/file": "content"})
	base := r.commit()
	mktree := func(input string) string {
		t.Helper()
		cmd := r.command("mktree")
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("mktree: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	empty := mktree("")
	dir := mktree(fmt.Sprintf("040000 tree %s\tempty\n100644 blob %s\tfile\n", empty, r.id("HEAD:d/file")))
	tree := mktree(fmt.Sprintf("040000 tree %s\td\n", dir))
	r.git("update-ref", "HEAD", strings.TrimSpace(r.git("commit-tree", tree, "-p", base.String(), "-m", "empty subtree")))
	changed := r.id("HEAD")
	tip := r.addEmptyCommits(historyGraphAfter)
	r.git("commit-graph", "write", "--reachable", "--changed-paths")

	repo := r.open()
	defer repo.Close()
	got, _, err := repo.LastChange(context.Background(), tip, "d")
	if err != nil || got != changed {
		t.Fatalf("directory tree last changed in %s, got %s: %v", changed, got, err)
	}
	layer, row := repo.graph.find(changed)
	if layer == nil || !layer.bloom.excludes(row, newBloomKeys("d")) {
		t.Fatal("expected an empty changed-path filter for the added empty subtree")
	}
}

func TestCommitGraphIgnoredSubmodule(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{".gitmodules": "[submodule \"sub\"]\n\tpath = sub\n\turl = ../sub\n\tignore = all\n"})
	base := r.commit()
	r.git("update-index", "--add", "--cacheinfo", "160000,"+base.String()+",sub")
	r.git("commit", "-qm", "add submodule")
	parent := r.id("HEAD")
	r.git("update-index", "--cacheinfo", "160000,"+parent.String()+",sub")
	r.git("commit", "-qm", "update submodule")
	changed := r.id("HEAD")
	tip := r.addEmptyCommits(historyGraphAfter)
	r.git("commit-graph", "write", "--reachable", "--changed-paths")

	repo := r.open()
	defer repo.Close()
	got, _, err := repo.LastChange(context.Background(), tip, "sub")
	if err != nil || got != changed {
		t.Fatalf("submodule last changed in %s, got %s: %v", changed, got, err)
	}
	if layer, _ := repo.graph.find(changed); layer == nil {
		t.Fatal("submodule update missing from graph")
	}
}

func checkGraphLastChange(t *testing.T, r *testRepo, repo *Repo, rev string, start ID, path string) {
	t.Helper()
	spec := path
	if path == "" {
		spec = "."
	}
	want := strings.TrimSpace(r.git("-c", "core.commitGraph=false", "--literal-pathspecs",
		"log", "-1", "--format=%H %at %ct %an <%ae> %s", rev, "--", spec))
	id, c, err := repo.LastChange(context.Background(), start, path)
	if err != nil {
		t.Fatalf("%s %q: %v", rev, path, err)
	}
	if got := fmt.Sprintf("%s %d %d %s <%s> %s", id, c.Author.Unix(), c.Committer.Unix(), c.AuthorName, c.AuthorEmail, c.Subject()); got != want {
		t.Errorf("%s %q: got %q, want %q", rev, path, got, want)
	}
}

func TestLastChangeSplitCommitGraph(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"base": "base"})
	base := r.commit()
	r.git("-c", "commitGraph.changedPathsVersion=1", "commit-graph", "write", "--reachable", "--changed-paths", "--split=no-merge")

	var parents []string
	for _, name := range []string{"one", "two", "three", "four"} {
		r.git("checkout", "-q", "--detach", base.String())
		r.write(map[string]string{"merge": name})
		parents = append(parents, r.commit().String())
	}
	// The matching parent is third, in the middle of the EDGE list.
	args := []string{"commit-tree", parents[2] + "^{tree}", "-m", "octopus"}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	r.git("update-ref", "HEAD", strings.TrimSpace(r.git(args...)))
	octopus := r.id("HEAD")
	r.git("branch", "octopus", "HEAD")
	r.git("-c", "commitGraph.changedPathsVersion=2", "commit-graph", "write", "--reachable", "--changed-paths", "--split=no-merge")
	tip := r.addEmptyCommits(historyGraphAfter) // Descendants outside the graph.

	repo := r.open()
	defer repo.Close()
	for _, p := range []string{"base", "merge", ""} {
		checkGraphLastChange(t, r, repo, "HEAD", tip, p)
	}
	if len(repo.graph) != 2 {
		t.Fatalf("got %d layers, want 2", len(repo.graph))
	}
	if len(repo.graph[1].edges) != 12 {
		t.Fatalf("got %d bytes of extra edges, want 12", len(repo.graph[1].edges))
	}
	layer, row := repo.graph.find(octopus)
	var got []string
	for pos := range layer.parents(row) {
		got = append(got, layer.id(pos-layer.start).String())
	}
	if !slices.Equal(got, parents) {
		t.Fatalf("octopus parents %v, want %v", got, parents)
	}

	split := filepath.Join(r.objectsDir(), "info", "commit-graphs")
	chain, err := os.ReadFile(filepath.Join(split, "commit-graph-chain"))
	if err != nil {
		t.Fatal(err)
	}
	sums := strings.Fields(string(chain))
	alternate := newTestRepo(t, SHA1)
	altSplit := filepath.Join(alternate.objectsDir(), "info", "commit-graphs")
	if err := os.Mkdir(altSplit, 0o755); err != nil {
		t.Fatal(err)
	}
	baseName := "graph-" + sums[0] + ".graph"
	if err := os.Rename(filepath.Join(split, baseName), filepath.Join(altSplit, baseName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.objectsDir(), "info", "alternates"), []byte(alternate.objectsDir()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	borrowed := r.open()
	defer borrowed.Close()
	checkGraphLastChange(t, r, borrowed, "HEAD", tip, "base")
	if len(borrowed.graph) != 2 {
		t.Fatalf("got %d layers with base in an alternate, want 2", len(borrowed.graph))
	}

	// An interrupted chain update can leave the upper layer missing.
	tipSum := sums[1]
	if err := os.Remove(filepath.Join(split, "graph-"+tipSum+".graph")); err != nil {
		t.Fatal(err)
	}
	partial := r.open()
	defer partial.Close()
	checkGraphLastChange(t, r, partial, "HEAD", tip, "base")
	if len(partial.graph) != 1 {
		t.Fatalf("got %d layers after removing the tip, want 1", len(partial.graph))
	}
}

func TestLastChangeDefersCommitGraph(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"a": "one"})
	changed := r.commit()
	tip := r.addEmptyCommits(historyGraphAfter)
	r.git("commit-graph", "write", "--reachable")

	borrower := newTestRepo(t, SHA1)
	alternates := filepath.Join(borrower.objectsDir(), "info", "alternates")
	if err := os.WriteFile(alternates, []byte(r.objectsDir()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := borrower.open()
	defer repo.Close()
	checkGraphLastChange(t, r, repo, changed.String(), changed, "a")
	if repo.graphReady {
		t.Fatal("short walk loaded the commit graph")
	}
	checkGraphLastChange(t, r, repo, "HEAD", tip, "a")
	if layer, _ := repo.graph.find(changed); layer == nil {
		t.Fatal("longer walk did not load the alternate's commit graph")
	}
}

func TestLastChangePicksUpNewCommitGraph(t *testing.T) {
	for _, name := range []string{"standalone", "missing split layer"} {
		t.Run(name, func(t *testing.T) {
			r := newTestRepo(t, SHA1)
			r.write(map[string]string{"a": "one"})
			changed := r.commit()
			tip := r.addEmptyCommits(historyGraphAfter)
			info := filepath.Join(r.objectsDir(), "info")
			path := filepath.Join(info, "commit-graph")
			args := []string{"commit-graph", "write", "--reachable", "--changed-paths"}
			if name == "standalone" {
				r.git(args...)
			} else {
				r.git(append(args, "--split")...)
				dir := filepath.Join(info, "commit-graphs")
				chain, err := os.ReadFile(filepath.Join(dir, "commit-graph-chain"))
				if err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(dir, "graph-"+strings.TrimSpace(string(chain))+".graph")
			}
			saved := filepath.Join(t.TempDir(), "graph")
			if err := os.Rename(path, saved); err != nil {
				t.Fatal(err)
			}

			synctest.Test(t, func(t *testing.T) {
				repo := r.open()
				defer repo.Close()
				checkGraphLastChange(t, r, repo, "HEAD", tip, "a")
				if len(repo.graph) != 0 {
					t.Fatal("first lookup unexpectedly loaded a graph")
				}
				if err := os.Rename(saved, path); err != nil {
					t.Fatal(err)
				}
				if g, err := repo.historyGraph(context.Background()); err != nil || len(g) != 0 {
					t.Fatalf("lookup before retry delay got %d layers and error %v", len(g), err)
				}
				time.Sleep(graphRetryAfter)
				checkGraphLastChange(t, r, repo, "HEAD", tip, "a")
				if layer, _ := repo.graph.find(changed); layer == nil {
					t.Fatal("later lookup did not load the published graph")
				}
			})
		})
	}
}

func TestCommitGraphLoadCanRetryAfterCancellation(t *testing.T) {
	r := newTestRepo(t, SHA1)
	tip := r.commit()
	r.git("commit-graph", "write", "--reachable")

	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		format := *SHA1
		attempts := 0
		format.new = func() hash.Hash {
			attempts++
			if attempts == 1 {
				<-release
			}
			return SHA1.new()
		}
		repo, err := OpenRepo(r.objectsDir(), &format)
		if err != nil {
			t.Fatal(err)
		}
		defer repo.Close()

		load := func(ctx context.Context) <-chan error {
			result := make(chan error, 1)
			go func() {
				g, err := repo.historyGraph(ctx)
				if len(g) != 0 {
					t.Error("canceled load returned a graph")
				}
				result <- err
			}()
			return result
		}
		ctx, cancel := context.WithCancel(context.Background())
		loading := load(ctx)
		synctest.Wait()
		waitCtx, cancelWait := context.WithCancel(context.Background())
		waiting := load(waitCtx)
		synctest.Wait()
		cancelWait()
		synctest.Wait()
		select {
		case err := <-waiting:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("canceled wait returned %v", err)
			}
		default:
			t.Error("canceled wait is still blocked")
		}
		cancel()
		close(release)
		if err := <-loading; !errors.Is(err, context.Canceled) || repo.graphReady {
			t.Fatalf("canceled load returned %v, ready %v", err, repo.graphReady)
		}
		synctest.Wait()
		g, err := repo.historyGraph(context.Background())
		if layer, _ := g.find(tip); err != nil || layer == nil || attempts != 2 {
			t.Fatalf("retry got %d layers and error %v after %d attempts", len(g), err, attempts)
		}
	})
}

func TestCommitGraphFallback(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"a": "one"})
	r.commit()
	r.write(map[string]string{"a": "two"})
	r.commit()
	tip := r.addEmptyCommits(historyGraphAfter)
	r.git("commit-graph", "write", "--reachable", "--changed-paths")
	path := filepath.Join(r.objectsDir(), "info", "commit-graph")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := r.open()
	defer repo.Close()
	checkGraphLastChange(t, r, repo, "HEAD", tip, "a")
	if len(repo.graph) != 1 {
		t.Fatal("graph did not load")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	checkGraphLastChange(t, r, repo, "HEAD", tip, "a")

	// The layout of the graph still parses, but its checksum is wrong.
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := r.open()
	defer bad.Close()
	checkGraphLastChange(t, r, bad, "HEAD", tip, "a")
	if !bad.graphReady || len(bad.graph) != 0 {
		t.Fatal("graph with bad checksum was loaded")
	}
}

func TestCommitGraphSymlinks(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.commit()
	r.git("commit-graph", "write", "--reachable")
	info := filepath.Join(r.objectsDir(), "info")
	path := filepath.Join(info, "commit-graph")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "commit-graph"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "commit-graph"), path); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		if g, _ := openCommitGraph(context.Background(), []string{r.objectsDir()}, SHA1); len(g) != 0 {
			g.close()
			t.Fatal("graph opened through a symlink")
		}
	}
	check()
	if err := os.Rename(info, info+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, info); err != nil {
		t.Fatal(err)
	}
	check()
}

func FuzzCommitGraph(f *testing.F) {
	for _, format := range []*Format{SHA1, SHA256} {
		r := newTestRepo(f, format)
		r.write(map[string]string{"ä/b": "one"})
		r.commit()
		r.commit()
		r.git("commit-graph", "write", "--reachable", "--changed-paths")
		data, err := os.ReadFile(filepath.Join(r.objectsDir(), "info", "commit-graph"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data, format == SHA256, uint32(1), "ä/b")
	}
	f.Add([]byte("CGPH"), false, uint32(0), "")
	f.Fuzz(func(t *testing.T, data []byte, sha256 bool, position uint32, path string) {
		format := SHA1
		if sha256 {
			format = SHA256
		}
		layer := parseGraphLayer(data, format, 0, nil)
		if layer == nil || layer.end == 0 {
			return
		}
		row := position % layer.end
		id := layer.id(row)
		_, _ = layer.find(id)
		_ = layer.tree(row)
		for p := range layer.parents(row) {
			if p < layer.end {
				_ = layer.tree(p)
			}
		}
		_ = layer.bloom.excludes(row, newBloomKeys(path))
	})
}
