// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func NewID() string { return newIDAt(time.Now()) }

func newIDAt(now time.Time) string {
	var b [16]byte
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(now.UnixMilli())) //nolint:gosec // G115: positive until year 292 million
	copy(b[:6], ts[2:])
	_, _ = rand.Read(b[6:])
	return encodeULID(b)
}

func encodeULID(b [16]byte) string {
	var out [26]byte
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
