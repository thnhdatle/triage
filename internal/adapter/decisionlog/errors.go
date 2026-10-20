// SPDX-License-Identifier: Apache-2.0

package decisionlog

import "errors"

// Errors returned by the package.
var (
	// ErrInvalidSize is returned by New for a non-positive buffer size.
	ErrInvalidSize = errors.New("decisionlog: buffer size must be positive")
	// ErrInvalidCursor is returned by List for a malformed cursor.
	ErrInvalidCursor = errors.New("decisionlog: invalid cursor")
)
