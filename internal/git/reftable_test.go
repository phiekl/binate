// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReftableRefs checks that each ref of a nested repository's reftable
// stack is read as git reads it: in blocks after the first, peeled tags,
// symrefs, and refs deleted in a newer table.
func TestReftableRefs(t *testing.T) {
	for _, f := range []*Format{SHA1, SHA256} {
		t.Run(f.name, func(t *testing.T) {
			r := newTestRepo(t, SHA1)
			r.write(map[string]string{"f": "f\n"})
			r.commit()
			inner := &testRepo{t: t, dir: filepath.Join(r.dir, "inner"), format: f}
			r.write(map[string]string{"inner/data": "d\n"})
			inner.git("init", "-q", "--ref-format=reftable", "--object-format="+f.name)
			inner.git("config", "reftable.autoCompaction", "false")
			inner.commit()
			// Enough refs for several blocks, then a table deleting one.
			var updates strings.Builder
			for i := range 600 {
				fmt.Fprintf(&updates, "create refs/heads/branch-with-a-long-name-%d HEAD\n", i)
			}
			cmd := inner.command("update-ref", "--stdin")
			cmd.Stdin = strings.NewReader(updates.String())
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git update-ref: %v\n%s", err, out)
			}
			inner.git("tag", "-a", "-m", "tag", "v1")
			inner.git("pack-refs", "--all")
			inner.commit()
			inner.git("branch", "-D", "-q", "branch-with-a-long-name-7")

			tree := openWorkTreeTest(t, r, GlobalIgnore{})
			want := map[string]string{}
			for line := range strings.Lines(inner.git("for-each-ref", "--format=%(refname) %(objectname)")) {
				name, id, _ := strings.Cut(strings.TrimSpace(line), " ")
				want[name] = id
			}
			if len(want) < 600 {
				t.Fatalf("git lists %d refs", len(want))
			}
			for name, id := range want {
				got, ok, err := tree.reftableRef("inner/.git", name, f)
				if err != nil || !ok || got.id.String() != id || got.target != "" {
					t.Errorf("%s = %+v, %v, %v, want %s", name, got, ok, err, id)
				}
			}
			if got, ok, err := tree.reftableRef("inner/.git", "refs/heads/branch-with-a-long-name-7", f); ok || err != nil {
				t.Errorf("deleted branch = %+v, %v, %v", got, ok, err)
			}
			if got, ok, err := tree.reftableRef("inner/.git", "HEAD", f); !ok || err != nil || got.target != "refs/heads/main" && got.target != "refs/heads/master" {
				t.Errorf("HEAD = %+v, %v, %v", got, ok, err)
			}
			entry, err := tree.Entry("inner", false)
			if head := inner.id("HEAD"); err != nil || entry.Target != head.String() {
				t.Errorf("nested repository = %+v, %v, want %s", entry, err, head)
			}
			if got := status(t, tree, "inner", true); got != "??" {
				t.Errorf("status of the nested repository = %q", got)
			}
		})
	}
}

// TestReftableTruncated checks that no part of a table is read past its
// end, however it is cut short.
func TestReftableTruncated(t *testing.T) {
	r := newTestRepo(t, SHA1)
	if err := os.RemoveAll(filepath.Join(r.dir, ".git")); err != nil {
		t.Fatal(err)
	}
	r.git("init", "-q", "--ref-format=reftable")
	r.commit()
	r.git("tag", "-a", "-m", "tag", "v1")
	r.git("pack-refs", "--all")
	list, err := os.ReadFile(filepath.Join(r.dir, ".git", "reftable", "tables.list"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(r.dir, ".git", "reftable", strings.Fields(string(list))[0]))
	if err != nil {
		t.Fatal(err)
	}
	if refs, err := parseReftable(data, SHA1); err != nil || len(refs) < 3 {
		t.Fatalf("whole table: %v, %v", refs, err)
	}
	if _, err := parseReftable(data, SHA256); err == nil {
		t.Error("read as another object format")
	}
	for n := range len(data) {
		_, _ = parseReftable(data[:n], SHA1)
		corrupt := append([]byte(nil), data...)
		corrupt[n] ^= 0xff
		_, _ = parseReftable(corrupt, SHA1)
	}
}
