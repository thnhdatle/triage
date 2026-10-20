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

// Derived artifact file names, written into the entry directory next to
// model.safetensors (the FP32 graph must sit there: ORT resolves its
// external data relative to the graph file).
const (
	FP32FileName = "graph.onnx"
	INT8FileName = "model.int8.onnx"
)

// DefaultConvertTimeout bounds one INT8 conversion (about 1 minute on the
// reference Mac).
const DefaultConvertTimeout = 15 * time.Minute

// ErrConvert is returned when the INT8 converter fails or its output does
// not check out.
var ErrConvert = errors.New("bert: int8 conversion failed")

// FP32Deriver installs the committed weight-free graph
// (models/bert/graph.onnx) next to the downloaded model.safetensors.
type FP32Deriver struct {
	graph []byte
	ref   weightsRef
}

var _ artifact.Deriver = (*FP32Deriver)(nil)

// NewFP32Deriver returns a deriver for the serialised weight-free graph,
// which must carry the triage.weights.* metadata.
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

// Kind implements artifact.Deriver.
func (*FP32Deriver) Kind() artifact.DerivedKind { return artifact.KindONNXFP32 }

// Derive implements artifact.Deriver: it checks that the graph was built for
// the entry's weights (manifest and file), then writes dir/graph.onnx
// atomically. No Python is involved.
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

// CommandRunner runs an external command; ExecRunner is the real one and
// tests inject a fake.
type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error
}

// ExecRunner runs commands with os/exec; the process is killed when ctx ends.
type ExecRunner struct{}

// Run implements CommandRunner.
func (ExecRunner) Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: argv built by Converter, no shell.
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bert: run %s: %w", name, err)
	}
	return nil
}

// Converter builds the argv that runs research/export_bert.py
// (`export_bert --checkpoint-dir <dir> --out <file>`, research/README.md).
type Converter interface {
	Command(dir, out string) (name string, args []string)
}

// exportModule is the documented converter entry point.
const exportModule = "triage_research.export_bert"

// UVConverter runs the pinned research/ project with uv (locked
// dependencies, no dev group), from a checkout.
type UVConverter struct {
	// UV is the uv executable; empty means "uv" on PATH.
	UV string
	// ResearchDir is the research/ directory holding pyproject.toml and uv.lock.
	ResearchDir string
}

// Command implements Converter.
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

// ImageConverter runs the weight-free converter image with the entry
// directory mounted at /models and no network.
type ImageConverter struct {
	// Engine is the container CLI; empty means "docker".
	Engine string
	// Image is RAYLD_TRIAGE_MODELS_IMAGE or its default.
	Image string
	// User is passed as --user (e.g. "501:20") so the output is owned by the
	// caller; empty omits it.
	User string
}

// containerDir is where ImageConverter mounts the entry directory.
const containerDir = "/models"

// Command implements Converter. out must be inside dir.
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

// CurrentUser returns "uid:gid" of the process for ImageConverter.User, or
// "" where ids do not exist (Windows).
func CurrentUser() string {
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return ""
	}
	return strconv.Itoa(uid) + ":" + strconv.Itoa(gid)
}

// DevImage is the converter image built locally by `make docker`, the
// fallback when the released image is neither present nor pullable.
const DevImage = "thnhdatle/triage-models:dev"

// ImageResolver is a Converter that first picks a usable image: the released
// one (present locally or pullable), else DevImage. Command, used when Resolve
// was skipped, uses the first image unchecked.
type ImageResolver struct {
	// Engine is the container CLI; empty means "docker".
	Engine string
	// Images are tried in order; the last one is never pulled (it is local).
	Images []string
	// User is passed as --user, see ImageConverter.
	User string
}

// Command implements Converter for callers that skip Resolve.
func (r ImageResolver) Command(dir, out string) (string, []string) {
	img := ""
	if len(r.Images) > 0 {
		img = r.Images[0]
	}
	return ImageConverter{Engine: r.Engine, Image: img, User: r.User}.Command(dir, out)
}

// Resolve returns the ImageConverter for the first image that `<engine> image
// inspect` finds or `<engine> pull` fetches (the last image is only inspected).
// When none works the error names every image tried.
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

// resolver is implemented by converters that must pick their runtime first.
type resolver interface {
	Resolve(ctx context.Context, run CommandRunner) (Converter, error)
}

// SelectConverter returns the uv converter when uv is on PATH and a research
// directory is known (the checkout path), else the converter image with the
// local DevImage as fallback (a released binary has no research/).
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

// INT8Deriver builds model.int8.onnx with the research converter.
type INT8Deriver struct {
	conv    Converter
	run     CommandRunner
	timeout time.Duration
}

var _ artifact.Deriver = (*INT8Deriver)(nil)

// INT8Option configures an INT8Deriver.
type INT8Option func(*INT8Deriver)

// WithCommandRunner replaces ExecRunner.
func WithCommandRunner(r CommandRunner) INT8Option { return func(d *INT8Deriver) { d.run = r } }

// WithConvertTimeout replaces DefaultConvertTimeout.
func WithConvertTimeout(t time.Duration) INT8Option { return func(d *INT8Deriver) { d.timeout = t } }

// NewINT8Deriver returns a deriver running conv.
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

// Kind implements artifact.Deriver.
func (*INT8Deriver) Kind() artifact.DerivedKind { return artifact.KindONNXINT8 }

// Derive implements artifact.Deriver. The converter writes dir/model.int8.onnx
// atomically and prints `<sha256>  <path>` as its last stdout line; the file
// is re-hashed here and must match, and must be tagged triage.variant=int8.
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

// reportedSHA parses the converter's last stdout line `<sha256>  <path>`.
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

// tail keeps the end of converter output for error messages.
func tail(s string) string {
	const limit = 2048
	s = strings.TrimSpace(s)
	if len(s) > limit {
		return "..." + s[len(s)-limit:]
	}
	return s
}
