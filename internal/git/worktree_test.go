// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"cmp"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
)

func TestWorkTreeVisibility(t *testing.T) {
	for _, tc := range []struct {
		format  *Format
		version int
	}{{SHA1, 2}, {SHA1, 3}, {SHA256, 4}} {
		t.Run(tc.format.name+"/v"+strconv.Itoa(tc.version), func(t *testing.T) {
			r := newTestRepo(t, tc.format)
			long := "long/" + strings.Repeat("x", 180)
			r.write(map[string]string{
				"ignored/tracked": "old",
				"removed":         "old",
				long:              "long compressed prefix",
				"long/y":          "shorter path",
			})
			r.commit()
			r.git("rm", "--cached", "removed")
			if tc.version == 3 {
				r.git("update-index", "--skip-worktree", "long/y")
			}
			r.git("update-index", "--index-version="+strconv.Itoa(tc.version))
			data, err := os.ReadFile(filepath.Join(r.dir, ".git", "index"))
			if err != nil {
				t.Fatal(err)
			}
			if got := binary.BigEndian.Uint32(data[4:]); int(got) != tc.version {
				t.Fatalf("index version = %d, want %d", got, tc.version)
			}
			r.write(map[string]string{
				".git/info/exclude":         "repo-only\n*.override\n",
				".gitignore":                "\ufeffignored/\r\n!ignored/new\r\n*.log\nremoved\n/anchored\nspace\\ \n\\!literal\n\\#literal\n**/generated/**/item[[:digit:]]\n!keep.override\n",
				"ignored/.gitignore":        "!new\n",
				"ignored/tracked":           "changed on disk",
				"ignored/new":               "hidden by ancestor",
				"removed":                   "ignored after git rm --cached",
				"global-only":               "hidden",
				"repo-only":                 "hidden",
				"keep.override":             "root ignore overrides repository and global",
				"skip.override":             "hidden",
				"anchored":                  "hidden",
				"sub/anchored":              "not at the root",
				"sub/.gitignore":            "!keep.log\n",
				"sub/keep.log":              "nested negation",
				"sub/skip.log":              "hidden",
				"sub/generated/item3":       "hidden",
				"sub/generated/deep/item4":  "hidden",
				"sub/generated/deep/itemx":  "not in the character class",
				"sub/generated/deep/item33": "too many digits",
				"empty-dir/":                "",
				"only-ignored/skip.log":     "",
				"space ":                    "hidden",
				"!literal":                  "hidden",
				"#literal":                  "hidden",
			})
			user := t.TempDir()
			globalPath := filepath.Join(user, "git", "ignore")
			if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(globalPath, []byte("global-only\n*.override\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			global, err := ReadGlobalIgnore(func(key string) string {
				if key == "XDG_CONFIG_HOME" {
					return user
				}
				return ""
			})
			if err != nil {
				t.Fatal(err)
			}
			tree := openWorkTreeTest(t, r, global)
			got := workFiles(t, tree)
			all := r.git("-c", "core.excludesFile="+globalPath, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
			want := strings.Split(strings.TrimSuffix(all, "\x00"), "\x00")
			slices.Sort(want)
			want = slices.Compact(want)
			if !slices.Equal(got, want) {
				t.Errorf("visible paths = %q, git shows %q", got, want)
			}
			names, err := tree.ReadDir("")
			if err != nil || slices.Contains(names, "empty-dir") || slices.Contains(names, "only-ignored") {
				t.Errorf("work tree root = %q, %v", names, err)
			}
		})
	}
}

func TestWorkTreeReload(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"tracked": "old"})
	r.commit()
	r.write(map[string]string{".gitignore": "tracked\n"})
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	if !slices.Contains(workFiles(t, tree), "tracked") {
		t.Fatal("tracked file hidden")
	}
	r.git("rm", "--cached", "tracked")
	r.write(map[string]string{
		".git/info/exclude": "new\n",
		".gitignore":        "tracked\nnotes\n",
		"new":               "hidden by info/exclude",
		"notes":             "hidden by .gitignore",
	})
	reloaded, err := tree.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if got := workFiles(t, reloaded); !slices.Equal(got, []string{".gitignore"}) {
		t.Errorf("reloaded paths = %q", got)
	}
	// The top is stat'ed as the scan starts, while another goroutine may
	// load the index.
	concurrent, err := tree.Reload()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = concurrent.ReadDir("") })
	if _, err := concurrent.Stat(""); err != nil {
		t.Errorf("top stat = %v", err)
	}
	wg.Wait()
	r.write(map[string]string{".git/index": "DIRC broken"})
	broken, err := tree.Reload()
	if err != nil {
		t.Fatalf("reload read the index: %v", err)
	}
	if _, err := broken.Stat(""); err != nil {
		t.Errorf("top stat = %v", err)
	}
	if _, err := broken.ReadDir(""); err == nil || !strings.Contains(err.Error(), "invalid index") {
		t.Errorf("listing with a broken index = %v", err)
	}
}

func TestWorkTreeConfinement(t *testing.T) {
	r := newTestRepo(t, SHA1)
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("must-show\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.write(map[string]string{
		"link":                "->" + outside,
		"sub/.gitignore":      "->" + secret,
		"sub/link-to-storage": "->../.git",
	})
	r.commit()
	r.write(map[string]string{"sub/must-show": "local content"})
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	if got := workFiles(t, tree); !slices.Contains(got, "sub/must-show") {
		t.Errorf("symlinked .gitignore took effect: %q", got)
	}
	if entry, err := tree.Entry("link", false); err != nil || entry.Target != outside {
		t.Errorf("symlink entry = %+v, %v", entry, err)
	}
	for _, name := range []string{"link", "link/secret", "../outside", ".git/HEAD", "sub/link-to-storage/HEAD"} {
		if f, err := tree.Open(name, true); err == nil {
			_ = f.Close()
			t.Errorf("opened %q", name)
		}
	}
	if _, err := tree.ReadDir("link"); err == nil {
		t.Error("listed through a symlink")
	}
}

func TestWorkTreeUnsupportedIndexes(t *testing.T) {
	for _, typ := range []string{"split", "sparse"} {
		t.Run(typ, func(t *testing.T) {
			r := newTestRepo(t, SHA1)
			r.write(map[string]string{"first/f": "a", "second/f": "b"})
			r.commit()
			switch typ {
			case "split":
				r.git("update-index", "--split-index")
			case "sparse":
				r.git("sparse-checkout", "set", "--cone", "--sparse-index", "first")
			}
			repo := openTest(t, r, "")
			tree, err := repo.OpenWorkTree(GlobalIgnore{})
			if err == nil {
				_ = tree.Close()
				t.Fatal("accepted an unsupported index")
			}
			if !strings.Contains(err.Error(), typ+" index is not supported") {
				t.Errorf("error = %v", err)
			}
		})
	}
}

func TestWorkTreeSubmodule(t *testing.T) {
	sub := newTestRepo(t, SHA1)
	sub.write(map[string]string{"data": "one"})
	one := sub.commit()
	r := newTestRepo(t, SHA1)
	r.git("-c", "protocol.file.allow=always", "submodule", "add", sub.dir, "sub")
	r.commit()
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	entry, err := tree.Entry("sub", false)
	if err != nil || entry.Mode != source.ModeGitlink || entry.Target != one.String() {
		t.Errorf("gitlink = %+v, %v", entry, err)
	}
	if _, err := tree.Stat("sub/data"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("submodule file stat = %v", err)
	}
	// One with no commits compares as its index entry, as in Git.
	empty := &testRepo{t: t, dir: filepath.Join(r.dir, "empty"), format: SHA1}
	r.write(map[string]string{"empty/": ""})
	empty.git("init", "-q")
	r.git("update-index", "--add", "--cacheinfo", "160000,"+one.String()+",empty")
	if r.git("diff", "--name-only") != "" {
		t.Fatal("git sees a change in the submodule with no commits")
	}
	entry, err = openWorkTreeTest(t, r, GlobalIgnore{}).Entry("empty", false)
	if err != nil || entry.Mode != source.ModeGitlink || entry.Target != one.String() {
		t.Errorf("gitlink with no commits = %+v, %v", entry, err)
	}
	r.git("submodule", "deinit", "-f", "--", "sub")
	entry, err = tree.Entry("sub", false)
	if err != nil || entry.Mode != source.ModeGitlink || entry.Target != one.String() {
		t.Errorf("uninitialized gitlink = %+v, %v", entry, err)
	}
	if f, err := tree.Open(".gitmodules", false); err != nil {
		t.Error(err)
	} else {
		defer func() { _ = f.Close() }()
		if data, err := io.ReadAll(f); err != nil || !strings.Contains(string(data), "path = sub") {
			t.Errorf(".gitmodules = %q, %v", data, err)
		}
	}
}

func openWorkTreeTest(t *testing.T, r *testRepo, global GlobalIgnore) *WorkTree {
	t.Helper()
	tree, err := openTest(t, r, "").OpenWorkTree(global)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tree.Close() })
	return tree
}

func workFiles(t *testing.T, root source.Root) []string {
	t.Helper()
	var files []string
	var walk func(string)
	walk = func(dir string) {
		names, err := root.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			rel := path.Join(dir, name)
			st, err := root.Stat(rel)
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode&0o170000 == 0o040000 {
				walk(rel)
			} else {
				files = append(files, rel)
			}
		}
	}
	walk("")
	slices.Sort(files)
	return files
}

func TestWorkTreeUnreadableExcludes(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"tracked": "a"})
	r.commit()
	outside := filepath.Join(t.TempDir(), "exclude")
	if err := os.WriteFile(outside, []byte("*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.write(map[string]string{
		".git/info/exclude": "->" + outside,
		"new":               "untracked",
		"sub/.gitignore":    "*\n",
		"sub/file":          "untracked",
	})
	// Git warns about a .gitignore it can't read and lists the rest.
	if err := os.Chmod(filepath.Join(r.dir, "sub", ".gitignore"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(r.dir, "sub", ".gitignore"), 0o644) })
	unreadable := filepath.Join(t.TempDir(), "ignore")
	if err := os.WriteFile(unreadable, []byte("new\n"), 0); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config")
	for _, exclude := range []string{"../outside", unreadable} {
		if err := os.WriteFile(config, []byte("[core]\n\texcludesFile = "+exclude+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		global, err := ReadGlobalIgnore(func(key string) string {
			if key == "GIT_CONFIG_GLOBAL" {
				return config
			}
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		tree := openWorkTreeTest(t, r, global)
		if got := workFiles(t, tree); !slices.Equal(got, []string{"new", "sub/.gitignore", "sub/file", "tracked"}) {
			t.Errorf("excludes file %s: paths = %q", exclude, got)
		}
	}
}

func TestWorkTreeNestedRepository(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"tracked": "a"})
	r.commit()
	nested := &testRepo{t: t, dir: filepath.Join(r.dir, "vendor", "foo"), format: SHA1}
	if err := os.MkdirAll(nested.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	nested.git("init", "-q")
	nested.write(map[string]string{"data": "one", "build/out": "generated"})
	head := nested.commit()
	r.write(map[string]string{"store/": "", "vendor/separate/": ""})
	separate := &testRepo{t: t, dir: filepath.Join(r.dir, "vendor", "separate"), format: SHA1}
	r.git("init", "-q", "--separate-git-dir="+filepath.Join(r.dir, "store", "separate.git"), separate.dir)
	separate.write(map[string]string{"data": "two"})
	separate.commit()
	r.write(map[string]string{
		".git/info/exclude": "/store/\n",
		// None of these make a repository.
		"vendor/link/.git":             "->elsewhere",
		"vendor/link/file":             "shown",
		"vendor/empty/.git":            "",
		"vendor/empty/file":            "shown",
		"vendor/missing/.git":          "gitdir: nowhere\n",
		"vendor/missing/file":          "shown",
		"vendor/bare-dir/.git/":        "",
		"vendor/bare-dir/file":         "shown",
		"vendor/no-refs/.git/HEAD":     "ref: refs/heads/main\n",
		"vendor/no-refs/.git/objects/": "",
		"vendor/no-refs/file":          "shown",
		"vendor/no-space/.git":         "gitdir:../foo/.git\n",
		"vendor/no-space/file":         "shown",
	})
	// A symlink to the objects is followed, as Git does.
	shared := &testRepo{t: t, dir: filepath.Join(r.dir, "vendor", "shared"), format: SHA1}
	r.write(map[string]string{"vendor/shared/": ""})
	shared.git("init", "-q")
	shared.write(map[string]string{"data": "three"})
	shared.commit()
	objects := filepath.Join(shared.dir, ".git", "objects")
	if err := os.Rename(objects, filepath.Join(r.dir, "store", "objects")); err != nil {
		t.Fatal(err)
	}
	r.write(map[string]string{"vendor/shared/.git/objects": "->../../../store/objects"})
	// As are the refs, where HEAD is resolved.
	linkedRefs := &testRepo{t: t, dir: filepath.Join(r.dir, "vendor", "linked-refs"), format: SHA1}
	r.write(map[string]string{"vendor/linked-refs/": ""})
	linkedRefs.git("init", "-q")
	linkedRefs.write(map[string]string{"data": "four"})
	linkedRefsHead := linkedRefs.commit()
	if err := os.Rename(filepath.Join(linkedRefs.dir, ".git", "refs"), filepath.Join(r.dir, "store", "refs")); err != nil {
		t.Fatal(err)
	}
	r.write(map[string]string{"vendor/linked-refs/.git/refs": "->../../../store/refs"})
	if got := linkedRefs.git("rev-parse", "HEAD"); got != linkedRefsHead.String()+"\n" {
		t.Fatalf("git rev-parse HEAD = %q", got)
	}
	// A .git symlink to a git directory is followed, as Git does.
	symlinked := &testRepo{t: t, dir: filepath.Join(r.dir, "vendor", "symlinked"), format: SHA1}
	r.write(map[string]string{"vendor/symlinked/": ""})
	r.git("init", "-q", "--separate-git-dir="+filepath.Join(r.dir, "store", "symlinked.git"), symlinked.dir)
	symlinked.write(map[string]string{"data": "five"})
	symlinkedHead := symlinked.commit()
	r.write(map[string]string{"vendor/symlinked/.git": "->../../store/symlinked.git"})
	// Through an absolute one too, while it leads beneath the top.
	absolute := &testRepo{t: t, dir: filepath.Join(r.dir, "vendor", "absolute"), format: SHA1}
	r.write(map[string]string{"vendor/absolute/": ""})
	r.git("init", "-q", "--separate-git-dir="+filepath.Join(r.dir, "store", "absolute.git"), absolute.dir)
	absolute.write(map[string]string{"data": "seven"})
	absoluteHead := absolute.commit()
	top, err := filepath.EvalSymlinks(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	r.write(map[string]string{"vendor/absolute/.git": "->" + filepath.Join(top, "store", "absolute.git")})
	// A repository with no commits is one entry, with no commit to show.
	unborn := &testRepo{t: t, dir: filepath.Join(r.dir, "vendor", "unborn"), format: SHA1}
	r.write(map[string]string{"vendor/unborn/data": "six"})
	unborn.git("init", "-q")
	// One of another object format, its refs loose or packed.
	sha256 := &testRepo{t: t, dir: filepath.Join(r.dir, "vendor", "sha256"), format: SHA256}
	r.write(map[string]string{"vendor/sha256/data": "eight"})
	sha256.git("init", "-q", "--object-format=sha256")
	sha256Head := sha256.commit()
	packed := &testRepo{t: t, dir: filepath.Join(r.dir, "vendor", "sha256-packed"), format: SHA256}
	r.write(map[string]string{"vendor/sha256-packed/data": "nine"})
	packed.git("init", "-q", "--object-format=sha256")
	packedHead := packed.commit()
	packed.git("pack-refs", "--all")

	var want []string
	for _, name := range strings.Split(r.git("ls-files", "--cached", "--others", "--exclude-standard"), "\n") {
		if name != "" {
			want = append(want, strings.TrimSuffix(name, "/"))
		}
	}
	slices.Sort(want)
	if !slices.Contains(want, "vendor/foo") || !slices.Contains(want, "vendor/separate") || !slices.Contains(want, "vendor/shared") || !slices.Contains(want, "vendor/linked-refs") || !slices.Contains(want, "vendor/symlinked") || !slices.Contains(want, "vendor/absolute") || !slices.Contains(want, "vendor/unborn") || !slices.Contains(want, "vendor/sha256") || !slices.Contains(want, "vendor/no-space/file") || !slices.Contains(want, "vendor/empty/file") {
		t.Fatalf("git ls-files = %q", want)
	}
	// Before its parent is listed, a nested repository reads as one too.
	fresh := openWorkTreeTest(t, r, GlobalIgnore{})
	if f, err := fresh.Open("vendor/foo/data", false); err == nil {
		_ = f.Close()
		t.Error("opened a file in a nested repository before listing")
	}
	if entry, err := fresh.Entry("vendor/foo", false); err != nil || entry.Mode != source.ModeGitlink {
		t.Errorf("nested repository before listing = %+v, %v", entry, err)
	}
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	if got := workFiles(t, tree); !slices.Equal(got, want) {
		t.Errorf("visible paths = %q, git shows %q", got, want)
	}
	for rel, want := range map[string]ID{
		"vendor/foo": head, "vendor/linked-refs": linkedRefsHead,
		"vendor/symlinked": symlinkedHead, "vendor/unborn": {},
		"vendor/absolute": absoluteHead, "vendor/sha256": sha256Head,
		"vendor/sha256-packed": packedHead,
	} {
		entry, err := tree.Entry(rel, false)
		if err != nil || entry.Mode != source.ModeGitlink || entry.Target != want.String() {
			t.Errorf("nested repository %s = %+v, %v", rel, entry, err)
		}
	}
	if _, err := tree.Stat("vendor/foo/data"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("nested repository file stat = %v", err)
	}
}

func TestWorkTreeFileMode(t *testing.T) {
	for _, scope := range []string{"", "--local", "--worktree"} {
		t.Run(strings.TrimPrefix(cmp.Or(scope, "default"), "--"), func(t *testing.T) {
			r := newTestRepo(t, SHA1)
			r.write(map[string]string{"plain": "a", "script": "*b"})
			r.commit()
			r.write(map[string]string{"new": "*c"})
			for name, mode := range map[string]os.FileMode{"plain": 0o755, "script": 0o644} {
				if err := os.Chmod(filepath.Join(r.dir, name), mode); err != nil {
					t.Fatal(err)
				}
			}
			trusted := scope == ""
			if !trusted {
				if scope == "--worktree" {
					r.git("config", "extensions.worktreeConfig", "true")
				}
				r.git("config", scope, "core.fileMode", "false")
			}
			tree := openWorkTreeTest(t, r, GlobalIgnore{})
			for name, executable := range map[string]bool{"plain": trusted, "script": !trusted, "new": trusted} {
				want := uint32(0o100644)
				if executable {
					want = 0o100755
				}
				if e, err := tree.Entry(name, false); err != nil || e.Mode != want {
					t.Errorf("%s mode = %o, %v, want %o", name, e.Mode, err, want)
				}
			}
			if got := r.git("diff", "HEAD", "--name-only"); (got == "") == trusted {
				t.Errorf("git diff HEAD --name-only = %q", got)
			}
		})
	}
}

func TestWorkTreeIgnoreCase(t *testing.T) {
	for _, ignoreCase := range []bool{false, true} {
		t.Run(strconv.FormatBool(ignoreCase), func(t *testing.T) {
			r := newTestRepo(t, SHA1)
			r.git("config", "core.ignoreCase", strconv.FormatBool(ignoreCase))
			r.write(map[string]string{
				".gitignore": "Build/\n*.tmp\n[!a]x\n[A-C]y\nLiteral\n[[:upper:]]u\n[X]z\n[!X]w\n[XY]v\n[\\Q-R]q\n\xc9e\n",
				"UPPER.TMP":  "", "lower.tmp": "", "build/f": "", "BUILD/f": "",
				"Ax": "", "ax": "", "bx": "",
				"by": "", "BY": "", "dy": "",
				"literal": "", "LITERAL": "", "lu": "", "Lu": "", "1u": "",
				"xz": "", "Xz": "", "xw": "", "Xw": "", "qw": "", "xv": "", "Xv": "", "qq": "", "Rq": "", "\xe9e": "", "\xc9e": "",
			})
			global, err := ReadGlobalIgnore(func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			got := workFiles(t, openWorkTreeTest(t, r, global))
			all := r.git("ls-files", "-z", "--others", "--exclude-standard")
			want := strings.Split(strings.TrimSuffix(all, "\x00"), "\x00")
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("visible paths = %q, git shows %q", got, want)
			}
			if slices.Contains(got, "UPPER.TMP") == ignoreCase {
				t.Errorf("UPPER.TMP visible = %v", !ignoreCase)
			}
		})
	}
}

func TestWorkTreeSkipWorktree(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{
		"first/f":           "a",
		"first/kept":        "kept",
		"second/f":          "b",
		"second/link":       "->f",
		"second/deep/g":     "c",
		"second/.gitignore": "*.log\n",
	})
	r.commit()

	// Outside a sparse checkout, Git compares a skip-worktree file by
	// its index entry whatever is on disk.
	r.git("update-index", "--skip-worktree", "first/kept")
	r.write(map[string]string{"first/kept": "changed"})
	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	if got := readWorkFile(t, tree, "first/kept"); got != "kept" {
		t.Errorf("skip-worktree file = %q", got)
	}
	r.git("update-index", "--no-skip-worktree", "first/kept")
	r.git("checkout", "first/kept")

	r.git("sparse-checkout", "set", "--cone", "--no-sparse-index", "first")
	if _, err := os.Stat(filepath.Join(r.dir, "second")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second checked out: %v", err)
	}
	// Untracked files beside missing ones, which the missing .gitignore
	// still ignores.
	r.write(map[string]string{"second/deep/new.log": "ignored", "second/deep/new": "shown"})
	tree = openWorkTreeTest(t, r, GlobalIgnore{})
	got := workFiles(t, tree)
	all := r.git("ls-files", "-z", "--cached", "--others", "--exclude-standard")
	want := strings.Split(strings.TrimSuffix(all, "\x00"), "\x00")
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("visible paths = %q, git shows %q", got, want)
	}
	if got := readWorkFile(t, tree, "second/f"); got != "b" {
		t.Errorf("missing skip-worktree file = %q", got)
	}
	if e, err := tree.Entry("second/link", false); err != nil || e.Mode != 0o120777 || e.Target != "f" {
		t.Errorf("missing skip-worktree symlink = %+v, %v", e, err)
	}
	if e, err := tree.Entry("second", false); err != nil || e.Mode != 0o40755 {
		t.Errorf("missing directory = %+v, %v", e, err)
	}

	// In a sparse checkout, Git reads a skip-worktree file found on disk.
	r.write(map[string]string{"second/f": "on disk"})
	if got := r.git("diff", "HEAD", "--name-only"); got != "second/f\n" {
		t.Fatalf("git diff HEAD --name-only = %q", got)
	}
	tree = openWorkTreeTest(t, r, GlobalIgnore{})
	if got := readWorkFile(t, tree, "second/f"); got != "on disk" {
		t.Errorf("present skip-worktree file = %q", got)
	}
}

// TestWorkTreeAssumeUnchanged checks that a file marked with git
// update-index --assume-unchanged is compared as the index records it, as
// git diff does, however it changed on disk, but for a .gitignore deleted,
// whose rules Git no longer reads.
func TestWorkTreeAssumeUnchanged(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"mod": "mod\n", "del": "del\n", "exe": "exe\n", "dir/only": "only\n", "kept": "kept\n", ".gitignore": "x.log\n"})
	r.commit()
	r.git("update-index", "--assume-unchanged", "mod", "del", "exe", "dir/only", ".gitignore")
	r.write(map[string]string{"mod": "changed\n", "kept": "changed\n", "x.log": "x\n"})
	for _, name := range []string{"del", "dir", ".gitignore"} {
		if err := os.RemoveAll(filepath.Join(r.dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(r.dir, "exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := r.git("diff", "HEAD", "--name-only"); got != "kept\n" {
		t.Fatalf("git diff HEAD --name-only = %q", got)
	}

	tree := openWorkTreeTest(t, r, GlobalIgnore{})
	all := r.git("ls-files", "-z", "--cached", "--others", "--exclude-standard")
	want := strings.Split(strings.TrimSuffix(all, "\x00"), "\x00")
	slices.Sort(want)
	if got := workFiles(t, tree); !slices.Equal(got, want) {
		t.Errorf("visible paths = %q, git shows %q", got, want)
	}
	if got := readWorkFile(t, tree, "mod"); got != "mod\n" {
		t.Errorf("modified file = %q", got)
	}
	if e, err := tree.Entry("exe", false); err != nil || e.Mode != 0o100644 {
		t.Errorf("file made executable = %+v, %v", e, err)
	}
	s := scan.Start(headRoot(t, r), tree, scan.Options{Compare: scan.DefaultAttrs &^ (scan.Owner | scan.Mtime)}, nil)
	s.Wait()
	for p, want := range map[string]scan.State{
		"mod": scan.Same, "del": scan.Same, "exe": scan.Same, "dir/only": scan.Same, ".gitignore": scan.Same,
		"kept": scan.Differ, "x.log": scan.RightOnly,
	} {
		if n := s.Root.Find(p); n == nil || n.State != want {
			t.Errorf("%s: %+v, want %c", p, n, want.Mark())
		}
	}

	// In a sparse checkout too, where a skip-worktree file on disk is
	// read from it.
	r.git("sparse-checkout", "set", "--cone", "--no-sparse-index")
	if got := r.git("diff", "HEAD", "--name-only", "--", "mod"); got != "" {
		t.Fatalf("git diff HEAD --name-only -- mod = %q", got)
	}
	tree = openWorkTreeTest(t, r, GlobalIgnore{})
	if got := readWorkFile(t, tree, "mod"); got != "mod\n" {
		t.Errorf("modified file in a sparse checkout = %q", got)
	}
}

func readWorkFile(t *testing.T, root source.Root, rel string) string {
	t.Helper()
	f, err := root.Open(rel, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWorkTreeSubmoduleLinkedWorktree(t *testing.T) {
	sub := newTestRepo(t, SHA1)
	sub.write(map[string]string{"data": "one"})
	sub.commit()
	r := newTestRepo(t, SHA1)
	r.git("-c", "protocol.file.allow=always", "submodule", "add", sub.dir, "sub")
	r.git("-C", "sub", "worktree", "add", "-q", "-b", "feature", "../linked")
	linked := &testRepo{t: t, dir: filepath.Join(r.dir, "linked"), format: SHA1}
	linked.write(map[string]string{"data": "two"})
	head := linked.commit()
	r.git("update-index", "--add", "--cacheinfo", "160000,"+head.String()+",linked")
	for _, packed := range []bool{false, true} {
		if packed {
			linked.git("pack-refs", "--all")
		}
		tree := openWorkTreeTest(t, r, GlobalIgnore{})
		entry, err := tree.Entry("linked", false)
		if err != nil || entry.Mode != source.ModeGitlink || entry.Target != head.String() {
			t.Errorf("packed %v: linked work tree gitlink = %+v, %v", packed, entry, err)
		}
	}
}

func TestWorkTreeRepositoryExcludesFile(t *testing.T) {
	user := t.TempDir()
	if err := os.MkdirAll(filepath.Join(user, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(user, "git", "ignore"), []byte("*.user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	global, err := ReadGlobalIgnore(func(key string) string {
		if key == "XDG_CONFIG_HOME" {
			return user
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{
		"rules/repo-ignore": "?.repo\n",
		"rules/link":        "->repo-ignore",
		// Git resolves ".." after a symlink on disk, to actual/ignore.
		"links/jump":    "->../actual/child",
		"links/ignore":  "a.user\n",
		"actual/child/": "",
		"actual/ignore": "?.repo\n",
		"a.user":        "",
		"a.repo":        "",
		"kept":          "",
	})
	for _, tc := range []struct {
		value string
		want  []string
	}{
		{"", []string{"a.repo", "actual/ignore", "kept", "links/ignore", "links/jump", "rules/link", "rules/repo-ignore"}},
		{"rules/repo-ignore", []string{"a.user", "actual/ignore", "kept", "links/ignore", "links/jump", "rules/link", "rules/repo-ignore"}},
		{filepath.Join(r.dir, "rules", "repo-ignore"), []string{"a.user", "actual/ignore", "kept", "links/ignore", "links/jump", "rules/link", "rules/repo-ignore"}},
		// Git opens the file through symlinks.
		{"rules/link", []string{"a.user", "actual/ignore", "kept", "links/ignore", "links/jump", "rules/link", "rules/repo-ignore"}},
		{filepath.Join(r.dir, "rules", "link"), []string{"a.user", "actual/ignore", "kept", "links/ignore", "links/jump", "rules/link", "rules/repo-ignore"}},
		{"links/jump/../ignore", []string{"a.user", "actual/ignore", "kept", "links/ignore", "links/jump", "rules/link", "rules/repo-ignore"}},
		{r.dir + "/links/jump/../ignore", []string{"a.user", "actual/ignore", "kept", "links/ignore", "links/jump", "rules/link", "rules/repo-ignore"}},
	} {
		if tc.value != "" {
			r.git("config", "core.excludesFile", tc.value)
		}
		got := workFiles(t, openWorkTreeTest(t, r, global))
		if !slices.Equal(got, tc.want) {
			t.Errorf("core.excludesFile %q: paths = %q, want %q", tc.value, got, tc.want)
		}
		cmd := r.command("ls-files", "--others", "--exclude-standard")
		cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+user)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if gitWant := strings.Fields(string(out)); !slices.Equal(gitWant, tc.want) {
			t.Errorf("core.excludesFile %q: git shows %q, want %q", tc.value, gitWant, tc.want)
		}
	}
}

func TestWorkTreeUserExcludesFileAfterSymlink(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{
		"links/jump":    "->../actual/child",
		"links/ignore":  "a.user\n",
		"actual/child/": "",
		"actual/ignore": "?.repo\n",
		"a.user":        "",
		"a.repo":        "",
	})
	config := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(config, []byte("[core]\n\texcludesFile = links/jump/../ignore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	global, err := ReadGlobalIgnore(func(key string) string {
		if key == "GIT_CONFIG_GLOBAL" {
			return config
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	got := workFiles(t, openWorkTreeTest(t, r, global))
	if slices.Contains(got, "a.repo") || !slices.Contains(got, "a.user") {
		t.Errorf("paths = %q, want actual/ignore's rules", got)
	}
	if got := expandHome("~/a/../b", "/home/u/"); got != "/home/u/a/../b" {
		t.Errorf("expandHome = %q", got)
	}
}
