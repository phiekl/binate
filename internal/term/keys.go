// SPDX-FileCopyrightText: 2026 Philip Eklöf
//
// SPDX-License-Identifier: MIT

package term

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Code is the kind of a key.
type Code int

const (
	KeyNone      Code = iota
	KeyRune           // a character, in Key.Rune
	KeyCtrl           // Ctrl with a letter, in Key.Rune ('a' to 'z')
	KeyEnter          // Enter (CR or LF)
	KeyBackspace      // Backspace (DEL or BS)
	KeyDelete         // Delete
	KeyTab            // Tab
	KeyEsc            // Esc on its own
	KeyUp             // arrow keys
	KeyDown           //
	KeyLeft           //
	KeyRight          //
	KeyHome           //
	KeyEnd            //
	KeyPgUp           //
	KeyPgDn           //
	KeyUnknown        // a sequence or byte that means nothing here
)

// Key is one key press.
type Key struct {
	Code Code
	Rune rune
	// Alt is set for a key pressed with Alt (or Meta): as an ESC prefix,
	// or as the modifier parameter of a CSI sequence.
	Alt bool
	// Shift is set for modified special keys. Shifted letters are sent as
	// uppercase runes instead.
	Shift bool
}

// Rune returns the key for a character.
func Rune(r rune) Key { return Key{Code: KeyRune, Rune: r} }

// Ctrl returns the key for Ctrl and a letter.
func Ctrl(letter rune) Key { return Key{Code: KeyCtrl, Rune: letter} }

func (k Key) String() string {
	var s string
	switch k.Code {
	case KeyRune:
		s = strconv.QuoteRune(k.Rune)
	case KeyCtrl:
		s = "Ctrl+" + string(k.Rune)
	default:
		s = [...]string{"None", "Rune", "Ctrl", "Enter", "Backspace", "Delete", "Tab", "Esc",
			"Up", "Down", "Left", "Right", "Home", "End", "PgUp", "PgDn", "Unknown"}[k.Code]
	}
	if k.Shift {
		s = "Shift+" + s
	}
	if k.Alt {
		s = "Alt+" + s
	}
	return s
}

// maxSequence bounds an escape sequence: a longer one without its final
// byte is taken as unknown, so garbage can't make the parser wait forever.
const maxSequence = 32

// ParseKey parses the first key in buf and returns it with the number of
// bytes it used. With final false, an incomplete sequence at the end of buf
// returns 0 bytes, to wait for more; with final true, as once the Esc
// timeout has passed, every non-empty buf gives a key: a lone ESC is Esc,
// and an incomplete sequence is unknown.
func ParseKey(buf []byte, final bool) (Key, int) {
	if len(buf) == 0 {
		return Key{}, 0
	}
	if buf[0] != 0x1b {
		return parsePlain(buf, final)
	}
	if len(buf) == 1 {
		if final {
			return Key{Code: KeyEsc}, 1
		}
		return Key{}, 0
	}
	switch buf[1] {
	case '[':
		return parseCSI(buf, final)
	case 'O':
		if len(buf) == 2 {
			if final {
				return Key{Code: KeyRune, Rune: 'O', Alt: true}, 2
			}
			return Key{}, 0
		}
		return Key{Code: ss3Key(buf[2])}, 3
	case 0x1b:
		// ESC ESC [A: Alt with an arrow in some terminals.
		k, n := ParseKey(buf[1:], final)
		if n == 0 {
			return Key{}, 0
		}
		if n > 1 && k.Code != KeyUnknown {
			k.Alt = true
			return k, n + 1
		}
		return Key{Code: KeyEsc}, 1
	}
	k, n := parsePlain(buf[1:], final)
	if n == 0 {
		return Key{}, 0
	}
	if k.Code == KeyUnknown {
		return Key{Code: KeyEsc}, 1
	}
	k.Alt = true
	return k, n + 1
}

func parsePlain(buf []byte, final bool) (Key, int) {
	b := buf[0]
	switch {
	case b == '\r' || b == '\n':
		return Key{Code: KeyEnter}, 1
	case b == 0x7f || b == 0x08:
		return Key{Code: KeyBackspace}, 1
	case b == '\t':
		return Key{Code: KeyTab}, 1
	case b >= 1 && b <= 26:
		return Ctrl(rune('a' + b - 1)), 1
	case b < 0x20:
		return Key{Code: KeyUnknown}, 1
	case b < 0x80:
		return Rune(rune(b)), 1
	}
	if !utf8.FullRune(buf) && !final {
		return Key{}, 0
	}
	r, size := utf8.DecodeRune(buf)
	if r == utf8.RuneError && size <= 1 {
		return Key{Code: KeyUnknown}, 1
	}
	return Rune(r), size
}

func ss3Key(b byte) Code {
	switch b {
	case 'A':
		return KeyUp
	case 'B':
		return KeyDown
	case 'C':
		return KeyRight
	case 'D':
		return KeyLeft
	case 'H':
		return KeyHome
	case 'F':
		return KeyEnd
	}
	return KeyUnknown
}

// parseCSI parses ESC [ parameters intermediates final.
func parseCSI(buf []byte, final bool) (Key, int) {
	i := 2
	for i < len(buf) && i < maxSequence {
		c := buf[i]
		switch {
		case c >= 0x20 && c <= 0x3f:
			i++
			continue
		case c >= 0x40 && c <= 0x7e:
			return csiKey(string(buf[2:i]), c), i + 1
		}
		// Not part of a sequence: what came so far is unknown.
		return Key{Code: KeyUnknown}, i
	}
	if i >= maxSequence || final {
		return Key{Code: KeyUnknown}, i
	}
	return Key{}, 0
}

func csiKey(params string, final byte) Key {
	fields := strings.Split(params, ";")
	num := func(i int) int {
		if i >= len(fields) {
			return 0
		}
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			return -1
		}
		return n
	}
	// The modifier parameter is 1 plus a bit mask: 1 Shift, 2 Alt, 4 Ctrl.
	alt := len(fields) > 1 && num(1) > 1 && (num(1)-1)&2 != 0
	shift := len(fields) > 1 && num(1) > 1 && (num(1)-1)&1 != 0
	var code Code
	switch final {
	case 'A':
		code = KeyUp
	case 'B':
		code = KeyDown
	case 'C':
		code = KeyRight
	case 'D':
		code = KeyLeft
	case 'H':
		code = KeyHome
	case 'F':
		code = KeyEnd
	case '~':
		switch num(0) {
		case 1, 7:
			code = KeyHome
		case 4, 8:
			code = KeyEnd
		case 3:
			code = KeyDelete
		case 5:
			code = KeyPgUp
		case 6:
			code = KeyPgDn
		default:
			return Key{Code: KeyUnknown}
		}
	default:
		return Key{Code: KeyUnknown}
	}
	return Key{Code: code, Alt: alt, Shift: shift}
}

// ParseKeys parses all complete keys in buf and returns them with the
// bytes left over.
func ParseKeys(buf []byte, final bool) ([]Key, []byte) {
	var keys []Key
	for len(buf) > 0 {
		k, n := ParseKey(buf, final)
		if n == 0 {
			break
		}
		if n > len(buf) {
			panic(fmt.Sprintf("term: key parser used %d of %d bytes", n, len(buf)))
		}
		keys = append(keys, k)
		buf = buf[n:]
	}
	return keys, buf
}
