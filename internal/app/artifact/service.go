// SPDX-License-Identifier: Apache-2.0

package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/thnhdatle/triage/internal/domain/artifact"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

var ErrUnknownName = errors.New("artifact: unknown artifact name")

var ErrNoDeriver = errors.New("artifact: no deriver for kind")

var ErrInvalid = errors.New("artifact: invalid argument")

const AllName = "all"

const forbiddenFile = "optimizer.pt"

type LocalStore interface {
	Load(ctx context.Context) (artifact.Manifest, error)
	Save(ctx context.Context, m artifact.Manifest) error
}

type Verifier interface {
	Verify(path string, f artifact.File) error
}

type FileVerifier struct{}

func (FileVerifier) Verify(p string, f artifact.File) error {
	fh, err := os.Open(p) //nolint:gosec // path built from cache dir and manifest
	if err != nil {
		return fmt.Errorf("open %s: %w", p, err)
	}
	defer func() { _ = fh.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, fh)
	if err != nil {
		return fmt.Errorf("read %s: %w", p, err)
	}
	if f.Size > 0 && n != f.Size {
		return fmt.Errorf("%w: %s size %d, want %d", artifact.ErrChecksum, p, n, f.Size)
	}
	if f.SHA256 != "" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, f.SHA256) {
			return fmt.Errorf("%w: %s sha256 %s, want %s", artifact.ErrChecksum, p, got, f.SHA256)
		}
	}
	return nil
}

type Option func(*Service)

func WithClock(c clock.Clock) Option { return func(s *Service) { s.clock = c } }

func WithVerifier(v Verifier) Option { return func(s *Service) { s.verifier = v } }

type PullOptions struct {
	INT8 bool
}

type EntryReport struct {
	Name           string
	Fetched        []string
	Skipped        []string
	DerivedBuilt   []artifact.Derived
	DerivedSkipped []artifact.DerivedKind
	Duration       time.Duration
}

type Report struct {
	Entries  []EntryReport
	Duration time.Duration
}

// Service pulls, lists and verifies model artifacts.
type Service struct {
	manifest artifact.Manifest
	fetcher  artifact.Fetcher
	derivers map[artifact.DerivedKind]artifact.Deriver
	store    LocalStore
	cacheDir string
	clock    clock.Clock
	verifier Verifier
}

// New returns a Service that works in cacheDir.
func New(manifest artifact.Manifest, fetcher artifact.Fetcher, derivers []artifact.Deriver,
	store LocalStore, cacheDir string, opts ...Option,
) (*Service, error) {
	switch {
	case fetcher == nil:
		return nil, fmt.Errorf("%w: nil fetcher", ErrInvalid)
	case store == nil:
		return nil, fmt.Errorf("%w: nil store", ErrInvalid)
	case cacheDir == "":
		return nil, fmt.Errorf("%w: empty cache dir", ErrInvalid)
	case len(manifest.Entries) == 0:
		return nil, fmt.Errorf("%w: empty manifest", ErrInvalid)
	}
	s := &Service{
		manifest: manifest, fetcher: fetcher, store: store, cacheDir: cacheDir,
		derivers: make(map[artifact.DerivedKind]artifact.Deriver, len(derivers)),
		clock:    clock.Real(), verifier: FileVerifier{},
	}
	for _, d := range derivers {
		if d == nil {
			return nil, fmt.Errorf("%w: nil deriver", ErrInvalid)
		}
		s.derivers[d.Kind()] = d
	}
	for _, o := range opts {
		o(s)
	}
	for _, e := range manifest.Entries {
		if e.Name == "" || e.Name == AllName || e.Revision == "" {
			return nil, fmt.Errorf("%w: entry %q needs a name and revision", ErrInvalid, e.Name)
		}
		for _, f := range e.Files {
			if err := checkAllowed(e, f); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

func checkAllowed(e artifact.Entry, f artifact.File) error {
	listed := false
	for _, g := range e.Files {
		if g.Path == f.Path {
			listed = true
			break
		}
	}
	clean := path.Clean(f.Path)
	if !listed || path.Base(clean) == forbiddenFile || !fs.ValidPath(clean) || clean != f.Path {
		return fmt.Errorf("%w: %s/%s", artifact.ErrNotAllowed, e.Name, f.Path)
	}
	return nil
}

func (s *Service) entryDir(e artifact.Entry) string {
	return filepath.Join(s.cacheDir, e.Name, e.Revision)
}

func (s *Service) resolve(names []string) ([]artifact.Entry, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("%w: no artifact names", ErrInvalid)
	}
	var out []artifact.Entry
	seen := map[string]bool{}
	add := func(e artifact.Entry) {
		if !seen[e.Name] {
			seen[e.Name] = true
			out = append(out, e)
		}
	}
	for _, n := range names {
		if n == AllName {
			for _, e := range s.manifest.Entries {
				add(e)
			}
			continue
		}
		e, ok := s.manifest.Entry(n)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownName, n)
		}
		add(e)
	}
	return out, nil
}

// Pull fetches, verifies and derives the named entries and writes the local manifest.
func (s *Service) Pull(ctx context.Context, names []string, opts PullOptions) (Report, error) {
	entries, err := s.resolve(names)
	if err != nil {
		return Report{}, err
	}
	start := s.clock.Now()
	var rep Report
	for _, e := range entries {
		er, err := s.pullEntry(ctx, e, opts)
		rep.Entries = append(rep.Entries, er)
		if err != nil {
			rep.Duration = s.clock.Now().Sub(start)
			return rep, fmt.Errorf("pull %s: %w", e.Name, err)
		}
	}
	rep.Duration = s.clock.Now().Sub(start)
	return rep, nil
}

func (s *Service) pullEntry(ctx context.Context, e artifact.Entry, opts PullOptions) (EntryReport, error) {
	start := s.clock.Now()
	er := EntryReport{Name: e.Name}
	defer func() { er.Duration = s.clock.Now().Sub(start) }()
	dir := s.entryDir(e)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return er, fmt.Errorf("create %s: %w", dir, err)
	}
	if err := s.fetchFiles(ctx, e, dir, &er); err != nil {
		return er, err
	}
	kinds, err := s.wantedKinds(e, opts)
	if err != nil || len(kinds) == 0 {
		return er, err
	}
	return er, s.deriveKinds(ctx, e, dir, kinds, &er)
}

func (s *Service) fetchFiles(ctx context.Context, e artifact.Entry, dir string, er *EntryReport) error {
	for _, f := range e.Files {
		if err := checkAllowed(e, f); err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(f.Path))
		if s.verifier.Verify(target, f) == nil {
			er.Skipped = append(er.Skipped, f.Path)
			continue
		}
		if err := s.fetcher.Fetch(ctx, e, f, dir); err != nil {
			return fmt.Errorf("fetch %s: %w", f.Path, err)
		}
		if err := s.verifier.Verify(target, f); err != nil {
			return fmt.Errorf("verify %s: %w", f.Path, err)
		}
		er.Fetched = append(er.Fetched, f.Path)
	}
	return nil
}

func (s *Service) deriveKinds(ctx context.Context, e artifact.Entry, dir string,
	kinds []artifact.DerivedKind, er *EntryReport,
) error {
	local, err := s.store.Load(ctx)
	if err != nil {
		return fmt.Errorf("load local manifest: %w", err)
	}
	for _, k := range kinds {
		if recorded, ok := findDerived(local, e.Name, k); ok && recorded.Path != "" &&
			s.verifier.Verify(recorded.Path, artifact.File{SHA256: recorded.SHA256}) == nil {
			er.DerivedSkipped = append(er.DerivedSkipped, k)
			continue
		}
		d, err := s.derivers[k].Derive(ctx, e, dir)
		if err != nil {
			return fmt.Errorf("derive %s: %w", k, err)
		}
		d = completeDerived(e, k, d)
		local = mergeDerived(local, e.Name, d)
		if err := s.store.Save(ctx, local); err != nil {
			return fmt.Errorf("save local manifest: %w", err)
		}
		er.DerivedBuilt = append(er.DerivedBuilt, d)
	}
	return nil
}

func (s *Service) wantedKinds(e artifact.Entry, opts PullOptions) ([]artifact.DerivedKind, error) {
	var out []artifact.DerivedKind
	for _, d := range e.Derived {
		if d.Kind == artifact.KindONNXINT8 && !opts.INT8 {
			continue
		}
		if _, ok := s.derivers[d.Kind]; !ok {
			return nil, fmt.Errorf("%w: %s (entry %s)", ErrNoDeriver, d.Kind, e.Name)
		}
		out = append(out, d.Kind)
	}
	return out, nil
}

func completeDerived(e artifact.Entry, k artifact.DerivedKind, d artifact.Derived) artifact.Derived {
	d.Kind = k
	for _, m := range e.Derived {
		if m.Kind != k {
			continue
		}
		if d.BuiltBy == "" {
			d.BuiltBy = m.BuiltBy
		}
		if d.ConverterVersion == "" {
			d.ConverterVersion = m.ConverterVersion
		}
	}
	return d
}

func findDerived(m artifact.Manifest, name string, k artifact.DerivedKind) (artifact.Derived, bool) {
	e, ok := m.Entry(name)
	if !ok {
		return artifact.Derived{}, false
	}
	for _, d := range e.Derived {
		if d.Kind == k {
			return d, true
		}
	}
	return artifact.Derived{}, false
}

func mergeDerived(m artifact.Manifest, name string, d artifact.Derived) artifact.Manifest {
	out := artifact.Manifest{SchemaVersion: m.SchemaVersion, Entries: make([]artifact.Entry, len(m.Entries))}
	copy(out.Entries, m.Entries)
	for i := range out.Entries {
		if out.Entries[i].Name != name {
			continue
		}
		ds := append([]artifact.Derived(nil), out.Entries[i].Derived...)
		for j := range ds {
			if ds[j].Kind == d.Kind {
				ds[j] = d
				out.Entries[i].Derived = ds
				return out
			}
		}
		ds = append(ds, d)
		out.Entries[i].Derived = ds
		return out
	}
	out.Entries = append(out.Entries, artifact.Entry{Name: name, Derived: []artifact.Derived{d}})
	return out
}
