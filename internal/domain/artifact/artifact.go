// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"errors"
)

// DerivedKind is the kind of a derived artifact.
type DerivedKind string

// Derived artifact kinds.
const (
	KindONNXFP32 DerivedKind = "onnx-fp32"
	KindONNXINT8 DerivedKind = "onnx-int8"
	KindMFVector DerivedKind = "mf-vector"
)

// File is one allow-listed file of a pinned revision.
type File struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
	Size   int64  `yaml:"size"`
}

// Derived describes an artifact built locally from downloaded files.
type Derived struct {
	Kind             DerivedKind `yaml:"kind"`
	BuiltBy          string      `yaml:"built_by"`
	ConverterVersion string      `yaml:"converter_version"`
	// Path and SHA256 are set in the local manifest after a build.
	Path   string `yaml:"path,omitempty"`
	SHA256 string `yaml:"sha256,omitempty"`
}

// Entry is one pinned checkpoint.
type Entry struct {
	Name         string    `yaml:"name"`
	HFRepo       string    `yaml:"hf_repo"`
	Revision     string    `yaml:"revision"`
	BaseModel    string    `yaml:"base_model"`
	TrainingData string    `yaml:"training_data"`
	Files        []File    `yaml:"files"`
	LicenseFile  string    `yaml:"license_file"`
	Derived      []Derived `yaml:"derived"`
}

// Manifest is the list of pinned checkpoints (models/manifest.yaml).
type Manifest struct {
	SchemaVersion int     `yaml:"schema_version"`
	Entries       []Entry `yaml:"entries"`
}

// Entry returns the entry with the name.
func (m Manifest) Entry(name string) (Entry, bool) {
	for _, e := range m.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Fetcher downloads one allow-listed file of an entry into dir and verifies
// its SHA-256 and size.
type Fetcher interface {
	Fetch(ctx context.Context, e Entry, f File, dir string) error
}

// Deriver builds a derived artifact from an entry's verified files in dir.
type Deriver interface {
	Kind() DerivedKind
	Derive(ctx context.Context, e Entry, dir string) (Derived, error)
}

// Domain errors of artifact handling.
var (
	// ErrChecksum is returned when a file's SHA-256 or size does not match.
	ErrChecksum = errors.New("artifact: checksum mismatch")
	// ErrNotAllowed is returned for a file not on the entry's allow-list.
	ErrNotAllowed = errors.New("artifact: file not on the allow-list")
	// ErrMissing is returned when a required artifact is not present locally.
	ErrMissing = errors.New("artifact: not present locally")
)
