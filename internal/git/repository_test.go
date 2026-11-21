// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// noEnv is an environment without git's variables.
func noEnv(string) string { return "" }

func TestDiscover(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"a/b/f": "x"})
	r.commit()
	gitDir := filepath.Join(r.dir, ".git")

	// From the top or below it, the directory searched from is the prefix.
	for _, tc := range []struct{ from, prefix string }{
		{r.dir, ""},
		{filepath.Join(r.dir, "a"), "a"},
		{filepath.Join(r.dir, "a", "b"), "a/b"},
	} {
		if top, err := FindTop(tc.from, noEnv); err != nil || top != r.dir {
			t.Errorf("top from %s: %s, %v", tc.from, top, err)
		}
		loc, err := discover(tc.from, noEnv)
		if err != nil {
			t.Fatal(err)
		}
		if loc.GitDir != gitDir || loc.CommonDir != gitDir || loc.WorkTree != r.dir || loc.Prefix != tc.prefix {
			t.Errorf("from %s: %+v", tc.from, loc)
		}
		if want := []string{filepath.Join(gitDir, "objects")}; !slices.Equal(loc.ObjectDirs, want) {
			t.Errorf("objects %q", loc.ObjectDirs)
		}
	}
	// The search stops at a ceiling, above the directory searched from.
	ceiling := func(k string) string {
		if k == "GIT_CEILING_DIRECTORIES" {
			return r.dir
		}
		return ""
	}
	if _, err := discover(filepath.Join(r.dir, "a"), ceiling); err == nil {
		t.Error("searched past the ceiling")
	}
	// The git directory itself is a bare repository, where binate starts.
	if loc, err := discover(gitDir, noEnv); err != nil || loc.GitDir != gitDir || loc.WorkTree != "" {
		t.Errorf("in .git: %+v, %v", loc, err)
	}
	if _, err := discover(t.TempDir(), noEnv); err == nil {
		t.Error("found a repository outside one")
	}

	// GIT_DIR names the repository, relative to the directory, and no
	// work tree is assumed; not one outside it.
	env := func(dir string) func(string) string {
		return func(k string) string {
			if k == "GIT_DIR" {
				return dir
			}
			return ""
		}
	}
	if loc, err := discover(r.dir, env(".git")); err != nil || loc.GitDir != gitDir || loc.WorkTree != "" {
		t.Errorf("GIT_DIR: %+v, %v", loc, err)
	}
	if _, err := discover(t.TempDir(), env(gitDir)); !errors.Is(err, errOutside) {
		t.Errorf("GIT_DIR outside: %v", err)
	}
}

// Nothing outside the directory searched is read: a repository with a
// directory outside it is refused.
func TestDiscoverOutside(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.commit()
	gitDir := filepath.Join(r.dir, ".git")

	// A linked work tree's git directory, and the objects and refs it
	// shares, are in the main one.
	linked := filepath.Join(t.TempDir(), "linked")
	r.git("worktree", "add", "-q", linked)
	if _, err := discover(linked, noEnv); !errors.Is(err, errOutside) {
		t.Errorf("linked work tree: %v", err)
	}

	// .git as a symlink to a repository elsewhere.
	elsewhere := t.TempDir()
	if err := os.Symlink(gitDir, filepath.Join(elsewhere, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := discover(elsewhere, noEnv); !errors.Is(err, errOutside) {
		t.Errorf(".git a symlink outside: %v", err)
	}

	// An alternate outside, or the objects as a symlink outside.
	alt := newTestRepo(t, SHA1)
	writeAlternates(t, filepath.Join(gitDir, "objects"), filepath.Join(alt.dir, ".git", "objects"))
	if _, err := discover(r.dir, noEnv); !errors.Is(err, errOutside) {
		t.Errorf("alternate outside: %v", err)
	}
	// One missing, inside, is skipped, as git skips it.
	writeAlternates(t, filepath.Join(gitDir, "objects"), filepath.Join(r.dir, "missing"))
	if loc, err := discover(r.dir, noEnv); err != nil || len(loc.ObjectDirs) != 1 {
		t.Errorf("alternate missing: %+v, %v", loc, err)
	}
	if err := os.Remove(filepath.Join(gitDir, "objects", "info", "alternates")); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "objects")
	if err := os.Rename(filepath.Join(gitDir, "objects"), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, filepath.Join(gitDir, "objects")); err != nil {
		t.Fatal(err)
	}
	if _, err := discover(r.dir, noEnv); !errors.Is(err, errOutside) {
		t.Errorf("objects a symlink outside: %v", err)
	}
	// Inside, it is read, and allowed.
	inside := filepath.Join(r.dir, "objects-moved")
	if err := os.Rename(moved, inside); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(gitDir, "objects")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(gitDir, "objects")); err != nil {
		t.Fatal(err)
	}
	loc, err := discover(r.dir, noEnv)
	if err != nil || !slices.Contains(loc.ReadDirs(), filepath.Join(gitDir, "objects")) {
		t.Errorf("objects a symlink inside: %+v, %v", loc, err)
	}
}

func TestDiscoverBare(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.commit()
	bare := filepath.Join(t.TempDir(), "bare.git")
	r.git("clone", "-q", "--bare", r.dir, bare)
	loc, err := discover(bare, noEnv)
	if err != nil || loc.GitDir != bare || loc.WorkTree != "" {
		t.Errorf("%+v, %v", loc, err)
	}
	if _, err := discover(filepath.Join(bare, "refs"), noEnv); err == nil {
		t.Error("found a bare repository from below it")
	}
}

func TestDiscoverGitFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	// A git directory apart from the work tree, inside it or not.
	r := &testRepo{t: t, dir: t.TempDir(), format: SHA1}
	sep := filepath.Join(r.dir, "sep.git")
	r.git("init", "-q", "--separate-git-dir="+sep)
	loc, err := discover(r.dir, noEnv)
	if err != nil || loc.GitDir != sep || loc.WorkTree != r.dir {
		t.Errorf("%+v, %v", loc, err)
	}
	r = &testRepo{t: t, dir: t.TempDir(), format: SHA1}
	r.git("init", "-q", "--separate-git-dir="+filepath.Join(t.TempDir(), "sep.git"))
	if _, err := discover(r.dir, noEnv); !errors.Is(err, errOutside) {
		t.Errorf("git directory outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, ".git"), []byte("not a gitfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := discover(r.dir, noEnv); err == nil {
		t.Error("read an invalid gitfile")
	}
}

func TestParseConfig(t *testing.T) {
	data := "\ufeff# comment\n" +
		"[core]\n" +
		"\trepositoryFormatVersion = 1 ; comment\n" +
		"\tbare\n" +
		"[Extensions]\n" +
		"  objectFormat = \"sha256\"\n" +
		"[remote \"Origin \\\"x\\\"\"]\n" +
		"\turl = a\\\n  b # comment\n" +
		"\tname = \" spaced \"  \"out\"\n" +
		"\tesc = a\\tb\\nc\\\\d\\\"\n" +
		"[old.Sub]\n" +
		"\tk = v1\n" +
		"\tk = v2\n"
	c, err := parseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"core.repositoryformatversion": "1",
		"core.bare":                    "",
		"extensions.objectformat":      "sha256",
		`remote.Origin "x".url`:        "a  b",
		`remote.Origin "x".name`:       " spaced   out",
		`remote.Origin "x".esc`:        "a\tb\nc\\d\"",
		"old.sub.k":                    "v2",
	} {
		if got, ok := c.get(name); !ok || got != want {
			t.Errorf("%s: %q, %v", name, got, ok)
		}
	}
	for _, bad := range []string{
		"k = v\n",
		"[core\n",
		"[core \"x]\n",
		"[core]\nk = \"open\n",
		"[core]\nk = \\q\n",
		"[core]\n=v\n",
		"[]\n",
		"[co re]\n",
	} {
		if _, err := parseConfig(bad); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
}

func FuzzParseConfig(f *testing.F) {
	f.Add("[core]\n\trepositoryformatversion = 1\n[extensions]\nobjectformat = sha256\n")
	f.Add("[remote \"o\"]\n\turl = \"a\\\"b\" # c\n")
	f.Fuzz(func(t *testing.T, data string) {
		c, err := parseConfig(data)
		if err != nil {
			return
		}
		for name, values := range c.values {
			if len(values) == 0 || !strings.Contains(name, ".") {
				t.Errorf("%q: %q", name, values)
			}
		}
	})
}

func TestRepositoryFormat(t *testing.T) {
	for _, tc := range []struct {
		config   string
		want     *Format
		reftable bool
	}{
		{"", SHA1, false},
		{"[core]\nrepositoryformatversion = 0\n", SHA1, false},
		{"[core]\nrepositoryformatversion = 1\n[extensions]\nobjectformat = sha256\n", SHA256, false},
		{"[core]\nrepositoryformatversion = 1\n[extensions]\nobjectFormat = SHA1\nrefStorage = files\n", SHA1, false},
		{"[core]\nrepositoryformatversion = 1\n[extensions]\npartialclone = origin\nworktreeconfig\npreciousobjects\nnoop-v1\n", SHA1, false},
		// Version 0 ignores extensions it doesn't know.
		{"[core]\nrepositoryformatversion = 0\n[extensions]\nsomething = x\n", SHA1, false},
		{"[core]\nrepositoryformatversion = 1\n[extensions]\nrefstorage = reftable\n", SHA1, true},
		{"[core]\nrepositoryformatversion = 1\n[extensions]\nobjectformat = sha256\nrefstorage = Reftable\n", SHA256, true},
		{"[core]\nrepositoryformatversion = 2\n", nil, false},
		{"[core]\nrepositoryformatversion = x\n", nil, false},
		{"[core]\nrepositoryformatversion = 1\n[extensions]\nsomething = x\n", nil, false},
		{"[core]\nrepositoryformatversion = 1\n[extensions]\nrefstorage = other\n", nil, false},
		{"[core]\nrepositoryformatversion = 1\n[extensions]\nobjectformat = md5\n", nil, false},
		{"[core]\nrepositoryformatversion = 0\n[extensions]\nobjectformat = sha256\n", nil, false},
	} {
		c, err := parseConfig(tc.config)
		if err != nil {
			t.Fatal(err)
		}
		got, reftable, err := repositoryFormat(c)
		if got != tc.want || reftable != tc.reftable || (err == nil) != (tc.want != nil) {
			t.Errorf("%q: %v, reftable %v, %v", tc.config, got, reftable, err)
		}
	}
}

// openTest opens the test repository, found from its work tree's
// directory sub.
func openTest(t *testing.T, r *testRepo, sub string) *Repository {
	t.Helper()
	loc, err := discover(filepath.Join(r.dir, sub), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := Open(loc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(repo.Close)
	return repo
}

func TestOpenReftable(t *testing.T) {
	r := newTestRepo(t, SHA1)
	if err := os.RemoveAll(filepath.Join(r.dir, ".git")); err != nil {
		t.Fatal(err)
	}
	r.git("init", "-q", "--ref-format=reftable")
	loc, err := discover(r.dir, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(loc); err == nil || !strings.Contains(err.Error(), "reftable") {
		t.Errorf("opened a reftable repository: %v", err)
	}
}

func TestCheckRefName(t *testing.T) {
	for _, ok := range []string{"HEAD", "ORIG_HEAD", "FETCH_HEAD", "refs/heads/main", "refs/tags/v1.0", "refs/heads/a-b/c_d"} {
		if err := checkRefName(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"", "head", "main", "_X", "refs/heads/", "refs//x", "refs/heads/../../config",
		"refs/heads/.hidden", "refs/heads/x.lock", "refs/heads/a b", "refs/heads/a~1",
		"refs/heads/a^", "refs/heads/a:b", "refs/heads/a?", "refs/heads/a*", "refs/heads/a[",
		"refs/heads/a\\b", "refs/heads/a@{1}", "refs/heads/a.", "refs/heads/\x01", "/refs/heads/x",
	} {
		if checkRefName(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParsePackedRefs(t *testing.T) {
	id := strings.Repeat("ab", 20)
	refs, err := parsePackedRefs("# pack-refs with: peeled fully-peeled sorted \n"+
		id+" refs/heads/main\n"+id+" refs/tags/v1\n^"+id+"\n", SHA1)
	if err != nil || len(refs) != 2 {
		t.Fatalf("%v, %v", refs, err)
	}
	for _, bad := range []string{
		id + "\n",
		id + " HEAD\n",
		id + " refs/heads/../x\n",
		"xyz refs/heads/x\n",
		"^xyz\n",
		id + " refs/heads/x\n# late comment\n",
	} {
		if _, err := parsePackedRefs(bad, SHA1); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
}

// revRepo makes a repository with history to resolve revisions in: two
// branches merged, tags, a remote's branches and a symref.
func revRepo(t testing.TB) *testRepo {
	t.Helper()
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"f": "1", "dir/sub/g": "g", "dir/x": "x"})
	r.commit()
	r.git("branch", "-M", "main")
	r.git("tag", "light")
	r.git("tag", "-a", "-m", "annotated", "v1")
	r.git("tag", "-a", "-m", "of a tag", "v1-outer", "v1")
	r.write(map[string]string{"f": "2"})
	r.commit()
	r.git("checkout", "-q", "-b", "side", "HEAD~1")
	r.write(map[string]string{"side": "s"})
	r.commit()
	r.git("checkout", "-q", "main")
	r.git("merge", "-q", "--no-edit", "side")
	r.write(map[string]string{"f": "3"})
	r.commit()
	r.git("update-ref", "refs/remotes/origin/main", "HEAD~1")
	r.git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	r.git("tag", "-a", "-m", "blob", "blobtag", "HEAD:f")
	return r
}

// gitTarget returns what git resolves rev to, as Resolve should: a tree,
// or a blob.
func (r *testRepo) gitTarget(rev string) string {
	r.t.Helper()
	if !strings.Contains(rev, ":") {
		rev += "^{}"
	}
	id := strings.TrimSpace(r.git("rev-parse", "--verify", "-q", rev))
	if strings.TrimSpace(r.git("cat-file", "-t", id)) == "commit" {
		return strings.TrimSpace(r.git("rev-parse", id+"^{tree}"))
	}
	return id
}

func TestResolve(t *testing.T) {
	r := revRepo(t)
	head := r.id("HEAD").String()
	revs := []string{
		"HEAD", "@", "main", "refs/heads/main", "heads/main", "side", "HEAD~", "HEAD~2", "HEAD^", "HEAD^1",
		"HEAD^0", "HEAD~1^2", "@~1", "HEAD^^", "HEAD~0", "light", "v1", "tags/v1", "v1^{}",
		"v1^{commit}", "v1-outer", "v1-outer^{}", "v1-outer^{tag}", "HEAD^{tree}", "HEAD^{commit}", "HEAD^{object}",
		"origin", "origin/main", "remotes/origin/main", "HEAD:", "HEAD:dir", "HEAD:dir/", "HEAD:dir/sub/g",
		"HEAD:f", "HEAD~2:f", "v1:dir/x", "blobtag", "blobtag^{blob}", head, head[:7], head[:4],
		"HEAD:./dir", "HEAD:./dir/../f", "HEAD:./",
	}
	for _, packed := range []bool{false, true} {
		if packed {
			r.git("pack-refs", "--all")
		}
		repo := openTest(t, r, "")
		for _, rev := range revs {
			got, err := repo.Resolve(rev)
			if err != nil {
				t.Errorf("packed %v, %s: %v", packed, rev, err)
				continue
			}
			if want := r.gitTarget(rev); got.ID.String() != want {
				t.Errorf("packed %v, %s: %s, want %s", packed, rev, got.ID, want)
			}
		}
	}

	// ./ and ../ are relative to the directory searched from: a
	// subdirectory, or the top.
	for rev, want := range map[string]string{"HEAD:./x": "HEAD:dir/x", "HEAD:../f": "HEAD:f", "HEAD:../": "HEAD:", "HEAD:./": "HEAD:dir", "HEAD:sub/g": ""} {
		got, err := openTest(t, r, "dir").Resolve(rev)
		if want == "" {
			if err == nil {
				t.Errorf("%s: resolved from the root to %s", rev, got.ID)
			}
			continue
		}
		if err != nil || got.ID.String() != r.gitTarget(want) {
			t.Errorf("from dir, %s: %s, %v", rev, got.ID, err)
		}
	}
	repo := openTest(t, r, "")
	// A path ending in a slash, or a relative one in . or .., names a
	// tree, as in git, which keeps a slash of them.
	for rev, want := range map[string]string{
		"HEAD:./dir/x": "HEAD:dir/x", "HEAD:dir/../f": "", "HEAD:./dir/../f": "HEAD:f", "HEAD:./": "HEAD:", "HEAD:../f": "",
		"HEAD:f/": "", "HEAD:./f/.": "", "HEAD:./f/": "", "HEAD:./dir/x/..": "HEAD:dir", "HEAD:./dir/.": "HEAD:dir",
		"HEAD:./f/x/..": "", "HEAD:./.": "HEAD:",
	} {
		got, err := repo.Resolve(rev)
		if want == "" {
			if err == nil {
				t.Errorf("%s: resolved to %s", rev, got.ID)
			}
			continue
		}
		if err != nil || got.ID.String() != r.gitTarget(want) {
			t.Errorf("%s: %s, %v", rev, got.ID, err)
		}
	}
	if got, err := repo.Resolve("HEAD:f"); err != nil || got.Mode != ModeFile {
		t.Errorf("file: %+v, %v", got, err)
	}
	if got, err := repo.Resolve("HEAD"); err != nil || !got.IsTree() {
		t.Errorf("tree: %+v, %v", got, err)
	}

	for _, rev := range []string{
		"nope", "HEAD~9", "HEAD^2", "HEAD~1^3", "HEAD@{1}", "@{u}", ":f", ":/msg", "HEAD^{/msg}", "HEAD^{nope}",
		"HEAD..main", "HEAD^!", "HEAD^@", "^HEAD", "HEAD:nope", "HEAD:f/x", "HEAD:../x", "v1^{blob}",
		"HEAD^{", "refs/heads/../../config", "HEAD:dir/../f", "HEAD:/f", "HEAD:dir//x", "HEAD^99999999999",
	} {
		if got, err := repo.Resolve(rev); err == nil {
			t.Errorf("resolved %s to %s", rev, got.ID)
		}
	}
	if _, err := repo.Resolve(":f"); !errors.Is(err, errUnsupported) {
		t.Errorf(":f: %v", err)
	}
}

func TestResolveAmbiguous(t *testing.T) {
	r := newTestRepo(t, SHA1)
	// Enough blobs for two to share their first four hex digits.
	dir := t.TempDir()
	var paths []string
	for i := range 1500 {
		p := filepath.Join(dir, strings.Repeat("x", 1+i%5)+string(rune('a'+i%26))+strings.Repeat("y", i/26))
		if err := os.WriteFile(p, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	out := r.git(append([]string{"hash-object", "-w", "--"}, paths...)...)
	seen := map[string]string{}
	var short, one string
	for id := range strings.FieldsSeq(out) {
		if _, dup := seen[id[:4]]; dup && short == "" {
			short = id[:4]
		}
		seen[id[:4]] = id
		one = id
	}
	if short == "" {
		t.Skip("no two blobs share a prefix")
	}
	for _, packed := range []bool{false, true} {
		if packed {
			r.git("repack", "-adq")
		}
		repo := openTest(t, r, "")
		if _, err := repo.Resolve(short); !errors.Is(err, errAmbiguous) {
			t.Errorf("packed %v, %s: %v", packed, short, err)
		}
		if got, err := repo.Resolve(one[:12]); err != nil || got.ID.String() != one {
			t.Errorf("packed %v, %s: %v, %v", packed, one[:12], got.ID, err)
		}
		if got, err := repo.Resolve(one[:11]); err != nil || got.ID.String() != one {
			t.Errorf("packed %v, odd %s: %v, %v", packed, one[:11], got.ID, err)
		}
	}
}

func TestTargetRoot(t *testing.T) {
	r := revRepo(t)
	repo := openTest(t, r, "")
	tree, err := repo.Resolve("HEAD:dir")
	if err != nil {
		t.Fatal(err)
	}
	root := NewTargetRoot(repo.Repo, tree, "HEAD:dir")
	if got := root.Path("sub/g"); got != "HEAD:dir/sub/g" {
		t.Errorf("path %q", got)
	}
	if got := NewTargetRoot(repo.Repo, tree, "main").Path("x"); got != "main:x" {
		t.Errorf("path %q", got)
	}
}

func FuzzResolve(f *testing.F) {
	for _, s := range []string{"HEAD", "@~1", "v1^{}", "HEAD^2~1:dir/x", "main:./f", "origin", "abcd", "HEAD^{/x}"} {
		f.Add(s)
	}
	r := revRepo(f)
	loc, err := discover(r.dir, noEnv)
	if err != nil {
		f.Fatal(err)
	}
	repo, err := Open(loc)
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, rev string) {
		got, err := repo.Resolve(rev)
		if err != nil {
			return
		}
		switch got.Mode {
		case ModeTree, ModeFile, ModeExecutable:
		default:
			t.Errorf("%q: mode %o", rev, got.Mode)
		}
	})
}

// discover finds the repository of dir as binate does: its top, then the
// repository there.
func discover(dir string, getenv func(string) string) (*Location, error) {
	top, err := FindTop(dir, getenv)
	if err != nil {
		return nil, err
	}
	return Discover(top, dir, getenv)
}

func TestDiscoverSymlinkDirectory(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"sub/a": "one\n"})
	r.commit()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(r.dir, "sub"), link); err != nil {
		t.Fatal(err)
	}
	loc, err := discover(link, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if loc.WorkTree != r.dir || loc.Prefix != "sub" {
		t.Fatalf("location: %+v", loc)
	}
	rp, err := Open(loc)
	if err != nil {
		t.Fatal(err)
	}
	defer rp.Close()
	target, err := rp.Resolve("HEAD:./a")
	if err != nil {
		t.Fatal(err)
	}
	if target.ID != r.id("HEAD:sub/a") {
		t.Fatalf("wrong relative path: %+v", target)
	}
}

func TestResolveUppercaseHashes(t *testing.T) {
	for _, f := range []*Format{SHA1, SHA256} {
		t.Run(f.name, func(t *testing.T) {
			r := newTestRepo(t, f)
			r.write(map[string]string{"a": "one\n"})
			r.commit()
			hash := r.id("HEAD").String()
			for _, packed := range []bool{false, true} {
				if packed {
					r.git("repack", "-ad")
				}
				rp := openTest(t, r, "")
				for _, n := range []int{7, 8, len(hash)} {
					rev := strings.ToUpper(hash[:n])
					got, err := rp.Resolve(rev)
					if err != nil || got.Commit.String() != hash {
						t.Errorf("%s: %+v, %v", rev, got, err)
					}
				}
			}
		})
	}
}

func TestConfigBool(t *testing.T) {
	c, err := parseConfig("[core]\n\talone\n\tempty =\n\tquoted = \"\"\n\tyes = Yes\n\tzero = 0\n\ttwo = 2\n\tbad = maybe\n" +
		"\talonethenfalse\n\talonethenfalse = off\n\temptythenalone =\n\temptythenalone\n")
	if err != nil {
		t.Fatal(err)
	}
	later, err := parseConfig("[core]\n\tempty\n\tyes =\n")
	if err != nil {
		t.Fatal(err)
	}
	merged := newConfig()
	merged.merge(c)
	merged.merge(later)
	for _, tc := range []struct {
		c    config
		name string
		want bool
	}{
		{c, "core.alone", true},
		{c, "core.empty", false},
		{c, "core.quoted", false},
		{c, "core.yes", true},
		{c, "core.zero", false},
		{c, "core.two", true},
		{c, "core.unset", true},
		{c, "core.alonethenfalse", false},
		{c, "core.emptythenalone", true},
		{merged, "core.empty", true},
		{merged, "core.yes", false},
		{merged, "core.alone", true},
	} {
		if got, err := tc.c.bool(tc.name, true); err != nil || got != tc.want {
			t.Errorf("%s = %v, %v, want %v", tc.name, got, err, tc.want)
		}
	}
	if _, err := c.bool("core.bad", false); err == nil {
		t.Error("read maybe as a boolean")
	}
}

// TestDiscoverThroughSymlinkedPWD checks that a work tree started in, as
// "." with $PWD naming it through a symlinked parent, as a shell keeps it,
// is found with symlinks resolved, and its git directory within it.
func TestDiscoverThroughSymlinkedPWD(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"sub/a": "one\n"})
	r.commit()
	link := filepath.Join(t.TempDir(), "p")
	if err := os.Symlink(filepath.Dir(r.dir), link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ from, prefix string }{
		{filepath.Join(link, filepath.Base(r.dir)), ""},
		{filepath.Join(link, filepath.Base(r.dir), "sub"), "sub"},
	} {
		t.Chdir(tc.from)
		t.Setenv("PWD", tc.from)
		if wd, err := os.Getwd(); err != nil || wd != tc.from {
			t.Fatalf("working directory %q, %v", wd, err)
		}
		loc, err := discover(".", noEnv)
		if err != nil {
			t.Fatalf("%s: %v", tc.from, err)
		}
		if loc.WorkTree != r.dir || loc.Prefix != tc.prefix {
			t.Errorf("%s: %+v", tc.from, loc)
		}
		rp, err := Open(loc)
		if err != nil {
			t.Fatal(err)
		}
		tree, err := rp.OpenWorkTree(GlobalIgnore{})
		if err != nil {
			t.Errorf("%s: %v", tc.from, err)
		} else {
			_ = tree.Close()
		}
		rp.Close()
	}
}

// TestFindTopDotDotAfterSymlink checks that .. after a symlink leads above
// its target, as Git and the kernel take it, not back above the link.
func TestFindTopDotDotAfterSymlink(t *testing.T) {
	r := newTestRepo(t, SHA1)
	r.write(map[string]string{"sub/a": "one\n"})
	r.commit()
	other := t.TempDir()
	if err := os.Symlink(filepath.Join(r.dir, "sub"), filepath.Join(other, "link")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(other)
	t.Setenv("PWD", other)
	for _, dir := range []string{filepath.Join(other, "link") + "/..", "link/..", "./link/../."} {
		if top, err := FindTop(dir, noEnv); err != nil || top != r.dir {
			t.Errorf("FindTop(%q) = %q, %v, want %q", dir, top, err, r.dir)
		}
		if loc, err := discover(dir, noEnv); err != nil || loc.WorkTree != r.dir || loc.Prefix != "" {
			t.Errorf("discover(%q) = %+v, %v", dir, loc, err)
		}
	}
}
