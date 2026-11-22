// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package seccomp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// childEnv runs the test binary as a child that installs the filter, as
// it can't be removed once installed.
const childEnv = "BINATE_SECCOMP_TEST_CHILD"

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
		os.Exit(child())
	}
	os.Exit(m.Run())
}

// ioctl makes the syscall with the request as given, all 64 bits of it.
func ioctl(fd int, req uint64) error {
	var b [8]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(unsafe.Pointer(&b)))
	if errno != 0 {
		return errno
	}
	return nil
}

// child checks ioctl's errors on /dev/null, which is no terminal, before
// and after the filter: a request the filter lets through fails as one on
// no terminal does, with ENOTTY, and one it refuses with EPERM, also on a
// thread started before it, and with bits above the request's 32.
func child() int {
	f, err := os.Open(os.DevNull)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	fd := int(f.Fd())
	check := func(what string, err, want error) bool {
		if !errors.Is(err, want) {
			fmt.Printf("%s: %v, want %v\n", what, err, want)
			return false
		}
		return true
	}
	ok := check("TIOCSTI before", ioctl(fd, unix.TIOCSTI), unix.ENOTTY)

	// A thread that is there before the filter.
	asked, answered := make(chan uint64), make(chan error)
	go func() {
		runtime.LockOSThread()
		for req := range asked {
			answered <- ioctl(fd, req)
		}
	}()
	asked <- unix.TIOCGWINSZ
	<-answered

	if err := install(Refused); err != nil {
		fmt.Println("installing:", err)
		return 1
	}
	for _, c := range []struct {
		what string
		req  uint64
		want error
	}{
		{"TIOCSTI", unix.TIOCSTI, unix.EPERM},
		{"TIOCLINUX", unix.TIOCLINUX, unix.EPERM},
		{"TIOCSTI with high bits", 1<<32 | unix.TIOCSTI, unix.EPERM},
		{"TIOCGWINSZ", unix.TIOCGWINSZ, unix.ENOTTY},
		{"TCGETS", unix.TCGETS, unix.ENOTTY},
	} {
		ok = check(c.what, ioctl(fd, c.req), c.want) && ok
		asked <- c.req
		ok = check(c.what+" on an older thread", <-answered, c.want) && ok
	}
	if v, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0); err != nil || v != 1 {
		fmt.Printf("no_new_privs: %d, %v\n", v, err)
		ok = false
	}
	if !ok {
		return 1
	}
	fmt.Println("ok")
	return 0
}

// TestInstall checks the filter on the running kernel, in a child.
func TestInstall(t *testing.T) {
	if _, ok := arches[runtime.GOARCH]; !ok {
		t.Skipf("no filter for %s", runtime.GOARCH)
	}
	c := exec.Command(os.Args[0], "-test.run=^$")
	c.Env = append(os.Environ(), childEnv+"=1")
	out, err := c.CombinedOutput()
	if strings.Contains(string(out), "unsupported") {
		t.Skipf("seccomp filters unavailable: %s", out)
	}
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("child: %v\n%s", err, out)
	}
}

// run interprets prog, as the kernel would, on a syscall of nr made as
// arch with request as ioctl's second argument, on a machine of the given
// byte order, and returns its result. It knows only the instructions the
// filter has.
func run(t *testing.T, prog []unix.SockFilter, arch, nr uint32, request uint64, order binary.ByteOrder) uint32 {
	t.Helper()
	var data [64]byte
	order.PutUint32(data[nrOffset:], nr)
	order.PutUint32(data[archOffset:], arch)
	order.PutUint64(data[argsOffset+8:], request)
	var a uint32
	for pc := 0; pc < len(prog); pc++ {
		in := prog[pc]
		switch in.Code {
		case unix.BPF_LD | unix.BPF_W | unix.BPF_ABS:
			a = order.Uint32(data[in.K:])
		case unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, unix.BPF_JMP | unix.BPF_JGT | unix.BPF_K:
			taken := a == in.K
			switch in.Code & 0xf0 {
			case unix.BPF_JGE:
				taken = a >= in.K
			case unix.BPF_JGT:
				taken = a > in.K
			}
			if taken {
				pc += int(in.Jt)
			} else {
				pc += int(in.Jf)
			}
		case unix.BPF_RET | unix.BPF_K:
			return in.K
		default:
			t.Fatalf("instruction %d: unknown code %#x", pc, in.Code)
		}
	}
	t.Fatal("ran off the end")
	return 0
}

// TestFilter checks the program's decisions where a live process can't,
// as for syscalls of other architectures, x32's and on big-endian
// machines.
func TestFilter(t *testing.T) {
	const (
		allow = unix.SECCOMP_RET_ALLOW
		deny  = unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)
	)
	for _, tc := range []struct {
		name    string
		arch    uint32
		madeAs  uint32
		nr      uint32
		request uint64
		want    uint32
	}{
		{"TIOCSTI", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, unix.SYS_IOCTL, unix.TIOCSTI, deny},
		{"TIOCLINUX", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, unix.SYS_IOCTL, unix.TIOCLINUX, deny},
		{"high bits", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, unix.SYS_IOCTL, 0xffffffff<<32 | unix.TIOCSTI, deny},
		{"other ioctl", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, unix.SYS_IOCTL, unix.TIOCGWINSZ, allow},
		{"other syscall", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, unix.SYS_READ, unix.TIOCSTI, allow},
		{"i386", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_I386, 54, unix.TIOCSTI, deny},
		{"i386 read", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_I386, 3, 0, deny},
		{"x32 ioctl", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, x32 | 514, unix.TIOCSTI, deny},
		{"x32 read", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, x32, 0, deny},
		// x32's own numbers without its bit, which kernels before 5.4 ran.
		{"x32 ioctl without the bit", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, 514, unix.TIOCSTI, deny},
		{"x32's first number", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, x32First, 0, deny},
		{"x32's last number", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, x32Last, 0, deny},
		{"below x32's numbers", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, x32First - 1, 0, allow},
		{"above x32's numbers", unix.AUDIT_ARCH_X86_64, unix.AUDIT_ARCH_X86_64, x32Last + 1, 0, allow},
		{"arm64, x32's ioctl number", unix.AUDIT_ARCH_AARCH64, unix.AUDIT_ARCH_AARCH64, 514, 0, allow},
		{"arm64 TIOCSTI", unix.AUDIT_ARCH_AARCH64, unix.AUDIT_ARCH_AARCH64, unix.SYS_IOCTL, unix.TIOCSTI, deny},
		{"arm64, x32's bit", unix.AUDIT_ARCH_AARCH64, unix.AUDIT_ARCH_AARCH64, x32, 0, allow},
		{"arm", unix.AUDIT_ARCH_AARCH64, unix.AUDIT_ARCH_ARM, unix.SYS_IOCTL, unix.TIOCSTI, deny},
	} {
		for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
			// The program reads the low half where the machine has it.
			prog := filter(tc.arch, order == binary.BigEndian, Refused)
			if got := run(t, prog, tc.madeAs, tc.nr, tc.request, order); got != tc.want {
				t.Errorf("%s, %v: %#x, want %#x", tc.name, order, got, tc.want)
			}
		}
	}
}
