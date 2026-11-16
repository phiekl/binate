// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// showRepo is historyRepo with commits of messages as git keeps them
// verbatim: blank lines around, tabs, after wide characters too, spaces
// at the ends of lines, a subject in two lines, and none at all.
func showRepo(t *testing.T, f *Format) *testRepo {
	r := historyRepo(t, f)
	for i, msg := range []string{
		"\n\nSubject\n\n\tindented body\nmore  \n漢字\tx y\t\n\n\n",
		"A subject\nin two lines\n\nbody\n",
		"",
	} {
		file := filepath.Join(t.TempDir(), "msg")
		if err := os.WriteFile(file, []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		r.write(map[string]string{"m": strings.Repeat("m", i+1)})
		r.git("add", "-A")
		r.git("commit", "-q", "--cleanup=verbatim", "--allow-empty-message", "-F", file)
	}
	return r
}

func TestShow(t *testing.T) {
	for _, f := range []*Format{SHA1, SHA256} {
		t.Run(f.name, func(t *testing.T) {
			r := showRepo(t, f)
			repo := r.open()
			for _, rev := range strings.Fields(r.git("rev-list", "--all")) {
				id := r.id(rev)
				want := r.git("-c", "log.mailmap=false", "show", "--no-patch", "--no-decorate", "--format=medium", rev)
				got, err := repo.Show(id)
				if err != nil {
					t.Fatalf("%s: %v", rev, err)
				}
				if got != want {
					t.Errorf("%s:\ngot  %q\nwant %q", rev, got, want)
				}
				if got, want := repo.Abbrev(id), strings.TrimSpace(r.git("rev-parse", "--short", rev)); got != want {
					t.Errorf("Abbrev(%s) = %s, want %s", rev, got, want)
				}
			}
		})
	}
}

func TestSubject(t *testing.T) {
	for msg, want := range map[string]string{
		"":                           "",
		"one\n":                      "one",
		"\n\n  two  \nlines\n\nbody": "two lines",
	} {
		if got := (&CommitInfo{Message: msg}).Subject(); got != want {
			t.Errorf("Subject of %q = %q, want %q", msg, got, want)
		}
	}
}

func TestChangeTrees(t *testing.T) {
	r := historyRepo(t, SHA1)
	repo := r.open()
	head := r.id("HEAD")
	root := NewTargetRoot(repo, Target{ID: r.id("HEAD^{tree}"), Mode: ModeTree, Commit: head}, "HEAD")
	for _, tc := range []struct {
		rev, before string
		parents     int
	}{
		{"HEAD", "HEAD^", 2},
		{"HEAD^", "HEAD^^", 1},
		{"ROOT", "", 0},
	} {
		if tc.rev == "ROOT" {
			tc.rev = strings.TrimSpace(r.git("rev-list", "--max-parents=0", "HEAD"))
		}
		id := r.id(tc.rev)
		before, after, parents, err := root.ChangeTrees(id.String())
		if err != nil {
			t.Fatalf("%s: %v", tc.rev, err)
		}
		if parents != tc.parents {
			t.Errorf("%s: %d parents, want %d", tc.rev, parents, tc.parents)
		}
		abbrev := repo.Abbrev(id)
		if got := after.Path("a"); got != abbrev+":a" {
			t.Errorf("%s: after is %s", tc.rev, got)
		}
		wantNames := strings.Fields(r.git("ls-tree", "--name-only", tc.rev))
		if names, err := after.ReadDir(""); err != nil || strings.Join(names, " ") != strings.Join(wantNames, " ") {
			t.Errorf("%s: after has %q, %v; want %q", tc.rev, names, err, wantNames)
		}
		if h, ok := after.(*Root); !ok || !h.HasHistory() {
			t.Errorf("%s: after has no history", tc.rev)
		}
		names, err := before.ReadDir("")
		if tc.before == "" {
			if err != nil || len(names) != 0 || before.Path("") != "(no parent)" {
				t.Errorf("%s: before is %s with %q, %v", tc.rev, before.Path(""), names, err)
			}
			if _, err := before.Entry("a", false); err == nil {
				t.Errorf("%s: a is before a commit without parents", tc.rev)
			}
			continue
		}
		wantNames = strings.Fields(r.git("ls-tree", "--name-only", tc.before))
		if err != nil || strings.Join(names, " ") != strings.Join(wantNames, " ") {
			t.Errorf("%s: before has %q, %v; want %q", tc.rev, names, err, wantNames)
		}
		if got := before.Path("a"); got != abbrev+"^:a" {
			t.Errorf("%s: before is %s", tc.rev, got)
		}
	}
	if _, _, _, err := root.ChangeTrees(r.id("HEAD^{tree}").String()); err == nil {
		t.Error("a tree's trees as a commit's")
	}
}
