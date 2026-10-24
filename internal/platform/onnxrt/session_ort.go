// SPDX-License-Identifier: Apache-2.0

//go:build cgo && !noonnx

package onnxrt

import (
	"context"
	"fmt"
	"slices"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

type SessionConfig struct {
	IntraOpThreads int
	InterOpThreads int
	InputNames     []string
	OutputNames    []string
}

func (c SessionConfig) validate() error {
	if c.IntraOpThreads < 0 || c.InterOpThreads < 0 {
		return fmt.Errorf("%w: intra-op threads %d, inter-op threads %d (min 0)",
			ErrInvalidConfig, c.IntraOpThreads, c.InterOpThreads)
	}
	return nil
}

// Session is a loaded model.
type Session struct {
	rt      *Runtime
	inputs  []string
	outputs []string

	mu sync.RWMutex
	s  *ort.DynamicAdvancedSession
}

type ioInfoFunc func() ([]ort.InputOutputInfo, []ort.InputOutputInfo, error)

type createFunc func(in, out []string, so *ort.SessionOptions) (*ort.DynamicAdvancedSession, error)

// NewSession loads a model from its serialized ONNX bytes.
func (r *Runtime) NewSession(model []byte, cfg SessionConfig) (*Session, error) {
	return r.newSession(cfg, "model bytes",
		func() ([]ort.InputOutputInfo, []ort.InputOutputInfo, error) {
			return ort.GetInputOutputInfoWithONNXData(model)
		},
		func(in, out []string, so *ort.SessionOptions) (*ort.DynamicAdvancedSession, error) {
			return ort.NewDynamicAdvancedSessionWithONNXData(model, in, out, so)
		})
}

func (r *Runtime) NewSessionFromFile(path string, cfg SessionConfig) (*Session, error) {
	return r.newSession(cfg, path,
		func() ([]ort.InputOutputInfo, []ort.InputOutputInfo, error) {
			return ort.GetInputOutputInfo(path)
		},
		func(in, out []string, so *ort.SessionOptions) (*ort.DynamicAdvancedSession, error) {
			return ort.NewDynamicAdvancedSession(path, in, out, so)
		})
}

func (r *Runtime) newSession(cfg SessionConfig, src string, info ioInfoFunc, create createFunc) (*Session, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrClosed
	}
	in, out := slices.Clone(cfg.InputNames), slices.Clone(cfg.OutputNames)
	if len(in) == 0 || len(out) == 0 {
		mIn, mOut, err := info()
		if err != nil {
			return nil, fmt.Errorf("onnxrt: read inputs and outputs of %s: %w", src, err)
		}
		if len(in) == 0 {
			in = ioNames(mIn)
		}
		if len(out) == 0 {
			out = ioNames(mOut)
		}
	}
	so, err := sessionOptions(cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = so.Destroy() }()
	s, err := create(in, out, so)
	if err != nil {
		return nil, fmt.Errorf("onnxrt: create session from %s: %w", src, err)
	}
	sess := &Session{rt: r, inputs: in, outputs: out, s: s}
	r.sessions[sess] = struct{}{}
	return sess, nil
}

func ioNames(infos []ort.InputOutputInfo) []string {
	names := make([]string, len(infos))
	for i, x := range infos {
		names[i] = x.Name
	}
	return names
}

func sessionOptions(cfg SessionConfig) (*ort.SessionOptions, error) {
	so, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("onnxrt: session options: %w", err)
	}
	if cfg.IntraOpThreads > 0 {
		err = so.SetIntraOpNumThreads(cfg.IntraOpThreads)
	}
	if err == nil && cfg.InterOpThreads > 0 {
		err = so.SetInterOpNumThreads(cfg.InterOpThreads)
	}
	if err != nil {
		_ = so.Destroy()
		return nil, fmt.Errorf("onnxrt: session options: %w", err)
	}
	return so, nil
}

func (s *Session) InputNames() []string { return slices.Clone(s.inputs) }

func (s *Session) OutputNames() []string { return slices.Clone(s.outputs) }

// Run feeds the input tensors and returns the outputs in OutputNames order.
func (s *Session) Run(ctx context.Context, inputs ...Tensor) ([]Tensor, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("onnxrt: run: %w", err)
	}
	ordered, err := s.order(inputs)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.s == nil {
		return nil, ErrClosed
	}
	in, err := toValues(ordered)
	defer destroyAll(in)
	if err != nil {
		return nil, err
	}
	out := make([]ort.Value, len(s.outputs))
	defer destroyAll(out)
	if err := s.run(ctx, in, out); err != nil {
		return nil, err
	}
	return fromValues(s.outputs, out)
}

func (s *Session) run(ctx context.Context, in, out []ort.Value) error {
	ro, err := ort.NewRunOptions()
	if err != nil {
		return fmt.Errorf("onnxrt: run options: %w", err)
	}
	defer func() { _ = ro.Destroy() }()
	var (
		mu       sync.Mutex
		finished bool
	)
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		if !finished {
			_ = ro.Terminate()
		}
	})
	err = s.s.RunWithOptions(in, out, ro)
	mu.Lock()
	finished = true
	mu.Unlock()
	stop()
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("onnxrt: run: %w", cerr)
		}
		return fmt.Errorf("onnxrt: run: %w", err)
	}
	return nil
}

func (s *Session) order(inputs []Tensor) ([]Tensor, error) {
	if len(inputs) != len(s.inputs) {
		return nil, fmt.Errorf("%w: got %d tensors, session inputs are %v", ErrInvalidInput, len(inputs), s.inputs)
	}
	ordered := make([]Tensor, len(s.inputs))
	for _, t := range inputs {
		i := slices.Index(s.inputs, t.name)
		if i < 0 {
			return nil, fmt.Errorf("%w: unknown input %q, session inputs are %v", ErrInvalidInput, t.name, s.inputs)
		}
		if ordered[i].dtype != 0 {
			return nil, fmt.Errorf("%w: input %q given twice", ErrInvalidInput, t.name)
		}
		if err := t.validate(); err != nil {
			return nil, err
		}
		ordered[i] = t
	}
	return ordered, nil
}

func toValues(ts []Tensor) ([]ort.Value, error) {
	vals := make([]ort.Value, len(ts))
	for i, t := range ts {
		var (
			v   ort.Value
			err error
		)
		if t.dtype == Float32 {
			v, err = ort.NewTensor(ort.NewShape(t.shape...), t.f32)
		} else {
			v, err = ort.NewTensor(ort.NewShape(t.shape...), t.i64)
		}
		if err != nil {
			return vals, fmt.Errorf("%w: %q: %w", ErrInvalidInput, t.name, err)
		}
		vals[i] = v
	}
	return vals, nil
}

func fromValues(names []string, vals []ort.Value) ([]Tensor, error) {
	res := make([]Tensor, len(vals))
	for i, v := range vals {
		switch t := v.(type) {
		case *ort.Tensor[float32]:
			res[i] = Float32Tensor(names[i], t.GetShape(), slices.Clone(t.GetData()))
		case *ort.Tensor[int64]:
			res[i] = Int64Tensor(names[i], t.GetShape(), slices.Clone(t.GetData()))
		default:
			return nil, fmt.Errorf("%w: output %q (want float32 or int64)", ErrUnsupportedType, names[i])
		}
	}
	return res, nil
}

func destroyAll(vals []ort.Value) {
	for _, v := range vals {
		if v != nil {
			_ = v.Destroy()
		}
	}
}

func (s *Session) Close() error {
	s.mu.Lock()
	if s.s == nil {
		s.mu.Unlock()
		return nil
	}
	err := s.s.Destroy()
	s.s = nil
	s.mu.Unlock()
	s.rt.forget(s)
	if err != nil {
		return fmt.Errorf("onnxrt: close session: %w", err)
	}
	return nil
}
