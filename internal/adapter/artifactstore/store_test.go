// SPDX-License-Identifier: Apache-2.0

package artifactstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thnhdatle/triage/internal/adapter/artifactstore"
	"github.com/thnhdatle/triage/internal/domain/artifact"
)

func sample() artifact.Manifest {
	return artifact.Manifest{SchemaVersion: 1, Entries: []artifact.Entry{{
		Name: "bert", HFRepo: "r/b", Revision: "abc",
		Files: []artifact.File{{Path: "config.json", SHA256: "aa", Size: 3}},
		Derived: []artifact.Derived{{
			Kind: artifact.KindONNXFP32, BuiltBy: "go", ConverterVersion: "1", Path: "/x/y.onnx", SHA256: "bb",
		}},
	}}}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", "cache")
	s, err := artifactstore.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "manifest.local.yaml"); s.Path() != want {
		t.Fatalf("path %s", s.Path())
	}
	ctx := context.Background()
	empty, err := s.Load(ctx)
	if err != nil || len(empty.Entries) != 0 || empty.SchemaVersion != artifactstore.SchemaVersion {
		t.Fatalf("empty load: %+v %v", empty, err)
	}
	if err := s.Save(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, sample()) {
		t.Fatalf("got %+v", got)
	}
	m := sample()
	m.SchemaVersion = 0
	m.Entries[0].Revision = "def"
	if err := s.Save(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Load(ctx)
	if got.Entries[0].Revision != "def" || got.SchemaVersion != 1 {
		t.Fatalf("got %+v", got)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("leftover files: %v", files)
	}
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, _ := artifactstore.New(dir)
	ctx := context.Background()
	if err := os.WriteFile(s.Path(), []byte("entries: [unknown_field: 1]\nbogus: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := os.WriteFile(s.Path(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Load(ctx); err != nil || len(m.Entries) != 0 {
		t.Fatalf("empty file: %+v %v", m, err)
	}
	d2 := t.TempDir()
	s2, _ := artifactstore.New(d2)
	if err := os.Mkdir(s2.Path(), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Load(ctx); err == nil {
		t.Fatal("expected read error")
	}
	if err := s2.Save(ctx, sample()); err == nil {
		t.Fatal("expected replace error")
	}
}

func TestSaveErrors(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := artifactstore.New(filepath.Join(file, "sub"))
	if err := s.Save(context.Background(), sample()); err == nil {
		t.Fatal("expected mkdir error")
	}
}

func TestContextAndNew(t *testing.T) {
	t.Parallel()
	if _, err := artifactstore.New(""); !errors.Is(err, artifactstore.ErrInvalid) {
		t.Fatalf("err %v", err)
	}
	s, _ := artifactstore.New(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if err := s.Save(ctx, sample()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestSaveReadOnlyDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // test: make the dir read-only
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // restore for cleanup
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	s, _ := artifactstore.New(dir)
	if err := s.Save(context.Background(), sample()); err == nil {
		t.Fatal("expected temp file error")
	}
}
