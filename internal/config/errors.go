// SPDX-License-Identifier: Apache-2.0

package config

import "strings"

type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "config: invalid configuration:\n  " + strings.Join(e.Problems, "\n  ")
}
