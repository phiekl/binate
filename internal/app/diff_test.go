// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/diff"
	"pxy.se/go/binate/internal/scan"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/term"
)

func TestLoadReportsBinarySides(t *testing.T) {
	const binary = "Unable to show data that is not UTF-8 encoded."
	for _, tt := range []struct {
		name, left, right string
		want              [2]string
	}{
		{"text", "left\n", "right\n", [2]string{}},
		{"left binary", "\xff", "right\n", [2]string{binary, ""}},
		{"right binary", "left\n", "\xfe", [2]string{"", binary}},
		{"both binary", "\xff", "\xfe", [2]string{binary, binary}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			source := func(name, content string) Source {
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
				return Source{Root: source.Disk(path)}
			}
			d := NewDiffScreen(nil, DiffConfig{
				Styles: colors.Default(),
				Left:   source("left", tt.left),
				Right:  source("right", tt.right),
			})
			err := d.Load()
			if tt.want == [2]string{} {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, diff.ErrBinary) {
				t.Fatalf("Load = %v, want a binary error", err)
			}
			d.ShowError(err)
			if d.view.SideErrors != tt.want || d.view.Message != "" {
				t.Errorf("side errors %q, message %q, want %q", d.view.SideErrors, d.view.Message, tt.want)
			}
		})
	}
}

// Nothing, as /dev/null given, is a missing side, every time it is
// loaded, though /dev/null is there.
func TestNothingStaysMissing(t *testing.T) {
	left := filepath.Join(t.TempDir(), "l")
	if err := os.WriteFile(left, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDiffScreen(nil, DiffConfig{
		Styles: colors.Default(),
		Left:   Source{Root: source.Disk(left)},
		Right:  Source{Root: source.Nothing("/dev/null"), Missing: true},
	})
	for range 2 {
		if err := d.Load(); err != nil {
			t.Fatal(err)
		}
		if rows := d.view.Rows; len(rows) != 1 || rows[0].Kind != diff.LeftOnly || !d.right.Missing {
			t.Fatalf("rows %+v, right missing %v", rows, d.right.Missing)
		}
	}
}

func TestLoadReadsASideCreatedSince(t *testing.T) {
	dir := t.TempDir()
	left, right := filepath.Join(dir, "l"), filepath.Join(dir, "r")
	if err := os.WriteFile(left, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := NewDiffScreen(nil, DiffConfig{
		Styles: colors.Default(),
		Left:   Source{Root: source.Disk(left)},
		Right:  Source{Root: source.Disk(right), Missing: true},
	})
	if err := d.Load(); err != nil {
		t.Fatal(err)
	}
	if rows := d.view.Rows; len(rows) != 1 || rows[0].Kind != diff.LeftOnly {
		t.Fatalf("rows before %+v", rows)
	}
	// An editor creates the missing side.
	if err := os.WriteFile(right, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.Load(); err != nil {
		t.Fatal(err)
	}
	if rows := d.view.Rows; len(rows) != 1 || rows[0].Kind != diff.Same {
		t.Errorf("rows after %+v", rows)
	}
	if why := d.right.editSide().Why; why != "" {
		t.Errorf("the created side can't be edited: %s", why)
	}
}

func TestLoadShowsWhyAMissingSideCantBeTold(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	left, right := filepath.Join(dir, "l"), filepath.Join(sub, "r")
	for _, p := range []string{left, right} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(sub, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	d := NewDiffScreen(nil, DiffConfig{
		Styles: colors.Default(),
		Left:   Source{Root: source.Disk(left)},
		Right:  Source{Root: source.Disk(right), Missing: true},
	})
	if err := d.Load(); err == nil || err.Error() != "right: permission denied" {
		t.Errorf("Load = %v, want right: permission denied", err)
	}
	if !d.right.Missing {
		t.Error("a failed lookup cleared Missing")
	}
	// Readable again, with the file gone: missing, as before.
	_ = os.Chmod(sub, 0o755)
	if err := os.Remove(right); err != nil {
		t.Fatal(err)
	}
	if err := d.Load(); err != nil {
		t.Errorf("Load after access is back = %v", err)
	}
	if rows := d.view.Rows; len(rows) != 1 || rows[0].Kind != diff.LeftOnly {
		t.Errorf("rows %+v", rows)
	}
	// Still gone, through a path that is no longer a directory: missing.
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	d = NewDiffScreen(nil, DiffConfig{
		Styles: colors.Default(),
		Left:   Source{Root: source.Disk(left)},
		Right:  Source{Root: source.Disk(right), Missing: true},
	})
	if err := d.Load(); err != nil {
		t.Errorf("Load = %v", err)
	}
}

func TestEditRefusals(t *testing.T) {
	a := &App{HasEditor: true}
	for _, tt := range []struct {
		l, r string
		want []string
	}{
		{"a directory", "a directory", []string{"Neither side is a regular file."}},
		{"a directory", "a symlink", []string{"Neither side is a regular file:", "The left side is a directory.", "The right side is a symlink."}},
		{"missing", notRegular, []string{"Neither side is a regular file."}},
		{notOnDisk, inIndex, []string{"Neither side can be edited:", "The left side is in a git revision.", "The right side is in the git index."}},
		{inIndex, "a directory", []string{"Neither side can be edited:", "The left side is in the git index.", "The right side is a directory."}},
	} {
		_, got := a.Edit([2]EditSide{{Why: tt.l}, {Why: tt.r}}, "", nil)
		if !slices.Equal(got, tt.want) {
			t.Errorf("%q, %q: %q, want %q", tt.l, tt.r, got, tt.want)
		}
	}
}

// A side in the index is said to be, not in a revision, and with nothing
// editable, setting an editor wouldn't help.
func TestEditIndex(t *testing.T) {
	if got := whyNotEditable(indexRoot{}, scan.File); got != inIndex {
		t.Errorf("why a file in the index can't be edited = %q", got)
	}
	_, got := (&App{}).Edit([2]EditSide{{Why: notOnDisk}, {Why: inIndex}}, "", nil)
	if len(got) == 0 || got[0] != "Neither side can be edited:" {
		t.Errorf("without an editor: %q", got)
	}
}

// indexRoot is a side in an index.
type indexRoot struct{ source.Root }

func (indexRoot) IsIndex() bool { return true }

func TestEditPopupKeys(t *testing.T) {
	a := &App{HasEditor: true, screen: term.NewScreen(100, 30)}
	e, why := a.Edit([2]EditSide{{Path: "/a/f"}, {Path: "/b/f"}}, "", nil)
	if e == nil {
		t.Fatalf("no popup: %q", why)
	}
	// The left side is selected first; the keys that move through Help
	// move the selection, and other keys are ignored.
	for _, step := range []struct {
		k    term.Key
		side int
	}{
		{term.Rune('j'), 1}, {term.Rune('j'), 1}, {term.Rune('k'), 0}, {term.Key{Code: term.KeyDown}, 1},
		{term.Key{Code: term.KeyUp}, 0}, {term.Rune('G'), 1}, {term.Rune('x'), 1}, {term.Key{Code: term.KeyHome}, 0},
	} {
		if !e.Key(step.k) || e.popup.Side() != step.side {
			t.Errorf("%v: open, side %d; want side %d", step.k, e.popup.Side(), step.side)
		}
	}
	if e.Key(term.Key{Code: term.KeyEsc}) {
		t.Error("Esc left the popup open")
	}
}
