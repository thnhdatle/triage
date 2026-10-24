// SPDX-License-Identifier: Apache-2.0

package decisionlog

import "errors"

var (
	ErrInvalidSize   = errors.New("decisionlog: buffer size must be positive")
	ErrInvalidCursor = errors.New("decisionlog: invalid cursor")
)
