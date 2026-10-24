// SPDX-License-Identifier: Apache-2.0

package bert

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thnhdatle/triage/internal/domain/artifact"
)

const (
	FP32FileName = "graph.onnx"
	INT8FileName = "model.int8.onnx"
)

const DefaultConvertTimeout = 15 * time.Minute

var ErrConvert = errors.New("bert: int8 conversion failed")

type FP32Deriver struct {
	graph []byte
	ref   weightsRef
}

var _ artifact.Deriver = (*FP32Deriver)(nil)

// NewFP32Deriver returns a deriver that installs the FP32 graph.
func NewFP32Deriver(graph []byte) (*FP32Deriver, error) {
	meta, err := ReadMetadata(bytes.NewReader(graph))
	if err != nil {
		return nil, err
	}
	ref, err := weightsRefOf(meta)
	if err != nil {
		return nil, err
	}
	return &FP32Deriver{graph: graph, ref: ref}, nil
}

func (*FP32Deriver) Kind() artifact.DerivedKind { return artifact.KindONNXFP32 }

func (d *FP32Deriver) Derive(ctx context.Context, e artifact.Entry, dir string) (artifact.Derived, error) {
	for _, f := range e.Files {
		if f.Path == d.ref.file && (f.SHA256 != d.ref.sha256 || f.Size != d.ref.size) {
			return artifact.Derived{}, fmt.Errorf("%w: graph built for %s sha256 %s, manifest pins %s",
				ErrWeightsMismatch, d.ref.file, d.ref.sha256, f.SHA256)
		}
	}
	if err := d.ref.verify(ctx, dir); err != nil {
		return artifact.Derived{}, err
	}
	out := filepath.Join(dir, FP32FileName)
	if err := writeAtomic(out, d.graph); err != nil {
		return artifact.Derived{}, err
	}
	sum := sha256.Sum256(d.graph)
	return artifact.Derived{Kind: artifact.KindONNXFP32, Path: out, SHA256: hex.EncodeToString(sum[:])}, nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.part")
	if err != nil {
		return fmt.Errorf("bert: write %s: %w", path, err)
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
		return fmt.Errorf("bert: write %s: %w", path, werr)
	}
	return nil
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: argv built by Converter, no shell.
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bert: run %s: %w", name, err)
	}
	return nil
}

type Converter interface {
	Command(dir, out string) (name string, args []string)
}

const exportModule = "triage_research.export_bert"

type UVConverter struct {
	UV          string
	ResearchDir string
}

func (c UVConverter) Command(dir, out string) (string, []string) {
	uv := c.UV
	if uv == "" {
		uv = "uv"
	}
	return uv, []string{
		"run", "--project", c.ResearchDir, "--frozen", "--no-dev",
		"python", "-m", exportModule, "--checkpoint-dir", dir, "--out", out,
	}
}

type ImageConverter struct {
	Engine string
	Image  string
	User   string
}

const containerDir = "/models"

func (c ImageConverter) Command(dir, out string) (string, []string) {
	engine := c.Engine
	if engine == "" {
		engine = "docker"
	}
	args := []string{"run", "--rm", "--network", "none"}
	if c.User != "" {
		args = append(args, "--user", c.User)
	}
	args = append(args,
		"-v", dir+":"+containerDir, "--entrypoint", "python", c.Image,
		"-m", exportModule, "--checkpoint-dir", containerDir,
		"--out", containerDir+"/"+filepath.Base(out))
	return engine, args
}

func CurrentUser() string {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return ""
	}
	return strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
}

const DevImage = "thnhdatle/triage-models:dev"

type ImageResolver struct {
	Engine string
	Images []string
	User   string
}

func (r ImageResolver) Command(dir, out string) (string, []string) {
	img := ""
	if len(r.Images) > 0 {
		img = r.Images[0]
	}
	return ImageConverter{Engine: r.Engine, Image: img, User: r.User}.Command(dir, out)
}

func (r ImageResolver) Resolve(ctx context.Context, run CommandRunner) (Converter, error) {
	engine := r.Engine
	if engine == "" {
		engine = "docker"
	}
	var tried []string
	for i, img := range r.Images {
		var out bytes.Buffer
		if err := run.Run(ctx, engine, []string{"image", "inspect", img}, &out, &out); err == nil {
			return ImageConverter{Engine: r.Engine, Image: img, User: r.User}, nil
		}
		if i < len(r.Images)-1 {
			if err := run.Run(ctx, engine, []string{"pull", img}, &out, &out); err == nil {
				return ImageConverter{Engine: r.Engine, Image: img, User: r.User}, nil
			}
		}
		tried = append(tried, img)
	}
	return nil, fmt.Errorf("%w: no converter image available (tried %s); pull the released image or run `make docker` "+
		"to build %s, or use uv with a research/ checkout", ErrConvert, strings.Join(tried, ", "), DevImage)
}

type resolver interface {
	Resolve(ctx context.Context, run CommandRunner) (Converter, error)
}

func SelectConverter(lookPath func(string) (string, error), researchDir, image string) Converter {
	if researchDir != "" {
		if uv, err := lookPath("uv"); err == nil {
			return UVConverter{UV: uv, ResearchDir: researchDir}
		}
	}
	images := []string{image}
	if image != DevImage {
		images = append(images, DevImage)
	}
	return ImageResolver{Images: images, User: CurrentUser()}
}

type INT8Deriver struct {
	conv    Converter
	run     CommandRunner
	timeout time.Duration
}

var _ artifact.Deriver = (*INT8Deriver)(nil)

type INT8Option func(*INT8Deriver)

func WithCommandRunner(r CommandRunner) INT8Option { return func(d *INT8Deriver) { d.run = r } }

func WithConvertTimeout(t time.Duration) INT8Option { return func(d *INT8Deriver) { d.timeout = t } }

// NewINT8Deriver returns a deriver that converts the model to INT8.
func NewINT8Deriver(conv Converter, opts ...INT8Option) (*INT8Deriver, error) {
	d := &INT8Deriver{conv: conv, run: ExecRunner{}, timeout: DefaultConvertTimeout}
	for _, o := range opts {
		if o == nil {
			return nil, fmt.Errorf("%w: nil option", ErrInvalidConfig)
		}
		o(d)
	}
	if d.conv == nil || d.run == nil || d.timeout <= 0 {
		return nil, fmt.Errorf("%w: converter, command runner and a positive timeout are required", ErrInvalidConfig)
	}
	return d, nil
}

func (*INT8Deriver) Kind() artifact.DerivedKind { return artifact.KindONNXINT8 }

func (d *INT8Deriver) Derive(ctx context.Context, _ artifact.Entry, dir string) (artifact.Derived, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return artifact.Derived{}, fmt.Errorf("bert: %w", err)
	}
	out := filepath.Join(abs, INT8FileName)
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	conv := d.conv
	if r, ok := conv.(resolver); ok {
		var err error
		if conv, err = r.Resolve(ctx, d.run); err != nil {
			return artifact.Derived{}, fmt.Errorf("bert: %w", err)
		}
	}
	name, args := conv.Command(abs, out)
	var stdout, stderr bytes.Buffer
	if err := d.run.Run(ctx, name, args, &stdout, &stderr); err != nil {
		return artifact.Derived{}, fmt.Errorf("%w: %w: %s", ErrConvert, err, tail(stderr.String()))
	}
	reported, err := reportedSHA(stdout.String())
	if err != nil {
		return artifact.Derived{}, err
	}
	sum, err := fileSHA256(ctx, out)
	if err != nil {
		return artifact.Derived{}, fmt.Errorf("%w: %w", ErrConvert, err)
	}
	if sum != reported {
		return artifact.Derived{}, fmt.Errorf("%w: %s has sha256 %s, converter reported %s", ErrConvert, out, sum, reported)
	}
	if err := checkModel(ctx, out, VariantINT8); err != nil {
		return artifact.Derived{}, fmt.Errorf("%w: %w", ErrConvert, err)
	}
	return artifact.Derived{Kind: artifact.KindONNXINT8, Path: out, SHA256: sum}, nil
}

func reportedSHA(stdout string) (string, error) {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 2 || len(fields[0]) != sha256.Size*2 {
		return "", fmt.Errorf("%w: last stdout line %q is not `<sha256>  <path>`", ErrConvert, tail(stdout))
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", fmt.Errorf("%w: bad sha256 %q", ErrConvert, fields[0])
	}
	return strings.ToLower(fields[0]), nil
}

func tail(s string) string {
	const limit = 2048
	s = strings.TrimSpace(s)
	if len(s) > limit {
		return "..." + s[len(s)-limit:]
	}
	return s
}
