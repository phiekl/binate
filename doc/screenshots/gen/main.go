// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Command gen makes the screenshots in doc/screenshots. It runs binate in
// a pseudo-terminal, types keys into it, and draws the screen as a PNG.
//
//	go run . -binate ../../../build/binate -linux ~/src/linux -out ..
//
// The trees compared are made in a temporary directory, from parts of
// /usr/share and made-up files, and binate runs as root of a user
// namespace so that they show as owned by root. The git screenshot
// compares v7.1 and v7.2 of a clone of Linux.
package main

import (
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const cols, rows = 114, 35

// scene is one screenshot: binate run with args in dir, the keys typed,
// and the texts the screen must show before it is taken.
type scene struct {
	name   string
	dir    string
	home   string
	root   bool
	args   []string
	keys   []string
	expect []string
}

func main() {
	binate := flag.String("binate", "", "the binate binary to run")
	linux := flag.String("linux", "", "a clone of Linux with tags v7.1 and v7.2")
	out := flag.String("out", ".", "the directory to write the screenshots to")
	flag.Parse()
	if err := generate(*binate, *linux, *out); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func generate(binate, linux, out string) error {
	if binate == "" || linux == "" {
		return errors.New("-binate and -linux are required")
	}
	binate, err := filepath.Abs(binate)
	if err != nil {
		return err
	}
	linux, err = filepath.Abs(linux)
	if err != nil {
		return err
	}
	log := newLogger(os.Stderr)
	rd, err := newRenderer()
	if err != nil {
		return err
	}

	fx, err := os.MkdirTemp("", "binate-screenshots-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(fx)
	log.printf("making the trees in %s", fx)
	if err := makeRootfs(fx, log.with("trees: ")); err != nil {
		return err
	}
	n, err := countEntries(filepath.Join(fx, "rootfs.1"))
	if err != nil {
		return err
	}
	log.printf("made the trees, of %d entries each", n)

	rootfs := func(name string, keys []string, expect ...string) scene {
		return scene{name: name, dir: fx, home: fx, root: true,
			args: []string{"rootfs.1", "rootfs.2"}, keys: keys, expect: expect}
	}
	// sshd_config is the 9th entry of the tree, which shows only the
	// differences.
	toSshdConfig := repeat("j", 8)
	scenes := []scene{
		rootfs("tree", toSshdConfig, "~/rootfs.1/etc/ssh/sshd_config", "0:0"),
		rootfs("diff", concat(toSshdConfig, []string{"\r"}), "(viewing) rootfs.1/etc/ssh/sshd_config"),
		rootfs("filter", concat([]string{"F"}, chars("^Stockho"), []string{"\r"}), "filter (all): ^Stockho"),
		{
			name: "git", dir: linux, home: filepath.Dir(linux),
			args: []string{"-g", "v7.1", "v7.2"},
			// Search for the file, then scroll it to near the top.
			keys: concat([]string{"/"}, chars("^aes-spe-glue"), []string{"\r"},
				repeat("j", 10), repeat("k", 11), []string{"j"}),
			expect: []string{"v7.1:arch/powerpc/crypto/aes-spe-glue.c", "committed"},
		},
	}
	for i, sc := range scenes {
		slog := log.with(sc.name + ": ")
		slog.printf("taking screenshot %d of %d", i+1, len(scenes))
		if err := shoot(rd, binate, sc, filepath.Join(out, sc.name+".png"), slog); err != nil {
			return fmt.Errorf("%s: %w", sc.name, err)
		}
	}
	log.printf("done")
	return nil
}

func shoot(rd *renderer, binate string, sc scene, path string, log *logger) error {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + sc.home, "TERM=xterm-256color", "COLORTERM=truecolor"}
	if tz, ok := os.LookupEnv("TZ"); ok {
		env = append(env, "TZ="+tz)
	}
	how := ""
	if sc.root {
		how = " as root of a user namespace"
	}
	log.printf("running binate %s in %s%s", strings.Join(sc.args, " "), sc.dir, how)
	s, err := start(cols, rows, options{dir: sc.dir, env: env, root: sc.root, log: log}, append([]string{binate}, sc.args...)...)
	if err != nil {
		return err
	}
	defer s.close()
	t := time.Now()
	if err := s.waitFor(time.Minute, "items total"); err != nil {
		return err
	}
	log.printf("scanned in %.1fs", time.Since(t).Seconds())
	s.settle(time.Second)
	t = time.Now()
	if err := s.typeKeys(sc.keys); err != nil {
		return err
	}
	log.printf("typed %d keys in %.1fs", len(sc.keys), time.Since(t).Seconds())
	if err := s.waitFor(30*time.Second, sc.expect...); err != nil {
		return err
	}
	s.settle(time.Second)

	s.mu.Lock()
	img := rd.render(s.vt)
	s.mu.Unlock()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil {
		log.printf("wrote %s (%d KiB)", path, fi.Size()/1024)
	}
	return nil
}

// countEntries returns the number of entries below dir.
func countEntries(dir string) (int, error) {
	n := 0
	err := filepath.WalkDir(dir, func(_ string, _ fs.DirEntry, err error) error {
		n++
		return err
	})
	return n - 1, err
}

func repeat(key string, n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = key
	}
	return keys
}

func chars(s string) []string {
	return strings.Split(s, "")
}

func concat(lists ...[]string) []string {
	var all []string
	for _, l := range lists {
		all = append(all, l...)
	}
	return all
}
