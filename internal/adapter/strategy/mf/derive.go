// SPDX-License-Identifier: Apache-2.0

package mf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/thnhdatle/triage/internal/domain/artifact"
)

// File names in the mf entry directory.
const (
	WeightsFileName = "model.safetensors"
	VectorFileName  = "mf-vector.safetensors"
)

// Deriver computes u in Go from the downloaded model.safetensors and writes
// it as a one-tensor safetensors file (F64 [1536]) with provenance metadata.
type Deriver struct{}

var _ artifact.Deriver = Deriver{}

// Kind implements artifact.Deriver.
func (Deriver) Kind() artifact.DerivedKind { return artifact.KindMFVector }

// Derive implements artifact.Deriver. The output is written atomically and
// is byte-for-byte reproducible for the same weights.
func (Deriver) Derive(ctx context.Context, _ artifact.Entry, dir string) (artifact.Derived, error) {
	if err := ctx.Err(); err != nil {
		return artifact.Derived{}, fmt.Errorf("mf: derive: %w", err)
	}
	src := filepath.Join(dir, WeightsFileName)
	b, err := os.ReadFile(src) //nolint:gosec // G304: the entry directory of the artifact cache.
	if err != nil {
		return artifact.Derived{}, fmt.Errorf("mf: derive: %w", err)
	}
	f, err := ParseSafetensors(b)
	if err != nil {
		return artifact.Derived{}, fmt.Errorf("mf: derive %s: %w", src, err)
	}
	u, err := ComputeU(f, StrongRow, WeakRow)
	if err != nil {
		return artifact.Derived{}, fmt.Errorf("mf: derive %s: %w", src, err)
	}
	if len(u) != Dimensions {
		return artifact.Derived{}, fmt.Errorf("mf: derive %s: %w: vector has %d elements, want %d",
			src, ErrSafetensors, len(u), Dimensions)
	}
	sum := sha256.Sum256(b)
	out, err := encodeF64(VectorTensor, u, map[string]string{
		"triage.source.file":   WeightsFileName,
		"triage.source.sha256": hex.EncodeToString(sum[:]),
		"triage.strong_row":    strconv.Itoa(StrongRow),
		"triage.weak_row":      strconv.Itoa(WeakRow),
		"triage.formula":       "u = text_proj^T (classifier[0] * (normalize(P[strong]) - normalize(P[weak])))",
	})
	if err != nil {
		return artifact.Derived{}, err
	}
	path := filepath.Join(dir, VectorFileName)
	if err := writeAtomic(path, out); err != nil {
		return artifact.Derived{}, err
	}
	vs := sha256.Sum256(out)
	return artifact.Derived{Kind: artifact.KindMFVector, Path: path, SHA256: hex.EncodeToString(vs[:])}, nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.part")
	if err != nil {
		return fmt.Errorf("mf: write %s: %w", path, err)
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(name, path)
	}
	if werr != nil {
		_ = os.Remove(name)
		return fmt.Errorf("mf: write %s: %w", path, werr)
	}
	return nil
}
