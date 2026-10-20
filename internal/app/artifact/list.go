// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/thnhdatle/triage/internal/domain/artifact"
)

// FileState is the verification state of an allow-listed file.
type FileState string

// File states.
const (
	FileOK       FileState = "ok"
	FileMissing  FileState = "missing"
	FileMismatch FileState = "checksum-mismatch"
)

// Variants of the bert scorer reported by List.
const (
	VariantNone = "none"
	VariantAuto = "auto"
)

// FileStatus is the state of one file.
type FileStatus struct {
	Path  string
	State FileState
}

// DerivedStatus is the state of one derived artifact per the local manifest.
type DerivedStatus struct {
	Kind    artifact.DerivedKind
	Present bool
	Path    string
}

// LatencyHint is an expected scoring latency measured on the reference Mac.
type LatencyHint struct {
	Variant string
	Tokens  int
	// P50 is the expected median latency, e.g. "61 ms".
	P50 string
}

// Status is the List result for one manifest entry.
type Status struct {
	Name     string
	Revision string
	Files    []FileStatus
	Derived  []DerivedStatus
	// Active is the variant "auto" selects for bert: onnx-int8 if built,
	// else onnx-fp32 if built, else "none". Empty for other entries.
	Active string
	// Latency is the hint table (bert only).
	Latency []LatencyHint
	// SuggestedTimeout is a timeout suggestion for the active variant (bert only).
	SuggestedTimeout string
}

// Complete reports whether all files are verified.
func (s Status) Complete() bool {
	for _, f := range s.Files {
		if f.State != FileOK {
			return false
		}
	}
	return true
}

// bertLatency holds measured p50 hints for the pinned checkpoint on an Apple
// M3 Pro with 4 intra-op threads. 256 and 512 tokens: ONNX Runtime 1.31.0 from
// Python; 128 tokens: Go BenchmarkScoreReal (mean). Other CPUs differ.
func bertLatency() []LatencyHint {
	return []LatencyHint{
		{Variant: string(artifact.KindONNXINT8), Tokens: 128, P50: "31 ms"},
		{Variant: string(artifact.KindONNXINT8), Tokens: 256, P50: "61 ms"},
		{Variant: string(artifact.KindONNXINT8), Tokens: 512, P50: "134 ms"},
		{Variant: string(artifact.KindONNXFP32), Tokens: 128, P50: "61 ms"},
		{Variant: string(artifact.KindONNXFP32), Tokens: 256, P50: "118 ms"},
		{Variant: string(artifact.KindONNXFP32), Tokens: 512, P50: "248 ms"},
	}
}

func (s *Service) fileState(e artifact.Entry, f artifact.File) (FileState, error) {
	err := s.verifier.Verify(filepath.Join(s.entryDir(e), filepath.FromSlash(f.Path)), f)
	switch {
	case err == nil:
		return FileOK, nil
	case errors.Is(err, fs.ErrNotExist):
		return FileMissing, nil
	case errors.Is(err, artifact.ErrChecksum):
		return FileMismatch, nil
	default:
		return "", err
	}
}

// List reports the status of every manifest entry.
func (s *Service) List(ctx context.Context) ([]Status, error) {
	local, err := s.store.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load local manifest: %w", err)
	}
	out := make([]Status, 0, len(s.manifest.Entries))
	for _, e := range s.manifest.Entries {
		st := Status{Name: e.Name, Revision: e.Revision}
		for _, f := range e.Files {
			state, err := s.fileState(e, f)
			if err != nil {
				return nil, fmt.Errorf("verify %s/%s: %w", e.Name, f.Path, err)
			}
			st.Files = append(st.Files, FileStatus{Path: f.Path, State: state})
		}
		for _, m := range e.Derived {
			ds := DerivedStatus{Kind: m.Kind}
			if d, ok := findDerived(local, e.Name, m.Kind); ok && d.Path != "" &&
				s.verifier.Verify(d.Path, artifact.File{SHA256: d.SHA256}) == nil {
				ds.Present, ds.Path = true, d.Path
			}
			st.Derived = append(st.Derived, ds)
		}
		if e.Name == "bert" {
			st.Active = activeVariant(st.Derived)
			st.Latency = bertLatency()
			st.SuggestedTimeout = suggestedTimeout(st.Active)
		}
		out = append(out, st)
	}
	return out, nil
}

func activeVariant(ds []DerivedStatus) string {
	has := map[artifact.DerivedKind]bool{}
	for _, d := range ds {
		has[d.Kind] = d.Present
	}
	switch {
	case has[artifact.KindONNXINT8]:
		return string(artifact.KindONNXINT8)
	case has[artifact.KindONNXFP32]:
		return string(artifact.KindONNXFP32)
	default:
		return VariantNone
	}
}

func suggestedTimeout(active string) string {
	switch active {
	case string(artifact.KindONNXINT8):
		return "300ms" // about twice the 512-token p50 of 134 ms (difficulty.bert.timeout default)
	case string(artifact.KindONNXFP32):
		return "500ms" // about twice the 512-token p50 of 248 ms; set difficulty.bert.timeout
	default:
		return ""
	}
}

// Require returns nil when the named entry's files are verified and its
// required derived artifacts (all except onnx-int8) are built. Otherwise it
// returns artifact.ErrMissing (or artifact.ErrChecksum) wrapped with the hint
// run "triage models pull <name>".
func (s *Service) Require(ctx context.Context, name string) error {
	e, ok := s.manifest.Entry(name)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownName, name)
	}
	hint := fmt.Sprintf("run \"triage models pull %s\"", name)
	local, err := s.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("load local manifest: %w", err)
	}
	for _, f := range e.Files {
		state, err := s.fileState(e, f)
		if err != nil {
			return fmt.Errorf("verify %s/%s: %w", name, f.Path, err)
		}
		switch state {
		case FileMissing:
			return fmt.Errorf("%w: %s file %s: %s", artifact.ErrMissing, name, f.Path, hint)
		case FileMismatch:
			return fmt.Errorf("%w: %s file %s: %s", artifact.ErrChecksum, name, f.Path, hint)
		case FileOK:
		}
	}
	for _, m := range e.Derived {
		if m.Kind == artifact.KindONNXINT8 {
			continue
		}
		d, ok := findDerived(local, name, m.Kind)
		if !ok || d.Path == "" || s.verifier.Verify(d.Path, artifact.File{SHA256: d.SHA256}) != nil {
			return fmt.Errorf("%w: %s derived %s: %s", artifact.ErrMissing, name, m.Kind, hint)
		}
	}
	return nil
}
