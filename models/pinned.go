// SPDX-License-Identifier: Apache-2.0

// Package pinned embeds the pinned model manifest and the BERT graph.
package pinned

import (
	"bytes"
	_ "embed"
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

// Manifest parses the embedded manifest.yaml.
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

func BERTGraph() []byte { return bytes.Clone(bertGraph) }
