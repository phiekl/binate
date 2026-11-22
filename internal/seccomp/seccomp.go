// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

// Package seccomp refuses the ioctl requests that inject input into a
// terminal, which Landlock doesn't restrict on a terminal opened before
// it, with a seccomp filter on every thread of the process.
package seccomp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"

	"pxy.se/go/binate/internal/cli"
)

// arches are the audit architectures of the platforms binate builds on,
// all 64-bit, which a syscall must be made as to be let through.
var arches = map[string]uint32{
	"amd64":    unix.AUDIT_ARCH_X86_64,
	"arm64":    unix.AUDIT_ARCH_AARCH64,
	"loong64":  unix.AUDIT_ARCH_LOONGARCH64,
	"mips64":   unix.AUDIT_ARCH_MIPS64,
	"mips64le": unix.AUDIT_ARCH_MIPSEL64,
	"ppc64":    unix.AUDIT_ARCH_PPC64,
	"ppc64le":  unix.AUDIT_ARCH_PPC64LE,
	"riscv64":  unix.AUDIT_ARCH_RISCV64,
	"s390x":    unix.AUDIT_ARCH_S390X,
}

// x32 is the bit of x32's syscall numbers, which are made as x86-64's.
const x32 = 0x40000000

// x32 also has numbers of its own, x32First to x32Last, which x86-64
// keeps free. Before Linux 5.4 they ran as x32's syscalls without the
// bit too, as x32's ioctl, 514, does.
const (
	x32First = 512
	x32Last  = 547
)

// Refused are the ioctl requests refused: TIOCSTI, which types a byte
// into a terminal as if from its keyboard, and TIOCLINUX, which can paste
// the selection of a virtual console into it.
var Refused = []uint32{unix.TIOCSTI, unix.TIOCLINUX}

// seccomp_data's offsets: the syscall's number, its architecture, and its
// arguments, of 8 bytes each.
const (
	nrOffset   = 0
	archOffset = 4
	argsOffset = 16
)

// bigEndian is whether this machine is, which puts the low half of an
// argument after its high half.
var bigEndian = binary.NativeEndian.Uint16([]byte{0, 1}) == 1

// filter returns the program that lets every syscall through but ioctl
// with a request of refused, on arch, a machine big-endian or not, and any
// syscall made as another architecture, which has its own numbers, or on
// x86-64 as x32, by its bit or its own numbers. Only the request's low 32
// bits are compared, as the kernel ignores the rest.
func filter(arch uint32, bigEndian bool, refused []uint32) []unix.SockFilter {
	stmt := func(code uint16, k uint32) unix.SockFilter {
		return unix.SockFilter{Code: code, K: k}
	}
	// The low half of the request, the second argument.
	request := uint32(argsOffset + 8)
	if bigEndian {
		request += 4
	}
	const (
		load = unix.BPF_LD | unix.BPF_W | unix.BPF_ABS
		ret  = unix.BPF_RET | unix.BPF_K
	)
	allow := stmt(ret, unix.SECCOMP_RET_ALLOW)
	deny := stmt(ret, unix.SECCOMP_RET_ERRNO|uint32(unix.EPERM))

	// Jumps are relative to the next instruction, to the end, which
	// allows, or past it, which denies: each is set once the program is
	// built, from where they are and where they lead.
	var prog []unix.SockFilter
	var toAllow, toDeny []int
	jump := func(op uint16, k uint32, ifTrue, ifFalse *[]int) {
		if ifTrue != nil {
			*ifTrue = append(*ifTrue, len(prog)<<1)
		}
		if ifFalse != nil {
			*ifFalse = append(*ifFalse, len(prog)<<1|1)
		}
		prog = append(prog, unix.SockFilter{Code: unix.BPF_JMP | op | unix.BPF_K, K: k})
	}
	prog = append(prog, stmt(load, archOffset))
	jump(unix.BPF_JEQ, arch, nil, &toDeny)
	prog = append(prog, stmt(load, nrOffset))
	if arch == unix.AUDIT_ARCH_X86_64 {
		jump(unix.BPF_JGE, x32, &toDeny, nil)
		// Past x32's own numbers, skip the next check.
		prog = append(prog, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JGT | unix.BPF_K, K: x32Last, Jt: 1})
		jump(unix.BPF_JGE, x32First, &toDeny, nil)
	}
	jump(unix.BPF_JEQ, unix.SYS_IOCTL, nil, &toAllow)
	prog = append(prog, stmt(load, request))
	for _, r := range refused {
		jump(unix.BPF_JEQ, r, &toDeny, nil)
	}
	end := len(prog)
	prog = append(prog, allow, deny)
	set := func(jumps []int, to int) {
		for _, j := range jumps {
			i := j >> 1
			off := uint8(to - i - 1)
			if j&1 == 0 {
				prog[i].Jt = off
			} else {
				prog[i].Jf = off
			}
		}
	}
	set(toAllow, end)
	set(toDeny, end+1)
	return prog
}

// errUnsupported is returned where the kernel or the platform has no
// seccomp filters.
var errUnsupported = errors.New("unsupported")

// Restrict refuses ioctl's Refused requests on every thread of the
// process, now and to come, with EPERM, and sets no_new_privs, which the
// filter needs. A kernel without seccomp filters, or a platform the
// filter doesn't know, is not an error: the process is left as it is.
func Restrict(log *slog.Logger) error {
	err := install(Refused)
	switch {
	case errors.Is(err, errUnsupported):
		cli.Debug(log, cli.LevelDebug1, "seccomp: unavailable", "reason", err, "best_effort", true)
		return nil
	case err != nil:
		return err
	}
	cli.Debug(log, cli.LevelDebug1, "seccomp: enabled", "refused", "ioctl TIOCSTI, TIOCLINUX", "threads", "all")
	return nil
}

// install applies the filter refusing refused.
func install(refused []uint32) error {
	arch, ok := arches[runtime.GOARCH]
	if !ok {
		return fmt.Errorf("%w on %s", errUnsupported, runtime.GOARCH)
	}
	prog := filter(arch, bigEndian, refused)
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]}

	// no_new_privs is set on this thread, and the filter, with TSYNC,
	// sets it on the others too.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("setting no_new_privs: %w", err)
	}
	r, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER,
		unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&fprog)))
	runtime.KeepAlive(prog)
	switch {
	case errno == unix.ENOSYS || errno == unix.EINVAL:
		// Without CONFIG_SECCOMP_FILTER, or too old for TSYNC.
		return fmt.Errorf("%w by the kernel: %v", errUnsupported, errno)
	case errno != 0:
		return fmt.Errorf("installing the seccomp filter: %w", errno)
	case r != 0:
		// The thread that couldn't take the filter.
		return fmt.Errorf("installing the seccomp filter: thread %d refused it", r)
	}
	return nil
}
