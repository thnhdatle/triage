// SPDX-License-Identifier: Apache-2.0

package artifactstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/thnhdatle/triage/internal/domain/artifact"
)

// FileName is the local manifest's file name inside the cache directory.
const FileName = "manifest.local.yaml"

// SchemaVersion is the schema version written to a new local manifest.
const SchemaVersion = 1

// ErrInvalid is returned for invalid constructor arguments.
var ErrInvalid = errors.New("artifactstore: invalid argument")

// Store reads and writes the local manifest. It is safe for concurrent use
// within one process.
type Store struct {
	path string
	mu   sync.Mutex
}

// New returns a Store keeping its manifest in cacheDir/manifest.local.yaml.
// The directory is created on the first Save.
func New(cacheDir string) (*Store, error) {
	if cacheDir == "" {
		return nil, fmt.Errorf("%w: empty cache dir", ErrInvalid)
	}
	return &Store{path: filepath.Join(cacheDir, FileName)}, nil
}

// Path returns the manifest file path.
func (s *Store) Path() string { return s.path }

// Load returns the saved manifest, or an empty manifest when none was saved
// yet. Unknown fields are an error.
func (s *Store) Load(ctx context.Context) (artifact.Manifest, error) {
	if err := ctx.Err(); err != nil {
		return artifact.Manifest{}, fmt.Errorf("load local manifest: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return artifact.Manifest{SchemaVersion: SchemaVersion}, nil
	}
	if err != nil {
		return artifact.Manifest{}, fmt.Errorf("read %s: %w", s.path, err)
	}
	var m artifact.Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		return artifact.Manifest{}, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return m, nil
}

// Save atomically replaces the manifest.
func (s *Store) Save(ctx context.Context, m artifact.Manifest) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("save local manifest: %w", err)
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = SchemaVersion
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode local manifest: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, FileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after a successful rename
	_, werr := tmp.Write(data)
	serr := tmp.Sync()
	cerr := tmp.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("replace %s: %w", s.path, err)
	}
	return nil
}
