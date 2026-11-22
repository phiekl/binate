// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

//go:build 386 || arm || mips || mipsle

package files

// binate needs a 64-bit platform: it assumes an int holds the size of any
// file, or part of one, and isn't built or tested on others. The packages
// that don't build on a 32-bit platform all import this one, so this error
// comes first.
var _ = binateNeedsA64BitPlatform
