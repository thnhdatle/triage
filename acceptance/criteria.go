// SPDX-License-Identifier: Apache-2.0

// Package acceptance holds the acceptance criteria loader shared by the
// acceptance tests (build tag acceptance) and the runner in cmd/runner.
package acceptance

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Environment names selecting a threshold set.
const (
	EnvMac = "mac"
	EnvCI  = "ci"
)

// File is the parsed criteria.yaml.
type File struct {
	Project  string      `yaml:"project"`
	Machine  string      `yaml:"machine"`
	Criteria []Criterion `yaml:"criteria"`
}

// Criterion is one acceptance criterion.
type Criterion struct {
	ID       string   `yaml:"id"`
	Title    string   `yaml:"title"`
	Test     string   `yaml:"test"`
	Kind     string   `yaml:"kind"`
	RealDeps []string `yaml:"real_deps"`
	Requires []string `yaml:"requires"`
	Pass     Pass     `yaml:"pass"`
}

// Pass holds the thresholds per environment.
type Pass struct {
	Mac Threshold `yaml:"mac"`
	CI  Threshold `yaml:"ci"`
}

// Threshold is either a map of named limits or the scalar "skip".
type Threshold struct {
	Skip   bool
	Values map[string]any
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (th *Threshold) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Value != "skip" {
			return fmt.Errorf("line %d: threshold scalar must be \"skip\", got %q", n.Line, n.Value)
		}
		th.Skip = true
		return nil
	}
	if err := n.Decode(&th.Values); err != nil {
		return fmt.Errorf("decode thresholds: %w", err)
	}
	return nil
}

// Load reads and parses a criteria file.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is chosen by the caller
	if err != nil {
		return nil, fmt.Errorf("read criteria: %w", err)
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse criteria %s: %w", path, err)
	}
	return &f, nil
}

// Thresholds returns the limits for env ("mac" or "ci"). skip is true when the
// criterion is marked `ci: skip` (or `mac: skip`) for that environment.
func (c *Criterion) Thresholds(env string) (map[string]any, bool) {
	th := c.Pass.Mac
	if env == EnvCI {
		th = c.Pass.CI
	}
	if th.Skip {
		return nil, true
	}
	return th.Values, false
}

// Find returns the criterion with the given id.
func (f *File) Find(id string) (*Criterion, bool) {
	for i := range f.Criteria {
		if f.Criteria[i].ID == id {
			return &f.Criteria[i], true
		}
	}
	return nil, false
}
