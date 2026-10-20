// SPDX-License-Identifier: Apache-2.0

package bert_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/strategy/bert"
	"github.com/thnhdatle/triage/internal/domain/artifact"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(modelsDir, name))
	require.NoError(t, err)
	return b
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// entryDir returns a directory holding a copy of the fixture weights.
func entryDir(t *testing.T) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	w := readFixture(t, "model.safetensors")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "model.safetensors"), w, 0o600))
	return dir, w
}

func TestFP32Deriver(t *testing.T) {
	t.Parallel()
	graph := readFixture(t, bert.FP32FileName)
	d, err := bert.NewFP32Deriver(graph)
	require.NoError(t, err)
	assert.Equal(t, artifact.KindONNXFP32, d.Kind())

	dir, w := entryDir(t)
	e := artifact.Entry{Name: "bert", Files: []artifact.File{
		{Path: "config.json", SHA256: "x", Size: 1},
		{Path: "model.safetensors", SHA256: sha(w), Size: int64(len(w))},
	}}
	got, err := d.Derive(t.Context(), e, dir)
	require.NoError(t, err)
	assert.Equal(t, artifact.Derived{
		Kind: artifact.KindONNXFP32, Path: filepath.Join(dir, bert.FP32FileName),
		SHA256: sha(graph),
	}, got)
	written, err := os.ReadFile(got.Path)
	require.NoError(t, err)
	assert.Equal(t, graph, written)
	left, err := filepath.Glob(filepath.Join(dir, "*.part"))
	require.NoError(t, err)
	assert.Empty(t, left, "no temp files left")

	again, err := d.Derive(t.Context(), e, dir)
	require.NoError(t, err, "idempotent")
	assert.Equal(t, got, again)
}

func TestFP32DeriverErrors(t *testing.T) {
	t.Parallel()
	graph := readFixture(t, bert.FP32FileName)
	d, err := bert.NewFP32Deriver(graph)
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("manifest pins other weights", func(t *testing.T) {
		t.Parallel()
		dir, w := entryDir(t)
		e := artifact.Entry{Files: []artifact.File{{Path: "model.safetensors", SHA256: "00", Size: int64(len(w))}}}
		_, err := d.Derive(t.Context(), e, dir)
		require.ErrorIs(t, err, bert.ErrWeightsMismatch)
	})
	t.Run("missing weights", func(t *testing.T) {
		t.Parallel()
		_, err := d.Derive(t.Context(), artifact.Entry{}, t.TempDir())
		require.ErrorIs(t, err, fs.ErrNotExist)
	})
	t.Run("wrong size", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "model.safetensors"), []byte("short"), 0o600))
		_, err := d.Derive(t.Context(), artifact.Entry{}, dir)
		require.ErrorIs(t, err, bert.ErrWeightsMismatch)
	})
	t.Run("same size other bytes", func(t *testing.T) {
		t.Parallel()
		dir, w := entryDir(t)
		w[100] ^= 1
		require.NoError(t, os.WriteFile(filepath.Join(dir, "model.safetensors"), w, 0o600))
		_, err := d.Derive(t.Context(), artifact.Entry{}, dir)
		require.ErrorIs(t, err, bert.ErrWeightsMismatch)
	})
	t.Run("canceled while hashing", func(t *testing.T) {
		t.Parallel()
		dir, _ := entryDir(t)
		_, err := d.Derive(canceled, artifact.Entry{}, dir)
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("weights is a directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, "model.safetensors"), 0o700))
		_, err := d.Derive(t.Context(), artifact.Entry{}, dir)
		require.Error(t, err)
	})
	t.Run("unwritable target", func(t *testing.T) {
		t.Parallel()
		dir, _ := entryDir(t)
		require.NoError(t, os.Mkdir(filepath.Join(dir, bert.FP32FileName), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, bert.FP32FileName, "x"), nil, 0o600))
		_, err := d.Derive(t.Context(), artifact.Entry{}, dir)
		require.Error(t, err)
		left, _ := filepath.Glob(filepath.Join(dir, "*.part"))
		assert.Empty(t, left)
	})
	t.Run("missing directory", func(t *testing.T) {
		t.Parallel()
		_, err := d.Derive(t.Context(), artifact.Entry{}, filepath.Join(t.TempDir(), "nope"))
		require.Error(t, err)
	})
}

func TestNewFP32DeriverRejectsGraph(t *testing.T) {
	t.Parallel()
	meta := func(kv ...string) []byte {
		var b pb
		for i := 0; i < len(kv); i += 2 {
			b = b.bytes(14, entry(kv[i], kv[i+1]))
		}
		return b
	}
	tests := []struct {
		name  string
		graph []byte
	}{
		{name: "not protobuf", graph: []byte{0x80}},
		{name: "no metadata", graph: meta()},
		{name: "path in file name", graph: meta(bert.MetaWeightsFile, "../w", bert.MetaWeightsSHA256, "ab",
			bert.MetaWeightsSize, "1")},
		{name: "dot file name", graph: meta(bert.MetaWeightsFile, "..", bert.MetaWeightsSHA256, "ab",
			bert.MetaWeightsSize, "1")},
		{name: "bad size", graph: meta(bert.MetaWeightsFile, "w", bert.MetaWeightsSHA256, "ab",
			bert.MetaWeightsSize, "-1")},
		{name: "size not a number", graph: meta(bert.MetaWeightsFile, "w", bert.MetaWeightsSHA256, "ab",
			bert.MetaWeightsSize, "x")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := bert.NewFP32Deriver(tc.graph)
			require.ErrorIs(t, err, bert.ErrInvalidModel)
		})
	}
}

// fakeConverter is a CommandRunner standing in for export_bert: it writes
// body to the --out path and prints `<sha256>  <path>`.
type fakeConverter struct {
	body   []byte
	stdout string // overrides the printed line when set
	stderr string
	err    error
	name   string
	args   []string
}

func (f *fakeConverter) Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	f.name, f.args = name, args
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("no deadline")
	}
	_, _ = io.WriteString(stderr, f.stderr)
	if f.err != nil {
		return f.err
	}
	var out string
	for i, a := range args {
		if a == "--out" {
			out = args[i+1]
		}
	}
	if f.body != nil {
		if err := os.WriteFile(out, f.body, 0o600); err != nil {
			return err
		}
	}
	line := f.stdout
	if line == "" {
		line = fmt.Sprintf("progress...\n%s  %s\n", sha(f.body), out)
	}
	_, _ = io.WriteString(stdout, line)
	return nil
}

func TestINT8Deriver(t *testing.T) {
	t.Parallel()
	int8Model := readFixture(t, bert.INT8FileName)
	fc := &fakeConverter{body: int8Model}
	d, err := bert.NewINT8Deriver(bert.UVConverter{ResearchDir: "/src/research"}, bert.WithCommandRunner(fc),
		bert.WithConvertTimeout(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, artifact.KindONNXINT8, d.Kind())
	dir := t.TempDir()
	got, err := d.Derive(t.Context(), artifact.Entry{Name: "bert"}, dir)
	require.NoError(t, err)
	out := filepath.Join(dir, bert.INT8FileName)
	assert.Equal(t, artifact.Derived{Kind: artifact.KindONNXINT8, Path: out, SHA256: sha(int8Model)}, got)
	assert.Equal(t, "uv", fc.name)
	assert.Equal(t, []string{
		"run", "--project", "/src/research", "--frozen", "--no-dev",
		"python", "-m", "triage_research.export_bert", "--checkpoint-dir", dir, "--out", out,
	}, fc.args)
}

func TestINT8DeriverErrors(t *testing.T) {
	t.Parallel()
	int8Model := readFixture(t, bert.INT8FileName)
	fp32 := readFixture(t, bert.FP32FileName)
	tests := []struct {
		name    string
		fc      *fakeConverter
		wantMsg string
	}{
		{
			name: "converter fails", fc: &fakeConverter{
				err:    errors.New("exit status 1"),
				stderr: "export_bert: graph expects model.safetensors sha256 aa, found bb\n",
			},
			wantMsg: "graph expects model.safetensors",
		},
		{name: "no output line", fc: &fakeConverter{body: int8Model, stdout: "\n"}, wantMsg: "is not"},
		{name: "short sha", fc: &fakeConverter{body: int8Model, stdout: "abc  /x\n"}, wantMsg: "is not"},
		{
			name: "non-hex sha", fc: &fakeConverter{body: int8Model, stdout: strings.Repeat("z", 64) + "  /x\n"},
			wantMsg: "bad sha256",
		},
		{
			name: "sha mismatch", fc: &fakeConverter{body: int8Model, stdout: strings.Repeat("a", 64) + "  /x\n"},
			wantMsg: "converter reported",
		},
		{
			name: "no file written", fc: &fakeConverter{stdout: strings.Repeat("a", 64) + "  /x\n"},
			wantMsg: "no such file",
		},
		{name: "not an int8 model", fc: &fakeConverter{body: fp32}, wantMsg: "not an INT8 model"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := bert.NewINT8Deriver(bert.UVConverter{ResearchDir: "r"}, bert.WithCommandRunner(tc.fc))
			require.NoError(t, err)
			_, err = d.Derive(t.Context(), artifact.Entry{}, t.TempDir())
			require.ErrorIs(t, err, bert.ErrConvert)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestNewINT8DeriverValidation(t *testing.T) {
	t.Parallel()
	conv := bert.UVConverter{ResearchDir: "r"}
	tests := []struct {
		name string
		conv bert.Converter
		opts []bert.INT8Option
	}{
		{name: "nil converter"},
		{name: "nil option", conv: conv, opts: []bert.INT8Option{nil}},
		{name: "nil runner", conv: conv, opts: []bert.INT8Option{bert.WithCommandRunner(nil)}},
		{name: "zero timeout", conv: conv, opts: []bert.INT8Option{bert.WithConvertTimeout(0)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := bert.NewINT8Deriver(tc.conv, tc.opts...)
			require.ErrorIs(t, err, bert.ErrInvalidConfig)
		})
	}
}

func TestConverters(t *testing.T) {
	t.Parallel()
	name, args := bert.UVConverter{UV: "/opt/uv", ResearchDir: "/r"}.Command("/m", "/m/o.onnx")
	assert.Equal(t, "/opt/uv", name)
	assert.Equal(t, "/r", args[2])

	name, args = bert.ImageConverter{Image: "ghcr.io/thnhdatle/triage-models:v1", User: "501:20"}.
		Command("/cache/bert/rev", "/cache/bert/rev/model.int8.onnx")
	assert.Equal(t, "docker", name)
	assert.Equal(t, []string{
		"run", "--rm", "--network", "none", "--user", "501:20",
		"-v", "/cache/bert/rev:/models", "--entrypoint", "python", "ghcr.io/thnhdatle/triage-models:v1",
		"-m", "triage_research.export_bert", "--checkpoint-dir", "/models", "--out", "/models/model.int8.onnx",
	}, args)

	name, args = bert.ImageConverter{Engine: "podman", Image: "img"}.Command("/d", "/d/o.onnx")
	assert.Equal(t, "podman", name)
	assert.NotContains(t, args, "--user")

	if runtime.GOOS != "windows" {
		assert.Equal(t, fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), bert.CurrentUser())
	}

	found := func(string) (string, error) { return "/bin/uv", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	assert.Equal(t, bert.UVConverter{UV: "/bin/uv", ResearchDir: "/r"}, bert.SelectConverter(found, "/r", "img"))
	image := bert.ImageResolver{Images: []string{"img", bert.DevImage}, User: bert.CurrentUser()}
	assert.Equal(t, image, bert.SelectConverter(missing, "/r", "img"))
	assert.Equal(t, image, bert.SelectConverter(found, "", "img"))
	dev := bert.ImageResolver{Images: []string{bert.DevImage}, User: bert.CurrentUser()}
	assert.Equal(t, dev, bert.SelectConverter(missing, "", bert.DevImage))
}

func TestExecRunner(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	var out, errOut strings.Builder
	err := bert.ExecRunner{}.Run(t.Context(), "sh", []string{"-c", "echo out; echo err >&2"}, &out, &errOut)
	require.NoError(t, err)
	assert.Equal(t, "out\n", out.String())
	assert.Equal(t, "err\n", errOut.String())

	err = bert.ExecRunner{}.Run(t.Context(), "sh", []string{"-c", "exit 3"}, io.Discard, io.Discard)
	var exitErr interface{ ExitCode() int }
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 3, exitErr.ExitCode())
}
