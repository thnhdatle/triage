// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

// crockford is the ULID alphabet (Crockford base32).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a ULID: 48 bits of Unix milliseconds and 80 random bits in 26
// Crockford base32 characters. IDs sort by creation time to the millisecond.
func NewID() string { return newIDAt(time.Now()) }

func newIDAt(now time.Time) string {
	var b [16]byte
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(now.UnixMilli())) //nolint:gosec // G115: positive until year 292 million
	copy(b[:6], ts[2:])
	_, _ = rand.Read(b[6:]) // crypto/rand.Read never fails (Go 1.24+)
	return encodeULID(b)
}

func encodeULID(b [16]byte) string {
	var out [26]byte
	// 128 bits are encoded as 26 groups of 5 bits, the first group holding
	// the top 3 bits (zero padded).
	var bits, acc uint32
	pos := 25
	for i := 15; i >= 0; i-- {
		acc |= uint32(b[i]) << bits
		bits += 8
		for bits >= 5 && pos >= 0 {
			out[pos] = crockford[acc&31]
			acc >>= 5
			bits -= 5
			pos--
		}
	}
	if pos >= 0 {
		out[pos] = crockford[acc&31]
	}
	return string(out[:])
}
