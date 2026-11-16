// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// statRepo is showRepo with commits of what git's diff stat treats apart:
// binary files, a type and a mode changed, a submodule, names quoted, a
// file and a directory of names sorting around each other, long names and
// many lines, a file made a directory, and an empty commit.
func statRepo(t *testing.T, f *Format) *testRepo {
	r := showRepo(t, f)
	commit := func(msg string, files map[string]string, remove ...string) {
		t.Helper()
		for _, p := range remove {
			if err := os.RemoveAll(filepath.Join(r.dir, p)); err != nil {
				t.Fatal(err)
			}
		}
		r.write(files)
		r.git("add", "-A")
		r.git("commit", "-q", "--allow-empty", "-m", msg)
	}
	many := func(n int, s string) string { return strings.Repeat(s+"\n", n) }
	commit("binary", map[string]string{"bin": "b\x00b", "big": many(300, "x"), "a-b": "1\n", "a.c": "2\n", "sd/a": "3\n"})
	commit("changes", map[string]string{
		"bin": "bb\x00bbb", "x": "->a", "m": "*m\n", "big": many(150, "x") + many(200, "y"),
		"é s": "n\n", "q\"\\\tq": "q\n", "a/b": "4\n",
		"deep/" + strings.Repeat("long-directory-name/", 4) + "file.txt": many(40, "z"),
		"nonl": "no newline",
	}, "a-b", "a")
	commit("file to directory", map[string]string{"nonl/inner": "i\n", "bin2": strings.Repeat("\x00", 9000)}, "nonl", "sd")
	// Modes only, of a binary file and of text.
	for _, p := range []string{"bin", "a/b"} {
		if err := os.Chmod(filepath.Join(r.dir, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	commit("mode only", nil)
	r.git("update-index", "--add", "--cacheinfo", "160000,"+r.id("HEAD").String()+",sub")
	r.git("commit", "-q", "-m", "submodule")
	r.git("update-index", "--cacheinfo", "160000,"+r.id("HEAD~2").String()+",sub")
	r.git("commit", "-q", "-m", "submodule moved")
	commit("empty", nil)
	commit("all gone", nil, "big", "bin", "bin2")
	return r
}

func TestDiffStat(t *testing.T) {
	for _, f := range []*Format{SHA1, SHA256} {
		t.Run(f.name, func(t *testing.T) {
			r := statRepo(t, f)
			repo := r.open()
			root := NewTargetRoot(repo, Target{ID: r.id("HEAD^{tree}"), Mode: ModeTree, Commit: r.id("HEAD")}, "HEAD")
			for _, rev := range strings.Fields(r.git("rev-list", "--all")) {
				render, err := root.ChangeStat(context.Background(), rev)
				if err != nil {
					t.Fatalf("%s: %v", rev, err)
				}
				show, err := repo.Show(r.id(rev))
				if err != nil {
					t.Fatal(err)
				}
				for _, width := range []int{20, 40, 60, 80, 200} {
					want := r.git("-c", "log.mailmap=false", "show", fmt.Sprintf("--stat=%d", width), "--summary",
						"--no-decorate", "--format=medium", "--diff-merges=first-parent", rev)
					got := show
					if lines := render(width); len(lines) > 0 {
						got += "\n" + strings.Join(lines, "\n") + "\n"
					}
					if got != want {
						t.Errorf("%s at %d:\ngot\n%s\nwant\n%s", rev, width, got, want)
					}
				}
			}
		})
	}
}

func TestQuotePath(t *testing.T) {
	for in, want := range map[string]string{
		"a/b c":    "a/b c",
		"é":        `"\303\251"`,
		"q\"\\\t":  `"q\"\\\t"`,
		"\x01\x7f": `"\001\177"`,
	} {
		if got := quotePath(in); got != want {
			t.Errorf("quotePath(%q) = %s, want %s", in, got, want)
		}
	}
}
