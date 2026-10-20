// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that also accepts a "d" (24h) suffix.
type Duration time.Duration

var dayPrefix = regexp.MustCompile(`^(\d+)d(.*)$`)

// ParseDuration parses a Go duration string with an optional leading day
// count: "30s", "14d", "1d12h".
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	var days time.Duration
	if m := dayPrefix.FindStringSubmatch(s); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 32)
		if err != nil {
			return 0, fmt.Errorf("config: invalid duration %q: %w", s, err)
		}
		days = time.Duration(n) * 24 * time.Hour
		s = m[2]
		if s == "" {
			return days, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("config: invalid duration %q: %w", s, err)
	}
	return days + d, nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("config: line %d: duration must be a string like \"30s\": %w", n.Line, err)
	}
	v, err := ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Size is a byte count that accepts suffixes: B, KB, MB, GB (powers of 1000)
// and KiB, MiB, GiB (powers of 1024), case-insensitive; a bare number is bytes.
type Size int64

var sizeRe = regexp.MustCompile(`^(\d+)\s*([A-Za-z]*)$`)

var sizeUnits = map[string]int64{
	"": 1, "b": 1,
	"kb": 1000, "mb": 1000 * 1000, "gb": 1000 * 1000 * 1000,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30,
}

// ParseSize parses a size such as "8MiB" or "1048576".
func ParseSize(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("config: invalid size %q", s)
	}
	unit, ok := sizeUnits[strings.ToLower(m[2])]
	if !ok {
		return 0, fmt.Errorf("config: invalid size %q: unknown unit %q", s, m[2])
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > math.MaxInt64/unit {
		return 0, fmt.Errorf("config: invalid size %q: out of range", s)
	}
	return n * unit, nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (z *Size) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("config: line %d: size must be a number or string like \"8MiB\": %w", n.Line, err)
	}
	v, err := ParseSize(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*z = Size(v)
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (z Size) MarshalYAML() (any, error) { return int64(z), nil }

// Bytes returns the size in bytes.
func (z Size) Bytes() int64 { return int64(z) }
