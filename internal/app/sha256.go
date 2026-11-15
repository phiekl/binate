// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package app

import "crypto/sha256"

// sha256Action shows the SHA-256 checksum of each side, as sha256sum(1).
var sha256Action = checksum('2', "SHA-256", sha256.New)
