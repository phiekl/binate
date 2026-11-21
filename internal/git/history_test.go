// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// historyRepo makes a history of branches and merges: a merge taking one
// side's file, one resolving a conflict anew, a mode changed, a file
// deleted and added again the same, a symlink and an empty commit, each
// commit authored a day after the last.
func historyRepo(t *testing.T, f *Format) *testRepo {
	r := newTestRepo(t, f)
	day := 0
	commit := func(files map[string]string, remove ...string) {
		t.Helper()
		r.write(files)
		for _, p := range remove {
			if err := os.Remove(filepath.Join(r.dir, p)); err != nil {
				t.Fatal(err)
			}
		}
		day++
		r.git("add", "-A")
		r.git("commit", "-q", "--allow-empty", "-m", "c", fmt.Sprintf("--date=2026-01-%02dT12:00:00+0130", day),
			fmt.Sprintf("--author=Author Number %d <author%d@example.org>", day, day))
	}
	commit(map[string]string{"a": "a\n", "d/b": "b\n", "d/e/f": "f\n", "x": "x\n", "y": "y\n", "l": "->a"})
	commit(map[string]string{"d/b": "b2\n"})
	r.git("checkout", "-q", "-b", "side")
	commit(map[string]string{"a": "a side\n", "s": "s\n", "c": "side\n"})
	commit(map[string]string{"d/e/g": "g\n"})
	r.git("checkout", "-q", "-")
	commit(map[string]string{"x": "x2\n", "c": "main\n"})
	commit(map[string]string{"d/e/f": "*f\n"})
	commit(nil, "y")
	// The merge conflicts in c, which fails, resolved as neither side.
	if out, err := r.command("merge", "-q", "--no-commit", "side").CombinedOutput(); !strings.Contains(string(out), "CONFLICT") {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	r.write(map[string]string{"c": "merged\n"})
	commit(nil)
	commit(map[string]string{"y": "y\n"})
	commit(nil)
	r.git("checkout", "-q", "-b", "other", "HEAD~3")
	commit(map[string]string{"o": "o\n"})
	r.git("checkout", "-q", "-")
	r.git("merge", "-q", "--no-edit", "other")
	return r
}

func TestLastChange(t *testing.T) {
	for _, f := range []*Format{SHA1, SHA256} {
		t.Run(f.name, func(t *testing.T) {
			r := historyRepo(t, f)
			repo := r.open()
			head := r.id("HEAD")
			paths := append([]string{""}, strings.Fields(r.git("ls-tree", "-r", "-t", "--name-only", "HEAD"))...)
			for _, p := range paths {
				spec := p
				if spec == "" {
					spec = "."
				}
				want := strings.Fields(r.git("log", "-1", "--format=%H %at %ai %ct %an <%ae>", "--", spec))
				id, c, err := repo.LastChange(context.Background(), head, p)
				if err != nil {
					t.Errorf("%q: %v", p, err)
					continue
				}
				got := []string{id.String(), fmt.Sprint(c.Author.Unix()), c.Author.Format("2006-01-02"), c.Author.Format("15:04:05"), c.Author.Format("-0700"), fmt.Sprint(c.Committer.Unix())}
				got = append(got, strings.Fields(c.AuthorName+" <"+c.AuthorEmail+">")...)
				if strings.Join(got, " ") != strings.Join(want, " ") {
					t.Errorf("%q: got %v, want %v", p, got, want)
				}
			}
			if _, _, err := repo.LastChange(context.Background(), head, "nope"); err == nil {
				t.Error("a path missing: no error")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, _, err := repo.LastChange(ctx, head, "a"); !errors.Is(err, context.Canceled) {
				t.Errorf("canceled: %v", err)
			}
		})
	}
}

func TestTargetRootHistory(t *testing.T) {
	r := historyRepo(t, SHA1)
	rp := openTest(t, r, "")
	for _, tc := range []struct {
		rev, rel string
		history  bool
	}{
		{"HEAD", "d/b", true},
		{"HEAD:d", "e/f", true},
		{"HEAD:./d/e", "f", true},
		{"HEAD^{tree}", "d/b", false},
	} {
		target, err := rp.Resolve(tc.rev)
		if err != nil {
			t.Fatalf("%s: %v", tc.rev, err)
		}
		root := NewTargetRoot(rp.Repo, target, tc.rev)
		if root.HasHistory() != tc.history {
			t.Errorf("%s: history %v", tc.rev, root.HasHistory())
		}
		if !tc.history {
			continue
		}
		full := strings.TrimPrefix(strings.TrimPrefix(tc.rev, "HEAD"), ":")
		full = strings.TrimPrefix(strings.TrimPrefix(full, "./"), "/")
		full = strings.Trim(full+"/"+tc.rel, "/")
		want := strings.TrimSpace(r.git("log", "-1", "--format=%H", "--", full))
		for range 2 {
			ch, err := root.LastChange(context.Background(), tc.rel)
			if err != nil || ch.ID != want {
				t.Errorf("%s %s: %v %v, want %s", tc.rev, tc.rel, ch.ID, err, want)
			}
		}
	}
}

func TestParseSignature(t *testing.T) {
	for _, tc := range []struct {
		sig, name, email, time string
	}{
		{"A B <a@b> 1700000000 +0130", "A B", "a@b", "2023-11-14T23:43:20+01:30"},
		{"A <a@b> 1700000000 -0800", "A", "a@b", "2023-11-14T14:13:20-08:00"},
		{"A <a@b> 1700000000", "A", "a@b", "2023-11-14T22:13:20Z"},
		{"A <a@b> 1700000000 bad", "A", "a@b", "2023-11-14T22:13:20Z"},
		{"A <a@b>", "A", "a@b", "0001-01-01T00:00:00Z"},
		{"A <a@b> x +0000", "A", "a@b", "0001-01-01T00:00:00Z"},
		{"  <> 1700000000 +0000", "", "", "2023-11-14T22:13:20Z"},
		// The email ends at the first ">", the time starts after the last.
		{"A <a@b> c> 1700000000 +0000", "A", "a@b", "2023-11-14T22:13:20Z"},
	} {
		name, email, at, err := parseSignature(tc.sig)
		if err != nil || name != tc.name || email != tc.email || at.Format(time.RFC3339) != tc.time {
			t.Errorf("%q: %q %q %v %v, want %q %q %s", tc.sig, name, email, at.Format(time.RFC3339), err, tc.name, tc.email, tc.time)
		}
	}
	for _, sig := range []string{"A a@b 1700000000 +0000", "A <a@b 1700000000 +0000", "A >a@b< 1"} {
		if _, _, _, err := parseSignature(sig); err == nil {
			t.Errorf("%q: no error", sig)
		}
	}
}

func TestRepoPath(t *testing.T) {
	r := historyRepo(t, SHA1)
	rp := openTest(t, r, "")
	for _, tc := range []struct {
		rev, rel, want string
	}{
		{"HEAD", "d/e/f", "d/e/f"},
		{"HEAD", "", "."},
		{"HEAD:d", "e/f", "d/e/f"},
		{"HEAD:d", "", "d"},
		{"HEAD:./d/e", "f", "d/e/f"},
		// Where a tree named alone is in the repository isn't known.
		{"HEAD^{tree}", "d", ""},
	} {
		target, err := rp.Resolve(tc.rev)
		if err != nil {
			t.Fatalf("%s: %v", tc.rev, err)
		}
		root := NewTargetRoot(rp.Repo, target, tc.rev)
		if p, ok := root.RepoPath(tc.rel); p != tc.want || ok != (tc.want != "") {
			t.Errorf("%s %s: %q %v, want %q", tc.rev, tc.rel, p, ok, tc.want)
		}
	}
}

func TestShallowHistory(t *testing.T) {
	for _, f := range []*Format{SHA1, SHA256} {
		t.Run(f.name, func(t *testing.T) {
			r := newTestRepo(t, f)
			r.write(map[string]string{"a": "one\n"})
			r.commit()
			r.write(map[string]string{"b": "two\n"})
			r.commit()
			dst := filepath.Join(t.TempDir(), "clone")
			r.git("clone", "-q", "--depth=1", "file://"+r.dir, dst)
			loc, err := discover(dst, noEnv)
			if err != nil {
				t.Fatal(err)
			}
			rp, err := Open(loc)
			if err != nil {
				t.Fatal(err)
			}
			defer rp.Close()
			target, err := rp.Resolve("HEAD")
			if err != nil {
				t.Fatal(err)
			}
			id, c, err := rp.LastChange(context.Background(), target.Commit, "a")
			if err != nil {
				t.Fatal(err)
			}
			if id != target.Commit || len(c.Parents) != 0 {
				t.Fatalf("boundary: %s, parents %v", id, c.Parents)
			}
			root := NewTargetRoot(rp.Repo, target, "HEAD")
			before, _, parents, err := root.ChangeTrees(id.String())
			if err != nil {
				t.Fatal(err)
			}
			names, err := before.ReadDir("")
			if err != nil || len(names) != 0 || parents != 0 {
				t.Fatalf("before: %v, %d parents, %v", names, parents, err)
			}
			if _, err := root.ChangeStat(context.Background(), id.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := rp.Resolve("HEAD^"); err == nil {
				t.Fatal("resolved parent past shallow boundary")
			}
		})
	}
}
