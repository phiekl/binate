// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import "crypto/sha512"

// sha384Action shows the SHA-384 checksum of each side, as sha384sum(1).
var sha384Action = checksum('3', "SHA-384", sha512.New384)
