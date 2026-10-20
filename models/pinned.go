// SPDX-License-Identifier: Apache-2.0

// Package pinned embeds the committed model files the binary needs at run
// time: the pinned checkpoint manifest (manifest.yaml) and the weight-free
// bert graph (bert/graph.onnx) that `triage models pull bert` installs next to
// the downloaded weights. Nothing derived from weights is
// embedded.
//
//	m, err := pinned.Manifest()
//	graph := pinned.BERTGraph()
package pinned

import (
	"bytes"
	_ "embed" // go:embed of the committed model files
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/thnhdatle/triage/internal/domain/artifact"
)

//go:embed manifest.yaml
var manifestYAML []byte

//go:embed bert/graph.onnx
var bertGraph []byte

// Manifest parses the embedded models/manifest.yaml. Keys outside the
// artifact schema (fixture and ONNX Runtime pins used by scripts) are ignored.
func Manifest() (artifact.Manifest, error) {
	return ParseManifest(manifestYAML)
}

// ParseManifest parses a manifest in the models/manifest.yaml format.
func ParseManifest(data []byte) (artifact.Manifest, error) {
	var m artifact.Manifest
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return artifact.Manifest{}, fmt.Errorf("pinned: parse manifest: %w", err)
	}
	if len(m.Entries) == 0 {
		return artifact.Manifest{}, errors.New("pinned: manifest has no entries")
	}
	return m, nil
}

// BERTGraph returns a copy of the committed weight-free graph.
func BERTGraph() []byte { return bytes.Clone(bertGraph) }
