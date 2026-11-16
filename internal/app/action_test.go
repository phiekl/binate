// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"pxy.se/go/binate/internal/colors"
	"pxy.se/go/binate/internal/source"
	"pxy.se/go/binate/internal/term"
)

func TestActions(t *testing.T) {
	left := writeTestFiles(t, map[string]string{"dir/x": "1", "diff": "left", "lonly": "only", "same": "same", "tab\tx": "tab", "bad\xff": "bad"})
	right := writeTestFiles(t, map[string]string{"dir/x": "2", "diff": "right", "same": "same", "tab\tx": "tab", "bad\xff": "bad"})
	a := testApp(100, 40)
	ts := NewTreeScreen(a, TreeConfig{Styles: colors.Default(), Left: source.Disk(left), Right: source.Disk(right)})
	a.Main = ts
	ts.scan.Wait()
	ts.model.ShowSame = true
	scr := term.NewScreen(100, 40)
	ts.Draw(scr)
	selectPath := func(p string) {
		ts.scan.Lock()
		ts.model.Select(ts.scan.Root.Find(p))
		ts.scan.Unlock()
	}
	// run runs the action with key on p, and draws its result once done.
	run := func(p string, key rune) {
		t.Helper()
		selectPath(p)
		ts.Key(term.Rune('a'))
		if ts.actions == nil {
			t.Fatal("a did not open the Actions popup")
		}
		// Outside git mode, without the commit actions.
		if len(ts.actions.Rows) != len(actions) {
			t.Errorf("%d actions, want %d", len(ts.actions.Rows), len(actions))
		}
		ts.Key(term.Rune(key))
		if ts.actions != nil {
			t.Fatalf("%c left the Actions popup open", key)
		}
		for deadline := time.Now().Add(5 * time.Second); ts.actionRunning(); {
			if time.Now().After(deadline) {
				t.Fatal("the action did not finish")
			}
			time.Sleep(time.Millisecond)
		}
		scr.Clear()
		ts.Draw(scr)
	}
	// find returns where text is on the screen.
	find := func(text string) (x, y int, ok bool) {
		for y, line := range strings.Split(scr.String(), "\n") {
			if i := strings.Index(line, text); i >= 0 {
				return len([]rune(line[:i])), y, true
			}
		}
		return 0, 0, false
	}
	sha := func(data string) string {
		sum := sha256.Sum256([]byte(data))
		return hex.EncodeToString(sum[:])
	}

	// The same file: both sums, marked the same, each linked to its
	// sha256sum line, under the action's name.
	run("same", '2')
	sum := sha("same")
	x, y, ok := find(sum[:16])
	if !ok {
		t.Fatalf("no SHA-256 of same:\n%s", scr.String())
	}
	if c := scr.Cell(x, y); c.Style.Link != sum+"  same" {
		t.Errorf("sum linked to %q", c.Style.Link)
	}
	if !strings.Contains(scr.String(), " SHA-256 ") {
		t.Errorf("no title:\n%s", scr.String())
	}
	if _, y2, _ := find(sum[:8]); y2 != y || strings.Count(strings.Split(scr.String(), "\n")[y], sum[:8]) != 2 {
		t.Errorf("sums not side by side:\n%s", scr.String())
	}
	if !strings.Contains(strings.Split(scr.String(), "\n")[y], " = ") {
		t.Errorf("same sums not marked =:\n%s", strings.Split(scr.String(), "\n")[y])
	}
	// The sum replaces the preview, until Esc.
	ts.Key(term.Key{Code: term.KeyEsc})
	scr.Clear()
	ts.Draw(scr)
	if strings.Contains(scr.String(), sum[:8]) || ts.action != nil {
		t.Errorf("Esc left the sum:\n%s", scr.String())
	}

	// SHA-384 has its own sums, linked the same way.
	run("same", '3')
	s384 := sha512.Sum384([]byte("same"))
	sum384 := hex.EncodeToString(s384[:])
	if x, y, ok := find(sum384[:16]); !ok || scr.Cell(x, y).Style.Link != sum384+"  same" || !strings.Contains(scr.String(), " SHA-384 ") {
		t.Errorf("SHA-384 of same:\n%s", scr.String())
	}
	ts.Key(term.Key{Code: term.KeyEsc})

	// Different files are marked |, and SHA-512 has its own sums.
	run("diff", '5')
	l, r := sha512.Sum512([]byte("left")), sha512.Sum512([]byte("right"))
	ls, rs := hex.EncodeToString(l[:]), hex.EncodeToString(r[:])
	_, ly, lok := find(ls[:8])
	_, ry, rok := find(rs[:8])
	if !lok || !rok || ly != ry || !strings.Contains(strings.Split(scr.String(), "\n")[ly], " | ") {
		t.Errorf("SHA-512 of diff:\n%s", scr.String())
	}
	// Moving the selection drops the result.
	ts.Key(term.Rune('k'))
	scr.Clear()
	ts.Draw(scr)
	if strings.Contains(scr.String(), ls[:8]) || ts.action != nil {
		t.Errorf("the result stayed after moving:\n%s", scr.String())
	}

	// A side missing says so; the other is hashed.
	run("lonly", '2')
	if _, _, ok := find(sha("only")[:8]); !ok || !strings.Contains(scr.String(), "missing") {
		t.Errorf("left-only file:\n%s", scr.String())
	}
	ts.Key(term.Key{Code: term.KeyEsc})

	// A name with a control, as a tab, or that is not UTF-8 shows its
	// sums, but can't be put in a link, so they are written without one.
	for _, name := range []string{"tab\tx", "bad\xff"} {
		run(name, '2')
		sum := sha(map[string]string{"tab\tx": "tab", "bad\xff": "bad"}[name])
		if _, _, ok := find(sum[:16]); !ok {
			t.Errorf("no SHA-256 of %q:\n%s", name, scr.String())
		}
		var out bytes.Buffer
		if err := scr.Flush(&out); err != nil {
			t.Fatal(err)
		}
		if strings.Count(out.String(), sum[:16]) != 2 || strings.Contains(out.String(), "\x1b]8;;"+sum) {
			t.Errorf("SHA-256 of %q written with a link: %q", name, out.String())
		}
		ts.Key(term.Key{Code: term.KeyEsc})
	}

	// A directory can't be hashed: its row is dimmed, and choosing it
	// says why.
	selectPath("dir")
	ts.Key(term.Rune('a'))
	if !ts.actions.Lines[2].Dim {
		t.Error("SHA-256 not dimmed for a directory")
	}
	ts.Key(term.Key{Code: term.KeyEnter})
	if ts.action != nil || ts.message != "SHA-256 needs a regular file on a side" {
		t.Errorf("SHA-256 of a directory: message %q", ts.message)
	}
	// Esc closes the popup without running anything.
	ts.Key(term.Rune('a'))
	ts.Key(term.Key{Code: term.KeyEsc})
	if ts.actions != nil || ts.action != nil {
		t.Error("Esc did not close the Actions popup")
	}

	// Without room for the preview, the result takes the panels' place;
	// without that either, the status bar says so.
	scr = term.NewScreen(100, 24)
	run("same", '2')
	if _, _, ok := find(sum[:8]); !ok || strings.Contains(scr.String(), "mode │") {
		t.Errorf("result on a shorter screen:\n%s", scr.String())
	}
	scr = term.NewScreen(100, 8)
	scr.Clear()
	ts.Draw(scr)
	if !strings.Contains(scr.String(), "SHA-256 needs a taller terminal") {
		t.Errorf("result on a short screen:\n%s", scr.String())
	}
	// An edit, or a rescan, drops it.
	ts.statGen++
	scr.Clear()
	ts.Draw(scr)
	if ts.action != nil {
		t.Error("the result stayed after the entry changed")
	}
}
