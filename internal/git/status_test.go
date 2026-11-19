// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
)

// TestWorkTreeStatus checks that each path has the status git status
// --short gives it.
func TestWorkTreeStatus(t *testing.T) {
	for _, f := range []*Format{SHA1, SHA256} {
		t.Run(f.name, func(t *testing.T) {
			r := newTestRepo(t, f)
			files := map[string]string{}
			for _, name := range []string{
				"keep", "sdel", "udel", "cached", "mm", "md", "am", "tchange",
				"staged-type", "exe", "touched", "conflict", "dir/a", "dir/b",
				"replaced/old", "assumed", "assumed-gone", "assumed-staged",
			} {
				files[name] = name + "\n"
			}
			r.write(files)
			r.commit()
			r.git("checkout", "-q", "-b", "other")
			r.write(map[string]string{"conflict": "theirs\n"})
			r.commit()
			r.git("checkout", "-q", "-")
			r.write(map[string]string{"conflict": "ours\n"})
			r.commit()
			if err := r.command("merge", "-q", "other").Run(); err == nil {
				t.Fatal("merge without a conflict")
			}

			r.git("rm", "-q", "sdel", "dir/b")
			r.git("rm", "-q", "--cached", "cached")
			r.write(map[string]string{"mm": "staged\n", "md": "staged\n", "am": "changed\n", "added": "a\n", "ad": "a\n"})
			r.git("add", "mm", "md", "am", "added", "ad")
			r.write(map[string]string{"mm": "staged, then not\n", "added": "a, then b\n"})
			for _, name := range []string{"udel", "md", "ad", "tchange"} {
				if err := os.Remove(filepath.Join(r.dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			r.write(map[string]string{"tchange": "->keep", "staged-type": "->keep"})
			r.git("add", "staged-type")
			r.write(map[string]string{"staged-type": "staged-type\n"})
			if err := os.Chmod(filepath.Join(r.dir, "exe"), 0o755); err != nil {
				t.Fatal(err)
			}
			// A file in place of a tracked directory is untracked.
			if err := os.RemoveAll(filepath.Join(r.dir, "replaced")); err != nil {
				t.Fatal(err)
			}
			r.write(map[string]string{"ita": "i\n", "untracked/deep/f": "u\n", "new": "n\n", "replaced": "r\n"})
			r.git("add", "-N", "ita")
			// Marked unchanged: Git doesn't look at the disk, even for one
			// deleted, but what is staged still shows.
			r.write(map[string]string{"assumed-staged": "staged\n"})
			r.git("add", "assumed-staged")
			r.git("update-index", "--assume-unchanged", "assumed", "assumed-gone", "assumed-staged")
			r.write(map[string]string{"assumed": "changed\n", "assumed-staged": "changed again\n"})
			if err := os.Remove(filepath.Join(r.dir, "assumed-gone")); err != nil {
				t.Fatal(err)
			}
			// Rewritten unchanged: its stat data no longer matches the
			// index, and its contents must be read.
			r.write(map[string]string{"touched": "touched\n"})
			future := time.Now().Add(time.Hour)
			if err := os.Chtimes(filepath.Join(r.dir, "touched"), future, future); err != nil {
				t.Fatal(err)
			}

			want := gitStatus(t, r)
			tree := openWorkTreeTest(t, r, GlobalIgnore{})
			listed := map[string]bool{}
			for _, name := range workFiles(t, tree) {
				listed[name] = true
			}
			paths := map[string]bool{}
			for _, out := range []string{r.git("ls-files", "-z"), r.git("ls-tree", "-r", "-z", "--name-only", "HEAD")} {
				for name := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
					paths[name] = true
				}
			}
			maps.Copy(paths, listed)
			for _, name := range slices.Sorted(maps.Keys(paths)) {
				got := status(t, tree, name, listed[name])
				if got != want[name] {
					t.Errorf("status of %s = %q, git status gives %q", name, got, want[name])
				}
			}
			if got := status(t, tree, "untracked", true); got != "??" {
				t.Errorf("status of an untracked directory = %q", got)
			}
			if got := status(t, tree, "dir", true); got != "" {
				t.Errorf("status of a tracked directory = %q", got)
			}
		})
	}
}

// gitStatus returns the status git gives each path, as two letters, with
// a path both deleted and untracked as deleted.
func gitStatus(t *testing.T, r *testRepo) map[string]string {
	t.Helper()
	out := r.git("status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all")
	want := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		if line == "" {
			continue
		}
		code, name := line[:2], line[3:]
		if _, ok := want[name]; !ok || code != "??" {
			want[name] = code
		}
	}
	return want
}

// status returns the status of the entry at rel, listed by the work tree
// or not.
func status(t *testing.T, tree *WorkTree, rel string, listed bool) string {
	t.Helper()
	var e *source.Entry
	if listed {
		entry, err := tree.Entry(rel, false)
		if err != nil {
			t.Fatal(err)
		}
		e = &entry
	}
	st, err := tree.Status(context.Background(), rel, e)
	if err != nil {
		t.Fatalf("status of %s: %v", rel, err)
	}
	return st.String()
}

// TestWorkTreeStatusRacy checks that a file changed in the same time slot
// as the index was written, keeping its size and mtime, is read to tell.
func TestWorkTreeStatusRacy(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"f": "one\n"})
	r.commit()
	name := filepath.Join(r.dir, "f")
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	r.write(map[string]string{"f": "two\n"})
	if err := os.Chtimes(name, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	// The index as written in the same second, and the change as made
	// in it, as git can't tell from the index alone.
	index := filepath.Join(r.dir, ".git", "index")
	if err := os.Chtimes(index, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	e, err := tree.Entry("f", false)
	if err != nil {
		t.Fatal(err)
	}
	// Only the ctime tells it apart from what the index recorded:
	// match that too, as when a file is changed quickly enough.
	entry := tree.index.entries["f"]
	e.Ctime = time.Unix(int64(entry.stat.ctimeSec), int64(entry.stat.ctimeNsec))
	e.Ino = uint64(entry.stat.ino)
	st, err := tree.Status(context.Background(), "f", &e)
	if err != nil || st.String() != " M" {
		t.Errorf("status of a racily changed file = %q, %v", st.String(), err)
	}
}

// TestWorkTreeContentIDs checks that a file unchanged since staged has its
// blob's ID, and one that may have changed none, which a scan against a
// revision then reads.
func TestWorkTreeContentIDs(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"same": "same\n", "changed": "one\n", "racy": "one\n", "ita": "i\n", "skipped": "s\n"})
	r.commit()
	settle(t, r)
	r.write(map[string]string{"changed": "two\n", "ita": "i\n"})
	r.git("rm", "-q", "--cached", "ita")
	r.git("add", "-N", "ita")
	r.git("update-index", "--skip-worktree", "skipped")
	// Changed keeping its size and times, which its ctime tells.
	name := filepath.Join(r.dir, "racy")
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	r.write(map[string]string{"racy": "two\n"})
	if err := os.Chtimes(name, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	for rel, want := range map[string]bool{"same": true, "skipped": true, "changed": false, "racy": false, "ita": false} {
		id, got := tree.ContentID(rel)
		blob := string(r.id("HEAD:" + rel).Bytes())
		if got != want || got && (id.Scheme != "git-sha1" || id.Sum != blob) {
			t.Errorf("%s: ID %s %x, want one: %v", rel, id.Scheme, id.Sum, want)
		}
	}

	// Compared with HEAD, the changed files are read and differ.
	head := NewTargetRoot(openTest(t, r, "").Repo, Target{ID: r.id("HEAD^{tree}"), Mode: ModeTree, Commit: r.id("HEAD")}, "HEAD")
	s := scan.Start(head, tree, scan.Options{Compare: scan.DefaultAttrs &^ (scan.Owner | scan.Mtime)}, nil)
	s.Wait()
	for rel, want := range map[string]scan.State{"same": scan.Same, "skipped": scan.Same, "changed": scan.Differ, "racy": scan.Differ, "ita": scan.Same} {
		if got := s.Root.Find(rel).State; got != want {
			t.Errorf("%s: %c, want %c", rel, got.Mark(), want.Mark())
		}
	}
}

// TestWorkTreeContentIDsFilters checks that a file Git may filter as it
// stages it has no ID, its blob holding other bytes than the disk.
func TestWorkTreeContentIDsFilters(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(r *testRepo)
		// global is set for attributes outside the repository.
		global bool
		want   bool
	}{
		{"none", func(r *testRepo) {}, false, true},
		{"top", func(r *testRepo) { r.write(map[string]string{".gitattributes": "f.txt text\n"}) }, false, false},
		{"ancestor", func(r *testRepo) { r.write(map[string]string{"sub/.gitattributes": "f.txt text\n"}) }, false, false},
		{"diff", func(r *testRepo) { r.write(map[string]string{".gitattributes": "# text\nf.txt diff=cpp -merge\n"}) }, false, true},
		{"tracked", func(r *testRepo) {
			r.write(map[string]string{".gitattributes": "f.txt text\n"})
			r.git("add", ".gitattributes")
			r.git("update-index", "--skip-worktree", ".gitattributes")
			if err := os.Remove(filepath.Join(r.dir, ".gitattributes")); err != nil {
				t.Fatal(err)
			}
		}, false, false},
		{"elsewhere", func(r *testRepo) { r.write(map[string]string{"other/.gitattributes": "f.txt text\n"}) }, false, true},
		{"info", func(r *testRepo) { r.write(map[string]string{".git/info/attributes": "f.txt text\n"}) }, false, false},
		{"autocrlf", func(r *testRepo) { r.git("config", "core.autocrlf", "input") }, false, false},
		{"autocrlf alone", func(r *testRepo) {
			config := "[core]\n\trepositoryformatversion = 0\n\tautocrlf\n"
			if err := os.WriteFile(filepath.Join(r.dir, ".git", "config"), []byte(config), 0o644); err != nil {
				t.Fatal(err)
			}
		}, false, false},
		{"global", func(r *testRepo) {}, true, false},
		{"global overridden", func(r *testRepo) { r.write(map[string]string{".gitattributes": "f.txt -text\n"}) }, true, true},
		{"unrelated", func(r *testRepo) { r.write(map[string]string{".gitattributes": "x.bin text\n"}) }, false, true},
		{"unset", func(r *testRepo) { r.write(map[string]string{".gitattributes": "f.txt -text\n"}) }, false, true},
		{"set then unset", func(r *testRepo) { r.write(map[string]string{".gitattributes": "f.txt text\nf.txt -text\n"}) }, false, true},
		{"deeper", func(r *testRepo) {
			r.write(map[string]string{".gitattributes": "f.txt -text\n", "sub/.gitattributes": "f.txt text\n"})
		}, false, false},
		{"info over deeper", func(r *testRepo) {
			r.write(map[string]string{".git/info/attributes": "f.txt -text\n", "sub/.gitattributes": "f.txt text\n"})
		}, false, true},
		{"unspecified", func(r *testRepo) {
			r.write(map[string]string{".gitattributes": "f.txt text\n", "sub/.gitattributes": "f.txt !text\n"})
		}, false, true},
		{"binary", func(r *testRepo) { r.write(map[string]string{".gitattributes": "f.txt binary eol=lf\n"}) }, false, true},
		{"macro", func(r *testRepo) { r.write(map[string]string{".gitattributes": "[attr]conv text\nf.txt conv\n"}) }, false, false},
		{"macro below the top", func(r *testRepo) { r.write(map[string]string{"sub/.gitattributes": "[attr]conv text\nf.txt conv\n"}) }, false, true},
		{"crlf", func(r *testRepo) { r.write(map[string]string{".gitattributes": "f.txt crlf\n"}) }, false, false},
		{"eol", func(r *testRepo) { r.write(map[string]string{".gitattributes": "f.txt eol=crlf\n"}) }, false, false},
		{"ident", func(r *testRepo) { r.write(map[string]string{".gitattributes": "f.txt ident\n"}) }, false, false},
		{"encoding", func(r *testRepo) {
			r.write(map[string]string{".gitattributes": "f.txt working-tree-encoding=UTF-16\n"})
		}, false, false},
		{"path", func(r *testRepo) { r.write(map[string]string{"sub/.gitattributes": "deep/f.txt text\n"}) }, false, false},
		{"other path", func(r *testRepo) { r.write(map[string]string{".gitattributes": "deep/f.txt text\n"}) }, false, true},
		{"quoted", func(r *testRepo) { r.write(map[string]string{".gitattributes": "\"f\\056txt\" text\n"}) }, false, false},
		{"directory pattern", func(r *testRepo) { r.write(map[string]string{".gitattributes": "f.txt/ text\n"}) }, false, true},
		{"no driver", func(r *testRepo) { r.write(map[string]string{".gitattributes": "f.txt filter=up\n"}) }, false, true},
		{"driver", func(r *testRepo) {
			r.write(map[string]string{".gitattributes": "f.txt filter=up\n"})
			r.git("config", "filter.up.clean", "tr a-z A-Z")
		}, false, false},
		{"repository's", func(r *testRepo) {
			r.write(map[string]string{"attrs/a": "f.txt text\n"})
			r.git("config", "core.attributesFile", "attrs/a")
		}, false, false},
		{"repository's absolute", func(r *testRepo) {
			r.write(map[string]string{"attrs/a": "f.txt text\n"})
			r.git("config", "core.attributesFile", filepath.Join(r.dir, "attrs", "a"))
		}, false, false},
		{"repository's in place of the user's", func(r *testRepo) {
			r.write(map[string]string{"attrs/a": "x.bin text\n"})
			r.git("config", "core.attributesFile", "attrs/a")
		}, true, true},
		{"repository's missing", func(r *testRepo) { r.git("config", "core.attributesFile", "attrs/none") }, true, true},
		{"repository's /dev/null", func(r *testRepo) { r.git("config", "core.attributesFile", os.DevNull) }, true, true},
		{"repository's outside", func(r *testRepo) {
			// Unrelated, but out of reach after confinement, with
			// rules unknown.
			name := filepath.Join(t.TempDir(), "attributes")
			if err := os.WriteFile(name, []byte("x.bin text\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			r.git("config", "core.attributesFile", name)
		}, false, false},
		{"autocrlf unset", func(r *testRepo) {
			r.git("config", "core.autocrlf", "true")
			r.write(map[string]string{".gitattributes": "f.txt -text\n"})
		}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestRepo(t, SHA1)
			r.write(map[string]string{"sub/deep/f.txt": "f\n"})
			r.commit()
			settle(t, r)
			tc.setup(r)
			var global GlobalIgnore
			if tc.global {
				global.userAttributes = "f.txt text\n"
			}
			tree := openWorkTreeTest(t, r, global)
			if _, got := tree.ContentID("sub/deep/f.txt"); got != tc.want {
				t.Errorf("ID: %v, want %v", got, tc.want)
			}
		})
	}

	// A file committed with CRLF line endings as text has a blob with LF
	// ones: it differs from HEAD's on disk though unchanged since staged.
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{".gitattributes": "crlf.txt text\n"})
	r.commit()
	r.write(map[string]string{"crlf.txt": "a\r\nb\r\n"})
	r.commit()
	if blob := r.git("cat-file", "blob", "HEAD:crlf.txt"); blob != "a\nb\n" {
		t.Fatalf("blob %q", blob)
	}
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	s := scan.Start(headRoot(t, r), tree, scan.Options{Compare: scan.DefaultAttrs &^ (scan.Owner | scan.Mtime)}, nil)
	s.Wait()
	if got := s.Root.Find("crlf.txt").State; got != scan.Differ {
		t.Errorf("CRLF file: %c, want |", got.Mark())
	}

	// Its blob was filtered as staged by attributes since removed: its
	// size, not its own, tells.
	r.git("rm", "-q", "--cached", ".gitattributes")
	if err := os.Remove(filepath.Join(r.dir, ".gitattributes")); err != nil {
		t.Fatal(err)
	}
	tree = openWorkTreeTest(t, r, GlobalIgnore{})
	if _, ok := tree.ContentID("crlf.txt"); ok {
		t.Error("ID of a file filtered by attributes since removed")
	}
	s = scan.Start(headRoot(t, r), tree, scan.Options{Compare: scan.DefaultAttrs &^ (scan.Owner | scan.Mtime)}, nil)
	s.Wait()
	if got := s.Root.Find("crlf.txt").State; got != scan.Differ {
		t.Errorf("CRLF file without attributes: %c, want |", got.Mark())
	}
}

// headRoot returns the tree of r's HEAD.
func headRoot(t *testing.T, r *testRepo) *Root {
	t.Helper()
	return NewTargetRoot(openTest(t, r, "").Repo, Target{ID: r.id("HEAD^{tree}"), Mode: ModeTree, Commit: r.id("HEAD")}, "HEAD")
}

// TestWorkTreeStatusReload checks that a rescan finds HEAD again, after a
// commit with its refs packed.
func TestWorkTreeStatusReload(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"f": "one\n"})
	r.commit()
	r.git("pack-refs", "--all")
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	r.write(map[string]string{"f": "two\n"})
	r.commit()
	r.git("pack-refs", "--all")
	reloaded, err := tree.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if got := status(t, reloaded.(*WorkTree), "f", true); got != "" {
		t.Errorf("status after a commit = %q", got)
	}
}

// TestWorkTreeStatusReloadAfterGC checks that a rescan finds HEAD again,
// after a commit whose objects git gc packed and pruned.
func TestWorkTreeStatusReloadAfterGC(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"f": "one\n"})
	r.commit()
	r.git("gc", "-q")
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	r.write(map[string]string{"f": "two\n"})
	r.commit()
	r.git("gc", "-q", "--prune=now")
	reloaded, err := tree.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if got := status(t, reloaded.(*WorkTree), "f", true); got != "" {
		t.Errorf("status after a commit and git gc = %q", got)
	}
}

// TestWorkTreeStatusCanceled checks that reading a file to hash it stops
// once canceled.
func TestWorkTreeStatusCanceled(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"f": "one\n"})
	r.commit()
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(r.dir, "f"), future, future); err != nil {
		t.Fatal(err)
	}
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	e, err := tree.Entry("f", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tree.Status(ctx, "f", &e); !errors.Is(err, context.Canceled) {
		t.Errorf("status when canceled: %v", err)
	}
}

// TestWorkTreeRecorded checks that, compared with a revision before them,
// entries staged for deletion or added and then deleted from disk are shown
// for their statuses, on neither side.
func TestWorkTreeRecorded(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"keep": "k\n"})
	r.commit()
	base := r.id("HEAD")
	r.write(map[string]string{"later": "l\n", "dir/f": "f\n"})
	r.commit()
	r.git("rm", "-q", "later", "dir/f")
	r.write(map[string]string{"ad": "a\n"})
	r.git("add", "ad")
	if err := os.Remove(filepath.Join(r.dir, "ad")); err != nil {
		t.Fatal(err)
	}
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	old := NewTargetRoot(openTest(t, r, "").Repo, Target{ID: r.id("HEAD~^{tree}"), Mode: ModeTree, Commit: base}, "base")
	s := scan.Start(old, tree, scan.Options{}, nil)
	s.Wait()
	for p, want := range map[string]string{"later": "D ", "dir/f": "D ", "ad": "AD", "dir": ""} {
		n := s.Root.Find(p)
		if n == nil {
			t.Errorf("%s: no entry", p)
			continue
		}
		if n.Gone == scan.Missing || n.Status.String() != want {
			t.Errorf("%s: gone %v, status %q, want %q", p, n.Gone, n.Status.String(), want)
		}
	}
	if n := s.Root.Find("keep"); n.Gone != scan.Missing {
		t.Error("keep is gone")
	}
}

// TestReadGlobalIgnoreSystem checks that the system's configuration is read
// unless GIT_CONFIG_NOSYSTEM is set, and that attributes files outside the
// repository are found.
func TestReadGlobalIgnoreSystem(t *testing.T) {
	dir := t.TempDir()
	system := filepath.Join(dir, "gitconfig")
	attributes := filepath.Join(dir, "attributes")
	exclude := filepath.Join(dir, "exclude")
	for name, data := range map[string]string{
		system:     "[core]\n\texcludesFile = " + exclude + "\n\tattributesFile = " + attributes + "\n",
		attributes: "f.txt text\n",
		exclude:    "*.o\n",
	} {
		if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(env map[string]string) GlobalIgnore {
		t.Helper()
		env["GIT_ATTR_NOSYSTEM"] = "1"
		env["XDG_CONFIG_HOME"] = filepath.Join(dir, "none")
		g, err := ReadGlobalIgnore(func(key string) string { return env[key] })
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	g := read(map[string]string{"GIT_CONFIG_SYSTEM": system})
	if g.text != "*.o\n" || g.userAttributes != "f.txt text\n" {
		t.Errorf("with the system's configuration: %q, attributes %q", g.text, g.userAttributes)
	}
	g = read(map[string]string{"GIT_CONFIG_SYSTEM": system, "GIT_CONFIG_NOSYSTEM": "1"})
	if g.text != "" || g.userAttributes != "" {
		t.Errorf("without it: %q, attributes %q", g.text, g.userAttributes)
	}
}

// TestWorkTreeRecordedBelowFile checks that, compared with a revision with
// neither, the entries of a directory a staged file replaced are shown for
// their statuses below it, also once the file is deleted from disk.
func TestWorkTreeRecordedBelowFile(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"keep": "k\n"})
	r.commit()
	base := r.id("HEAD")
	r.write(map[string]string{"dir/old": "o\n"})
	r.commit()
	r.git("rm", "-q", "dir/old")
	r.write(map[string]string{"dir": "file\n"})
	r.git("add", "dir")
	for _, deleted := range []bool{false, true} {
		if deleted {
			if err := os.Remove(filepath.Join(r.dir, "dir")); err != nil {
				t.Fatal(err)
			}
		}
		want := gitStatus(t, r)
		tree := openWorkTreeTest(t, r, GlobalIgnore{})
		old := NewTargetRoot(openTest(t, r, "").Repo, Target{ID: r.id("HEAD~^{tree}"), Mode: ModeTree, Commit: base}, "base")
		s := scan.Start(old, tree, scan.Options{}, nil)
		s.Wait()
		for _, p := range []string{"dir", "dir/old"} {
			n := s.Root.Find(p)
			if n == nil {
				t.Errorf("deleted %v: no %s", deleted, p)
			} else if got := n.Status.String(); got != want[p] {
				t.Errorf("deleted %v: %s has %q, git gives %q", deleted, p, got, want[p])
			}
		}
	}
}

// TestWorkTreeDirectoryReplacingFile checks that a directory staged in
// place of a file in HEAD has the file's deletion, as git status gives it.
func TestWorkTreeDirectoryReplacingFile(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"dir": "file\n"})
	r.commit()
	if err := os.Remove(filepath.Join(r.dir, "dir")); err != nil {
		t.Fatal(err)
	}
	r.write(map[string]string{"dir/new": "n\n"})
	r.git("add", "-A")
	want := gitStatus(t, r)
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	for _, p := range []string{"dir", "dir/new"} {
		if got := status(t, tree, p, true); got != want[p] {
			t.Errorf("%s: %q, git gives %q", p, got, want[p])
		}
	}
	s := scan.Start(headRoot(t, r), tree, scan.Options{}, nil)
	s.Wait()
	if n := s.Root.Find("dir"); n.Status.String() != "D " {
		t.Errorf("scanned: %q", n.Status.String())
	}
}

// settle dates r's work tree files an hour back and refreshes the index's
// stat data of them, as for files staged well before the index was last
// written, which aren't read to tell they are unchanged.
func settle(t *testing.T, r *testRepo) {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	err := filepath.WalkDir(r.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Name() == ".git" {
			if err == nil {
				err = filepath.SkipDir
			}
			return err
		}
		if d.Type().IsRegular() {
			return os.Chtimes(p, past, past)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Exits 1 with files changed since staged, which stay so.
	_ = r.command("update-index", "-q", "--refresh").Run()
}

// TestWorkTreeStatusSameSecond checks that a file modified in the second
// the index was written, though before it, is read, as Git reads it.
func TestWorkTreeStatusSameSecond(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"f": "one\n"})
	r.commit()
	// The file modified at a second's start, and its stat data staged.
	sec := time.Unix(time.Now().Add(-time.Hour).Unix(), 0)
	if err := os.Chtimes(filepath.Join(r.dir, "f"), sec, sec); err != nil {
		t.Fatal(err)
	}
	_ = r.command("update-index", "-q", "--refresh").Run()
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	if _, ok := tree.ContentID("f"); !ok {
		t.Fatal("no ID of a file staged a second before the index was written")
	}
	// The index written later in the same second.
	later := sec.Add(900 * time.Millisecond)
	if err := os.Chtimes(filepath.Join(r.dir, ".git", "index"), later, later); err != nil {
		t.Fatal(err)
	}
	tree = openWorkTreeTest(t, r, GlobalIgnore{})
	if _, ok := tree.ContentID("f"); ok {
		t.Error("ID of a file modified in the second the index was written")
	}
}

// TestWorkTreeStatusSameSecondFiltered checks that a file Git filters as
// it stages it, modified in the second the index was written though
// before it, is unchanged, as Git finds it filtered, which its unfiltered
// bytes can't tell, and that one attributes no longer filter is read, as
// Git reads it.
func TestWorkTreeStatusSameSecondFiltered(t *testing.T) {
	for _, tc := range []struct{ attributes, want string }{
		{"f.txt text\n", ""},
		{"x.bin text\n", " M"},
	} {
		t.Run(tc.attributes, func(t *testing.T) {
			r := newTestRepo(t, SHA1)
			r.write(map[string]string{".gitattributes": "f.txt text\n", "f.txt": "one\r\ntwo\r\n"})
			r.commit()
			sec := time.Unix(time.Now().Add(-time.Hour).Unix(), 0)
			if err := os.Chtimes(filepath.Join(r.dir, "f.txt"), sec, sec); err != nil {
				t.Fatal(err)
			}
			_ = r.command("update-index", "-q", "--refresh").Run()
			// The stat data staged, the attributes changed.
			r.write(map[string]string{".gitattributes": tc.attributes})
			later := sec.Add(100 * time.Millisecond)
			if err := os.Chtimes(filepath.Join(r.dir, ".git", "index"), later, later); err != nil {
				t.Fatal(err)
			}
			got := status(t, openWorkTreeTest(t, r, GlobalIgnore{}), "f.txt", true)
			if git := gitStatus(t, r)["f.txt"]; got != tc.want || git != tc.want {
				t.Errorf("status = %q, git %q, want %q", got, git, tc.want)
			}
		})
	}
}
