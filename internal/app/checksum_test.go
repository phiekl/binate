// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSumLine(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"a/b c", "S  a/b c"},
		{`a\b`, `\S  a\\b`},
		{"n\nl", `\S  n\nl`},
		{"c\rr", `\S  c\rr`},
		{"t\tt", "S  t\tt"},
	} {
		if got := sumLine("S", c.name); got != c.want {
			t.Errorf("sumLine(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	// The lines are as sha256sum prints them, where it is installed.
	cmd, err := exec.LookPath("sha256sum")
	if err != nil {
		return
	}
	dir := t.TempDir()
	for _, name := range []string{"plain é", `back\slash`, "new\nline", "car\rriage"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		c := exec.Command(cmd, name)
		c.Dir = dir
		out, err := c.Output()
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(name))
		if got, want := sumLine(hex.EncodeToString(sum[:]), name), strings.TrimSuffix(string(out), "\n"); got != want {
			t.Errorf("sumLine for %q:\n%q, sha256sum printed\n%q", name, got, want)
		}
	}
}

func TestHashFileStops(t *testing.T) {
	dir := writeTestFiles(t, map[string]string{"f": "x"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hashFile(ctx, filepath.Join(dir, "f"), sha256.New(), &sideRun{}); err == nil {
		t.Error("a cancelled hash finished")
	}
	// A symlink is not followed.
	if err := os.Symlink("f", filepath.Join(dir, "l")); err != nil {
		t.Fatal(err)
	}
	if _, err := hashFile(context.Background(), filepath.Join(dir, "l"), sha256.New(), &sideRun{}); err == nil {
		t.Error("hashed through a symlink")
	}
}
