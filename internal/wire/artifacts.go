// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/thnhdatle/triage/internal/adapter/artifactstore"
	"github.com/thnhdatle/triage/internal/adapter/piionnx"
	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
	"github.com/thnhdatle/triage/internal/adapter/strategy/mf"
	"github.com/thnhdatle/triage/internal/domain/artifact"
	pinned "github.com/thnhdatle/triage/models"
)

// Artifact entry names in models/manifest.yaml.
const (
	entryBERT = "bert"
	entryMF   = "mf"
	entryNER  = "ner"
)

// artifacts locates pulled model files under models.cache_dir. Presence is
// checked with stat and the local manifest; full SHA-256 verification is
// `triage models list` (hashing 1.5 GB on every reload would stall it).
type artifacts struct {
	cacheDir string
	pinned   artifact.Manifest
	local    artifact.Manifest
}

func loadArtifacts(ctx context.Context, cacheDir string) (*artifacts, error) {
	m, err := pinned.Manifest()
	if err != nil {
		return nil, err
	}
	st, err := artifactstore.New(cacheDir)
	if err != nil {
		return nil, fmt.Errorf("models.cache_dir: %w", err)
	}
	local, err := st.Load(ctx)
	if err != nil {
		return nil, err
	}
	return &artifacts{cacheDir: cacheDir, pinned: m, local: local}, nil
}

// missingError names the pull command that fixes a missing artifact.
func missingError(name, what, path string) error {
	return fmt.Errorf("%w: %s %s not found at %s; run \"triage models pull %s\"",
		artifact.ErrMissing, name, what, path, name)
}

func (a *artifacts) dir(name string) string {
	e, _ := a.pinned.Entry(name)
	return filepath.Join(a.cacheDir, e.Name, e.Revision)
}

// derived returns the recorded derived artifact of a kind whose file exists.
func (a *artifacts) derived(name string, k artifact.DerivedKind, fallbackFile string) (path, sha string, ok bool) {
	if e, found := a.local.Entry(name); found {
		for _, d := range e.Derived {
			if d.Kind == k && d.Path != "" && exists(d.Path) {
				return d.Path, d.SHA256, true
			}
		}
	}
	p := filepath.Join(a.dir(name), fallbackFile)
	if exists(p) {
		return p, "", true
	}
	return "", "", false
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (a *artifacts) requireFile(name, rel string) (string, error) {
	p := filepath.Join(a.dir(name), filepath.FromSlash(rel))
	if _, err := os.Stat(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", missingError(name, rel, p)
		}
		return "", fmt.Errorf("%s %s: %w", name, rel, err)
	}
	return p, nil
}

// bertWeightsFile is the downloaded checkpoint the FP32 graph points into.
const bertWeightsFile = "model.safetensors"

// bertTokenizer is the XLM-R tokenizer.json of the bert entry.
func (a *artifacts) bertTokenizer() (string, error) {
	return a.requireFile(entryBERT, "tokenizer.json")
}

// bertFiles resolves bert.variant (auto: INT8 if built, else FP32).
type bertFiles struct {
	Tokenizer string
	Model     string
	Variant   bert.Variant
	// Key identifies the artifact for strategy reuse across reloads.
	Key string
}

func (a *artifacts) bert(variant string) (bertFiles, error) {
	tok, err := a.bertTokenizer()
	if err != nil {
		return bertFiles{}, err
	}
	if variant == "auto" || variant == string(bert.VariantINT8) {
		if p, sha, ok := a.derived(entryBERT, artifact.KindONNXINT8, bert.INT8FileName); ok {
			return bertFiles{Tokenizer: tok, Model: p, Variant: bert.VariantINT8, Key: fileKey(p, sha)}, nil
		}
		if variant != "auto" {
			return bertFiles{}, fmt.Errorf("%w: bert int8 model not built in %s; run \"triage models pull bert --int8\"",
				artifact.ErrMissing, a.dir(entryBERT))
		}
	}
	if _, err := a.requireFile(entryBERT, bertWeightsFile); err != nil {
		return bertFiles{}, err
	}
	p, sha, ok := a.derived(entryBERT, artifact.KindONNXFP32, bert.FP32FileName)
	if !ok {
		return bertFiles{}, missingError(entryBERT, "onnx-fp32 graph", filepath.Join(a.dir(entryBERT), bert.FP32FileName))
	}
	return bertFiles{Tokenizer: tok, Model: p, Variant: bert.VariantFP32, Key: fileKey(p, sha)}, nil
}

// mfVector is the derived mf vector u.
func (a *artifacts) mfVector() (path, key string, err error) {
	p, sha, ok := a.derived(entryMF, artifact.KindMFVector, mf.VectorFileName)
	if !ok {
		return "", "", missingError(entryMF, "vector", filepath.Join(a.dir(entryMF), mf.VectorFileName))
	}
	return p, fileKey(p, sha), nil
}

// ner returns the published dslim/bert-base-NER files.
func (a *artifacts) ner() (piionnx.Files, string, error) {
	var f piionnx.Files
	var err error
	if f.Model, err = a.requireFile(entryNER, "onnx/model.onnx"); err != nil {
		return f, "", err
	}
	if f.Tokenizer, err = a.requireFile(entryNER, "onnx/tokenizer.json"); err != nil {
		return f, "", err
	}
	if f.Config, err = a.requireFile(entryNER, "onnx/config.json"); err != nil {
		return f, "", err
	}
	return f, fileKey(f.Model, ""), nil
}

// fileKey identifies an artifact file: its recorded SHA-256 when known, else
// its path, size and modification time.
func fileKey(path, sha string) string {
	if sha != "" {
		return "sha256:" + sha
	}
	st, err := os.Stat(path)
	if err != nil {
		return path
	}
	return fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
}
