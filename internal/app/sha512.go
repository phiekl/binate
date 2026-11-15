// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import "crypto/sha512"

// sha512Action shows the SHA-512 checksum of each side, as sha512sum(1).
var sha512Action = checksum('5', "SHA-512", sha512.New)
