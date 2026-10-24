// SPDX-License-Identifier: Apache-2.0

package bert

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

var ErrWeightsMismatch = errors.New("bert: model.safetensors does not match the graph")

type weightsRef struct {
	file   string
	sha256 string
	size   int64
}

func weightsRefOf(meta map[string]string) (weightsRef, error) {
	ref := weightsRef{file: meta[MetaWeightsFile], sha256: meta[MetaWeightsSHA256]}
	if ref.file == "" || ref.sha256 == "" || meta[MetaWeightsSize] == "" {
		return weightsRef{}, fmt.Errorf("%w: metadata %s, %s and %s are required",
			ErrInvalidModel, MetaWeightsFile, MetaWeightsSHA256, MetaWeightsSize)
	}
	if filepath.Base(ref.file) != ref.file || ref.file == "." || ref.file == ".." {
		return weightsRef{}, fmt.Errorf("%w: %s %q is not a plain file name", ErrInvalidModel, MetaWeightsFile, ref.file)
	}
	size, err := strconv.ParseInt(meta[MetaWeightsSize], 10, 64)
	if err != nil || size < 0 {
		return weightsRef{}, fmt.Errorf("%w: %s %q", ErrInvalidModel, MetaWeightsSize, meta[MetaWeightsSize])
	}
	ref.size = size
	return ref, nil
}

func (r weightsRef) verify(ctx context.Context, dir string) error {
	path := filepath.Join(dir, r.file)
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("bert: weights: %w", err)
	}
	if st.Size() != r.size {
		return fmt.Errorf("%w: %s has %d bytes, graph expects %d", ErrWeightsMismatch, path, st.Size(), r.size)
	}
	sum, err := fileSHA256(ctx, path)
	if err != nil {
		return err
	}
	if sum != r.sha256 {
		return fmt.Errorf("%w: %s has sha256 %s, graph expects %s", ErrWeightsMismatch, path, sum, r.sha256)
	}
	return nil
}

func fileSHA256(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: artifact paths come from configuration or the cache.
	if err != nil {
		return "", fmt.Errorf("bert: hash: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("bert: hash %s: %w", path, err)
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		if err != nil {
			return "", fmt.Errorf("bert: hash %s: %w", path, err)
		}
	}
}
