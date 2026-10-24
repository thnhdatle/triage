// SPDX-License-Identifier: Apache-2.0

package piionnx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/platform/hftok"
	"github.com/thnhdatle/triage/internal/platform/onnxrt"
)

const ID = "ner"

const (
	DefaultMinScore       = 0.8
	DefaultMaxTokens      = 2048
	DefaultTimeout        = 200 * time.Millisecond
	DefaultWindowTokens   = 512
	DefaultWindowOverlap  = 128
	DefaultMaxConcurrency = 2
	DefaultQueueSize      = 64
	DefaultIntraOpThreads = 2
	MaxWindowTokens       = 512
)

const (
	InputIDs           = "input_ids"
	InputAttentionMask = "attention_mask"
	InputTokenTypeIDs  = "token_type_ids" //nolint:gosec // G101: a tensor name, not a credential
	OutputLogits       = "logits"
)

const (
	reasonTooLong  = "too_long"
	reasonTimeout  = "timeout"
	reasonRejected = "rejected"
	reasonError    = "error"
)

var errTooLong = errors.New("piionnx: text exceeds max_tokens")

type Files struct {
	Model     string
	Tokenizer string
	Config    string
}

type config struct {
	minScore       float64
	maxTokens      int
	timeout        time.Duration
	window         int
	overlap        int
	pool           onnxrt.PoolConfig
	intraOpThreads int
	log            *slog.Logger
}

func (c config) validate() error {
	var errs []error
	if c.minScore < 0 || c.minScore > 1 {
		errs = append(errs, fmt.Errorf("min_score %v outside [0,1]", c.minScore))
	}
	if c.maxTokens < 1 {
		errs = append(errs, fmt.Errorf("max_tokens %d < 1", c.maxTokens))
	}
	if c.timeout <= 0 {
		errs = append(errs, fmt.Errorf("timeout %v <= 0", c.timeout))
	}
	if c.window < 3 || c.window > MaxWindowTokens || c.overlap < 0 || c.overlap >= c.window-2 {
		errs = append(errs, fmt.Errorf("window %d (3..%d) with overlap %d (0..window-3)",
			c.window, MaxWindowTokens, c.overlap))
	}
	if c.intraOpThreads < 0 {
		errs = append(errs, fmt.Errorf("intra-op threads %d < 0", c.intraOpThreads))
	}
	if c.log == nil {
		errs = append(errs, errors.New("nil logger"))
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("piionnx: invalid config: %w", err)
	}
	return nil
}

type Option func(*config)

func WithMinScore(s float64) Option { return func(c *config) { c.minScore = s } }

func WithMaxTokens(n int) Option { return func(c *config) { c.maxTokens = n } }

func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

func WithWindow(tokens, overlap int) Option {
	return func(c *config) { c.window, c.overlap = tokens, overlap }
}

func WithPool(p onnxrt.PoolConfig) Option { return func(c *config) { c.pool = p } }

func WithIntraOpThreads(n int) Option { return func(c *config) { c.intraOpThreads = n } }

func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// Detector is the NER PII detector.
type Detector struct {
	cfg      config
	tok      *hftok.Tokenizer
	al       *aligner
	labels   []label
	cls, sep int
	sess     *onnxrt.Session
	pool     *onnxrt.Pool
}

var _ privacy.Detector = (*Detector)(nil)

// New loads the model and tokenizer and returns a Detector.
func New(rt *onnxrt.Runtime, files Files, opts ...Option) (*Detector, error) {
	cfg := config{
		minScore:       DefaultMinScore,
		maxTokens:      DefaultMaxTokens,
		timeout:        DefaultTimeout,
		window:         DefaultWindowTokens,
		overlap:        DefaultWindowOverlap,
		pool:           onnxrt.PoolConfig{MaxConcurrency: DefaultMaxConcurrency, QueueSize: DefaultQueueSize},
		intraOpThreads: DefaultIntraOpThreads,
		log:            slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(&cfg)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if rt == nil {
		return nil, errors.New("piionnx: nil ONNX runtime")
	}
	d := &Detector{cfg: cfg}
	if err := d.loadText(files); err != nil {
		return nil, err
	}
	sess, err := rt.NewSessionFromFile(files.Model, onnxrt.SessionConfig{
		IntraOpThreads: cfg.intraOpThreads,
		InputNames:     []string{InputIDs, InputAttentionMask, InputTokenTypeIDs},
		OutputNames:    []string{OutputLogits},
	})
	if err != nil {
		return nil, fmt.Errorf("piionnx: load model %s: %w", files.Model, err)
	}
	pool, err := onnxrt.NewPool(sess, cfg.pool)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("piionnx: %w", err), sess.Close())
	}
	d.sess, d.pool = sess, pool
	return d, nil
}

func (d *Detector) loadText(files Files) error {
	tok, err := hftok.Load(files.Tokenizer)
	if err != nil {
		return fmt.Errorf("piionnx: %w", err)
	}
	vocab, err := readVocabulary(files.Tokenizer)
	if err != nil {
		return err
	}
	al, err := newAligner(tok, vocab)
	if err != nil {
		return fmt.Errorf("%w (%s)", err, files.Tokenizer)
	}
	labels, err := readLabels(files.Config)
	if err != nil {
		return err
	}
	frame, err := tok.Encode("", true)
	if err != nil || len(frame) != 2 {
		return fmt.Errorf("%w: %s: post-processor must add exactly [CLS] and [SEP]", ErrInvalidFile, files.Tokenizer)
	}
	d.tok, d.al, d.labels = tok, al, labels
	d.cls, d.sep = frame[0], frame[1]
	return nil
}

func (*Detector) ID() string { return ID }

// Detect returns the PII findings in text.
func (d *Detector) Detect(ctx context.Context, text string) ([]privacy.Finding, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("piionnx: detect: %w", err)
	}
	fs, err := d.detect(ctx, text)
	if err == nil {
		return fs, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("piionnx: detect: %w", ctxErr)
	}
	d.cfg.log.LogAttrs(ctx, slog.LevelWarn, "ner unavailable",
		slog.String("component", "piionnx"),
		slog.String("status", reason(err)),
		slog.Int("bytes", len(text)),
		slog.String("error", err.Error()))
	return []privacy.Finding{{
		DetectorID: ID,
		Type:       privacy.TypeNERUnavailable,
		End:        len(text),
		Confidence: 1,
	}}, nil
}

func reason(err error) string {
	var rej interface{ Rejected() bool }
	switch {
	case errors.Is(err, errTooLong):
		return reasonTooLong
	case errors.Is(err, context.DeadlineExceeded):
		return reasonTimeout
	case errors.As(err, &rej) && rej.Rejected():
		return reasonRejected
	}
	return reasonError
}

func (d *Detector) detect(ctx context.Context, text string) ([]privacy.Finding, error) {
	ctx, cancel := context.WithTimeout(ctx, d.cfg.timeout)
	defer cancel()
	ids, err := d.tok.Encode(text, false)
	if err != nil {
		return nil, fmt.Errorf("piionnx: tokenize: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > d.cfg.maxTokens {
		return nil, fmt.Errorf("%w (%d > %d tokens)", errTooLong, len(ids), d.cfg.maxTokens)
	}
	spans, err := d.al.align(text, ids)
	if err != nil {
		return nil, err
	}
	ws := windows(len(ids), d.cfg.window-2, d.cfg.overlap)
	probs, err := d.infer(ctx, newBatch(ids, ws, d.cls, d.sep), ws)
	if err != nil {
		return nil, err
	}
	return entities(text, words(spans, probs), d.labels, d.cfg.minScore), nil
}

func (d *Detector) infer(ctx context.Context, b batch, ws []window) ([][]float64, error) {
	shape := []int64{int64(b.rows), int64(b.cols)}
	out, err := d.pool.Run(ctx,
		onnxrt.Int64Tensor(InputIDs, shape, b.ids),
		onnxrt.Int64Tensor(InputAttentionMask, shape, b.mask),
		onnxrt.Int64Tensor(InputTokenTypeIDs, shape, make([]int64, len(b.ids))),
	)
	if err != nil {
		return nil, fmt.Errorf("piionnx: inference: %w", err)
	}
	logits, err := out[0].Float32Data()
	if err != nil {
		return nil, fmt.Errorf("piionnx: logits: %w", err)
	}
	return tokenProbs(logits, out[0].Shape(), b, ws, len(d.labels))
}

func (d *Detector) Probe(ctx context.Context) error {
	ws := []window{{start: 0, n: 1}}
	_, err := d.infer(ctx, newBatch([]int{d.sep}, ws, d.cls, d.sep), ws)
	return err
}

func (d *Detector) Close(ctx context.Context) error {
	err := d.pool.Close(ctx)
	if err != nil {
		return fmt.Errorf("piionnx: %w", err)
	}
	if err := d.sess.Close(); err != nil {
		return fmt.Errorf("piionnx: close session: %w", err)
	}
	return nil
}
