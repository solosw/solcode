// Copyright 2026 Hashir Muzaffar. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license
// that can be found in the LICENSE file.

package hfbpe

import "unicode"

// byteToRune maps each of the 256 byte values to the printable rune that
// stands for it in a byte-level vocabulary, and runeToByte inverts it.
//
// The mapping keeps bytes that are already printable ASCII or printable
// Latin-1 as themselves, and lifts the remaining 68 byte values into the
// range U+0100 upwards so that every byte has a distinct, printable,
// non-whitespace representative. A space becomes U+0120 ("Ġ"), which is why
// byte-level vocabularies are full of it.
var (
	byteToRune [256]rune
	runeToByte map[rune]byte
)

func init() {
	printable := func(b int) bool {
		return (b >= '!' && b <= '~') || (b >= 0xA1 && b <= 0xAC) || (b >= 0xAE && b <= 0xFF)
	}
	next := rune(256)
	for b := 0; b < 256; b++ {
		if printable(b) {
			byteToRune[b] = rune(b)
		} else {
			byteToRune[b] = next
			next++
		}
	}
	runeToByte = make(map[rune]byte, 256)
	for b := 0; b < 256; b++ {
		runeToByte[byteToRune[b]] = byte(b)
	}
}

// encodeBytes renders s as one rune per input byte using the byte-level
// alphabet.
func encodeBytes(s string) string {
	out := make([]rune, 0, len(s))
	for i := 0; i < len(s); i++ {
		out = append(out, byteToRune[s[i]])
	}
	return string(out)
}

// decodeBytes inverts encodeBytes. Runes outside the byte-level alphabet are
// dropped, matching the reference implementation's lossy decode.
func decodeBytes(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if b, ok := runeToByte[r]; ok {
			out = append(out, b)
		}
	}
	return string(out)
}

// splitGPT2 splits s the way the ByteLevel pre-tokenizer does with
// use_regex set, reproducing
//
//	's|'t|'re|'ve|'m|'ll|'d| ?\p{L}+| ?\p{N}+| ?[^\s\p{L}\p{N}]+|\s+(?!\S)|\s+
//
// The final two alternatives need negative lookahead, which RE2 and so Go's
// regexp package cannot express, so the split is done directly. Doing it by
// hand also avoids a regexp match per piece.
func splitGPT2(s string) []string {
	r := []rune(s)
	var out []string

	isL := func(i int) bool { return i < len(r) && unicode.IsLetter(r[i]) }
	isN := func(i int) bool { return i < len(r) && unicode.IsNumber(r[i]) }
	isS := func(i int) bool { return i < len(r) && unicode.IsSpace(r[i]) }
	// The "other" class: not whitespace, not a letter, not a number.
	isO := func(i int) bool {
		return i < len(r) && !unicode.IsSpace(r[i]) && !unicode.IsLetter(r[i]) && !unicode.IsNumber(r[i])
	}

	for i := 0; i < len(r); {
		// Contractions, matched before anything else and only in lower case.
		if r[i] == '\'' && i+1 < len(r) {
			if n := contraction(r[i+1:]); n > 0 {
				out = append(out, string(r[i:i+1+n]))
				i += 1 + n
				continue
			}
		}

		// " ?\p{L}+", " ?\p{N}+" and " ?[^\s\p{L}\p{N}]+": an optional
		// single leading space followed by a run of one class.
		start := i
		j := i
		if r[j] == ' ' {
			j++
		}
		switch {
		case isL(j):
			for isL(j) {
				j++
			}
		case isN(j):
			for isN(j) {
				j++
			}
		case isO(j):
			for isO(j) {
				j++
			}
		default:
			j = -1
		}
		if j > 0 {
			out = append(out, string(r[start:j]))
			i = j
			continue
		}

		// Whitespace. "\s+(?!\S)" is greedy but must not end immediately
		// before a non-space, so when the run is followed by one it gives
		// up its last character, which the next iteration attaches to the
		// following piece. A run reaching the end of input is taken whole.
		k := i
		for isS(k) {
			k++
		}
		if k == i {
			// Should be unreachable: every rune is a space, a letter, a
			// number or "other". Consume one rune rather than spin.
			out = append(out, string(r[i:i+1]))
			i++
			continue
		}
		end := k
		if k < len(r) && k-i > 1 {
			end = k - 1
		}
		out = append(out, string(r[i:end]))
		i = end
	}
	return out
}

// contraction reports the length in runes of a recognised contraction tail
// following an apostrophe, or 0.
func contraction(r []rune) int {
	two := func(a, b rune) bool { return len(r) >= 2 && r[0] == a && r[1] == b }
	switch {
	case two('r', 'e'), two('v', 'e'), two('l', 'l'):
		return 2
	case len(r) >= 1 && (r[0] == 's' || r[0] == 't' || r[0] == 'm' || r[0] == 'd'):
		return 1
	}
	return 0
}
