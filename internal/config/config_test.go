// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/thnhdatle/triage/internal/config"
)

const examplePath = "../../deploy/config.example.yaml"

func TestExampleLoadsAndValidates(t *testing.T) {
	t.Parallel()
	cfg, err := config.Load(examplePath)
	require.NoError(t, err)
	assert.Equal(t, "triage-auto", cfg.Routes[0].Name)
	assert.InDelta(t, 0.62, cfg.Routes[0].Threshold, 1e-9)
	assert.Equal(t, int64(8<<20), cfg.API.MaxBodyBytes.Bytes())
	assert.Equal(t, 90*time.Second, cfg.Targets[1].AttemptTimeout.Std())
}

func TestQuickstartLoads(t *testing.T) {
	t.Parallel()
	_, err := config.Load("../../deploy/config.quickstart.yaml")
	require.NoError(t, err)
}

// TestExampleDocumentsEveryField fails when a struct field is missing from
// deploy/config.example.yaml or lacks a comment there.
func TestExampleDocumentsEveryField(t *testing.T) {
	t.Parallel()
	raw, err := readFile(examplePath)
	require.NoError(t, err)
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Content)

	var issues []string
	walk(reflect.TypeOf(config.Config{}), []*yaml.Node{doc.Content[0]}, "", &issues)
	assert.Empty(t, issues)
}

func mappingValue(m *yaml.Node, key string) (k, v *yaml.Node) {
	if m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

func hasComment(k *yaml.Node, v *yaml.Node) bool {
	for _, n := range []*yaml.Node{k, v} {
		if strings.TrimSpace(n.HeadComment+n.LineComment+n.FootComment) != "" {
			return true
		}
	}
	return false
}

// walk checks every field of struct type t against the example nodes that
// represent it (several for slices and maps of structs).
func walk(t reflect.Type, nodes []*yaml.Node, path string, issues *[]string) {
	for i := range t.NumField() {
		f := t.Field(i)
		key := strings.Split(f.Tag.Get("yaml"), ",")[0]
		p := strings.TrimPrefix(path+"."+key, ".")
		var children []*yaml.Node
		documented := false
		for _, n := range nodes {
			k, v := mappingValue(n, key)
			if k == nil {
				continue
			}
			children = append(children, v)
			documented = documented || hasComment(k, v)
		}
		if len(children) == 0 {
			*issues = append(*issues, p+": missing from example")
			continue
		}
		section := f.Type.Kind() == reflect.Struct // a section header needs no comment of its own
		if !documented && !section {
			*issues = append(*issues, p+": no comment in example")
		}
		walk2(f.Type, children, p, issues)
	}
}

func walk2(ft reflect.Type, children []*yaml.Node, p string, issues *[]string) {
	switch {
	case ft.Kind() == reflect.Struct:
		walk(ft, children, p, issues)
	case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
		var items []*yaml.Node
		for _, c := range children {
			items = append(items, c.Content...)
		}
		walk(ft.Elem(), items, p+"[]", issues)
	case ft.Kind() == reflect.Map && ft.Elem().Kind() == reflect.Struct:
		var vals []*yaml.Node
		for _, c := range children {
			for j := 1; j < len(c.Content); j += 2 {
				vals = append(vals, c.Content[j])
			}
		}
		walk(ft.Elem(), vals, p+".*", issues)
	}
}

func TestDefaultsApplied(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse([]byte(`
routes:
  - name: r
`))
	require.NoError(t, err)
	d := config.Default()
	assert.Equal(t, d.Server, cfg.Server)
	assert.Equal(t, d.Dispatch, cfg.Dispatch)
	assert.Equal(t, 10000, cfg.Decisions.BufferSize)
	r := cfg.Routes[0]
	assert.Equal(t, "none", r.Strategy)
	assert.InDelta(t, 0.5, r.Threshold, 1e-9)
	assert.Equal(t, "weak", r.DefaultTier)
	assert.Equal(t, "escalate", r.OnTierUnavailable)
	assert.Equal(t, "mask", r.Privacy.Mode)
	assert.Equal(t, "local_only", r.Privacy.MaxClientMode, "default")
}

func TestParseOverridesAndEmpty(t *testing.T) {
	t.Parallel()
	cfg, err := config.Parse([]byte("dispatch: { max_attempts: 5 }\nprivacy: { detectors: { street_address: true } }\n"))
	require.NoError(t, err)
	assert.Equal(t, 5, cfg.Dispatch.MaxAttempts)
	assert.Equal(t, 60*time.Second, cfg.Dispatch.AttemptTimeout.Std(), "untouched keys keep defaults")
	assert.True(t, cfg.Privacy.Detectors["street_address"])

	empty, err := config.Parse(nil)
	require.NoError(t, err)
	assert.Equal(t, "priority", empty.Selection.Mode)
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "unknown top-level key", yaml: "bogus: 1\n", want: "field bogus not found"},
		{name: "unknown nested key", yaml: "server:\n  listn: x\n", want: "field listn not found"},
		{name: "unknown key in list item", yaml: "targets:\n  - nam: x\n", want: "field nam not found"},
		{name: "bad type", yaml: "dispatch:\n  max_attempts: many\n", want: "cannot unmarshal"},
		{name: "bad duration", yaml: "health:\n  interval: soon\n", want: "invalid duration"},
		{name: "duration not a string", yaml: "health:\n  interval: [1]\n", want: "duration must be a string"},
		{name: "bad size", yaml: "api:\n  max_body_bytes: 8XiB\n", want: "unknown unit"},
		{name: "size not scalar", yaml: "api:\n  max_body_bytes: [1]\n", want: "size must be"},
		{name: "syntax", yaml: "server: [\n", want: "parse"},
		{name: "two documents", yaml: "server: {}\n---\nserver: {}\n", want: "multiple YAML documents"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Parse([]byte(tc.yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()
	_, err := config.Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.ErrorContains(t, err, "read")

	path := writeConfig(t, "bogus: 1\n")
	_, err = config.Load(path)
	require.ErrorContains(t, err, "bogus")

	path = writeConfig(t, "routes: []\n")
	_, err = config.Load(path)
	var ve *config.ValidationError
	require.ErrorAs(t, err, &ve)
}

func TestThresholdExplicitZeroIsKept(t *testing.T) {
	t.Parallel()
	cases := map[string]float64{"": 0.5, "threshold: 0": 0, "threshold: 0.0": 0, "threshold: 1": 1, "threshold: 0.3": 0.3}
	for in, want := range cases {
		cfg, err := config.Parse([]byte(validYAML + "    " + in + "\n"))
		require.NoError(t, err, in)
		assert.InDelta(t, want, cfg.Routes[0].Threshold, 1e-12, in)
		require.NoError(t, config.Validate(cfg), in)
	}
	cfg, err := config.Parse([]byte(validYAML + "    threshold: 1.5\n"))
	require.NoError(t, err)
	assert.ErrorContains(t, config.Validate(cfg), "threshold: must be within [0, 1]")
}

func TestDetectorsDefaultAndOverride(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	assert.Equal(t, map[string]bool{"street_address": false}, cfg.Privacy.Detectors)
	cfg, err := config.Parse([]byte(validYAML + "privacy: { detectors: { email: false, street_address: true } }\n"))
	require.NoError(t, err)
	require.NoError(t, config.Validate(cfg))
	assert.False(t, cfg.Privacy.Detectors["email"])
	assert.True(t, cfg.Privacy.Detectors["street_address"])
}
