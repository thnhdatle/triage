// SPDX-License-Identifier: Apache-2.0

package config

import "strings"

// ValidationError lists every problem found in a configuration, so an operator
// can fix them all at once. Each problem reads "path: message".
type ValidationError struct {
	Problems []string
}

// Error implements error.
func (e *ValidationError) Error() string {
	return "config: invalid configuration:\n  " + strings.Join(e.Problems, "\n  ")
}
