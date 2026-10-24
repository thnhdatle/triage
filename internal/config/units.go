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

type Duration time.Duration

var dayPrefix = regexp.MustCompile(`^(\d+)d(.*)$`)

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

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

func (d Duration) Std() time.Duration { return time.Duration(d) }

type Size int64

var sizeRe = regexp.MustCompile(`^(\d+)\s*([A-Za-z]*)$`)

var sizeUnits = map[string]int64{
	"": 1, "b": 1,
	"kb": 1000, "mb": 1000 * 1000, "gb": 1000 * 1000 * 1000,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30,
}

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

func (z Size) MarshalYAML() (any, error) { return int64(z), nil }

func (z Size) Bytes() int64 { return int64(z) }
