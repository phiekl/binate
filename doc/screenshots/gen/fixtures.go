// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The sshd_config of each side, with the differences the screenshots show.
var (
	//go:embed sshd_config.1
	sshdConfig1 string
	//go:embed sshd_config.2
	sshdConfig2 string
)

// sshdJunk ends both sshd_config files, the same on each side, giving them
// a size like a real one's of 4315 and 4317 bytes.
var sshdJunk = junkLines(4315 - len(sshdConfig1))

// shareDirs are the directories of /usr/share copied into both trees, to
// give them the size of a real root file system. Those missing are left
// out.
var shareDirs = []string{
	"X11", "bash-completion", "ca-certificates", "common-licenses",
	"consolefonts", "doc", "groff", "i18n", "man", "mime", "misc", "perl",
	"terminfo", "vim", "zoneinfo",
}

// makeRootfs makes rootfs.1 and rootfs.2 in dir: copies of parts of
// /usr/share, and of a made-up /etc/ssh and /var/log, with differences of
// every kind between them.
func makeRootfs(dir string, log *logger) error {
	a, b := filepath.Join(dir, "rootfs.1"), filepath.Join(dir, "rootfs.2")
	share := filepath.Join(a, "usr/share")
	for _, d := range []string{share, a + "/etc/ssh/ssh_config.d", a + "/etc/ssh/sshd_config.d", a + "/var/log"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	var copied, missing []string
	for _, d := range shareDirs {
		src := filepath.Join("/usr/share", d)
		if _, err := os.Stat(src); err != nil {
			missing = append(missing, d)
			continue
		}
		log.printf("copying %s", src)
		if err := run("cp", "-a", src, share); err != nil {
			return err
		}
		copied = append(copied, d)
	}
	if len(missing) > 0 {
		log.printf("left out what /usr/share lacks (%s)", strings.Join(missing, ", "))
	}
	log.printf("copied %d directories of /usr/share", len(copied))
	// The file the filter screenshot shows, the same on every system,
	// whatever was copied there, which may be a link.
	europe := filepath.Join(share, "zoneinfo/Europe")
	if err := os.MkdirAll(europe, 0o755); err != nil {
		return err
	}
	stockholm := filepath.Join(europe, "Stockholm")
	if err := os.RemoveAll(stockholm); err != nil {
		return err
	}
	tzif := append([]byte("TZif2"), randomBytes("Stockholm", 1909-5)...)
	if err := os.WriteFile(stockholm, tzif, 0o644); err != nil {
		return err
	}
	if err := os.Chmod(stockholm, 0o644); err != nil {
		return err
	}
	if err := setTime(stockholm, "2026-10-05 11:17:50"); err != nil {
		return err
	}
	if fi, err := os.Lstat(stockholm); err != nil {
		return err
	} else if !fi.Mode().IsRegular() || fi.Size() != 1909 {
		return fmt.Errorf("%s is %v of %d bytes, not a file of 1909", stockholm, fi.Mode().Type(), fi.Size())
	}

	key := base64.StdEncoding.EncodeToString(randomBytes("key", 300))
	for _, f := range []struct {
		path string
		data string
		mode os.FileMode
	}{
		{"etc/ssh/ssh_config", "Host *\n    SendEnv LANG LC_*\n    HashKnownHosts yes\n", 0o644},
		{"etc/ssh/sshd_config", sshdConfig1 + sshdJunk, 0o644},
		{"etc/ssh/ssh_host_ed25519.key", key + "\n", 0o644},
		{"var/log/lastlog", string(make([]byte, 292292)), 0o644},
		{"var/log/wtmp", string(randomBytes("wtmp", 38400)), 0o664},
	} {
		if err := os.WriteFile(filepath.Join(a, f.path), []byte(f.data), f.mode); err != nil {
			return err
		}
	}
	if err := os.Symlink("/usr/lib/systemd/ssh_config.d/20-systemd-ssh-proxy.conf",
		filepath.Join(a, "etc/ssh/ssh_config.d/20-systemd-ssh-proxy.conf")); err != nil {
		return err
	}
	if err := setTime(filepath.Join(a, "etc/ssh/sshd_config"), "2026-10-08 18:36:04"); err != nil {
		return err
	}

	// The right side: a copy, with an entry of each kind of difference.
	log.printf("copying rootfs.1 to rootfs.2")
	if err := run("cp", "-a", a, b); err != nil {
		return err
	}
	for _, p := range []string{"etc/ssh/ssh_config.d/20-systemd-ssh-proxy.conf", "var/log/lastlog", "var/log/wtmp"} {
		if err := os.Remove(filepath.Join(b, p)); err != nil {
			return err
		}
	}
	for _, f := range []struct{ path, data string }{
		{"etc/ssh/sshd_config", sshdConfig2 + sshdJunk},
		{"etc/ssh/sshd_config.d/override.conf", "PermitRootLogin no\n"},
		{"etc/ssh/moduli", moduli()},
	} {
		if err := os.WriteFile(filepath.Join(b, f.path), []byte(f.data), 0o644); err != nil {
			return err
		}
	}
	if err := os.Chmod(filepath.Join(b, "etc/ssh/ssh_host_ed25519.key"), 0o444); err != nil {
		return err
	}
	stockholm = filepath.Join(b, "usr/share/zoneinfo/Europe/Stockholm")
	if err := os.Remove(stockholm); err != nil {
		return err
	}
	if err := os.Symlink("Gothenburg", stockholm); err != nil {
		return err
	}
	for _, p := range []string{"etc/ssh/sshd_config", "usr/share/zoneinfo/Europe/Stockholm"} {
		if err := setTime(filepath.Join(b, p), "2026-11-16 23:37:03"); err != nil {
			return err
		}
	}
	return nil
}

// moduli returns lines in the form of /etc/ssh/moduli.
func moduli() string {
	var sb strings.Builder
	sb.WriteString("# Time Type Tests Tries Size Generator Modulus\n")
	r := rand.New(rand.NewChaCha8(seed("moduli")))
	for i := range 40 {
		fmt.Fprintf(&sb, "20260301%06d 2 6 100 3071 %d ", i*137, 2+3*(i%2))
		for range 192 {
			fmt.Fprintf(&sb, "%X", r.IntN(16))
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// junkLines returns n bytes of text, in lines of random hex digits after
// a #, the same each time.
func junkLines(n int) string {
	var sb strings.Builder
	r := rand.New(rand.NewChaCha8(seed("junk")))
	for sb.Len() < n {
		line := []byte("# ")
		for range 70 {
			line = append(line, "0123456789abcdef"[r.IntN(16)])
		}
		line = line[:min(len(line), n-sb.Len()-1)]
		sb.Write(line)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// randomBytes returns n bytes, the same each time for a name.
func randomBytes(name string, n int) []byte {
	b := make([]byte, n)
	rand.NewChaCha8(seed(name)).Read(b)
	return b
}

func seed(name string) [32]byte {
	var s [32]byte
	copy(s[:], name)
	return s
}

// setTime sets the access and modification times of path, not following
// a symlink, to a local time.
func setTime(path, when string) error {
	t, err := time.ParseInLocation(time.DateTime, when, time.Local)
	if err != nil {
		return err
	}
	ts := unix.NsecToTimespec(t.UnixNano())
	return unix.UtimesNanoAt(unix.AT_FDCWD, path, []unix.Timespec{ts, ts}, unix.AT_SYMLINK_NOFOLLOW)
}

func run(argv ...string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return nil
}
