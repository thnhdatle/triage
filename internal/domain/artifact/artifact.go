// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"errors"
)

type DerivedKind string

const (
	KindONNXFP32 DerivedKind = "onnx-fp32"
	KindONNXINT8 DerivedKind = "onnx-int8"
	KindMFVector DerivedKind = "mf-vector"
)

type File struct {
	Path   string `yaml:"path"`
	SHA256 string `yaml:"sha256"`
	Size   int64  `yaml:"size"`
}

type Derived struct {
	Kind             DerivedKind `yaml:"kind"`
	BuiltBy          string      `yaml:"built_by"`
	ConverterVersion string      `yaml:"converter_version"`
	Path             string      `yaml:"path,omitempty"`
	SHA256           string      `yaml:"sha256,omitempty"`
}

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

// Manifest lists the artifacts of a pinned revision.
type Manifest struct {
	SchemaVersion int     `yaml:"schema_version"`
	Entries       []Entry `yaml:"entries"`
}

func (m Manifest) Entry(name string) (Entry, bool) {
	for _, e := range m.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

type Fetcher interface {
	Fetch(ctx context.Context, e Entry, f File, dir string) error
}

type Deriver interface {
	Kind() DerivedKind
	Derive(ctx context.Context, e Entry, dir string) (Derived, error)
}

var (
	ErrChecksum   = errors.New("artifact: checksum mismatch")
	ErrNotAllowed = errors.New("artifact: file not on the allow-list")
	ErrMissing    = errors.New("artifact: not present locally")
)
