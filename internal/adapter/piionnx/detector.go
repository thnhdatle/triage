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

// ID is the detector id reported in Finding.DetectorID.
const ID = "ner"

// Defaults (privacy.ner and the BERT-base input limit).
const (
	DefaultMinScore       = 0.8
	DefaultMaxTokens      = 2048
	DefaultTimeout        = 200 * time.Millisecond
	DefaultWindowTokens   = 512
	DefaultWindowOverlap  = 128
	DefaultMaxConcurrency = 2
	DefaultQueueSize      = 64
	DefaultIntraOpThreads = 2
	// MaxWindowTokens is BERT-base's position-embedding limit.
	MaxWindowTokens = 512
)

// ONNX input and output names of a Hugging Face token-classification export.
const (
	InputIDs           = "input_ids"
	InputAttentionMask = "attention_mask"
	InputTokenTypeIDs  = "token_type_ids" //nolint:gosec // G101: a tensor name, not a credential
	OutputLogits       = "logits"
)

// Reasons logged (key "status") when a text is reported as ner_unavailable.
const (
	reasonTooLong  = "too_long"
	reasonTimeout  = "timeout"
	reasonRejected = "rejected"
	reasonError    = "error"
)

// errTooLong reports a text above max_tokens; it carries only counts.
var errTooLong = errors.New("piionnx: text exceeds max_tokens")

// Files are the pinned dslim/bert-base-NER artifacts (models/manifest.yaml):
// onnx/model.onnx, onnx/tokenizer.json and onnx/config.json.
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

// Option configures New.
type Option func(*config)

// WithMinScore sets privacy.ner.min_score: entities whose mean word score is
// below it are dropped. Default 0.8.
func WithMinScore(s float64) Option { return func(c *config) { c.minScore = s } }

// WithMaxTokens sets privacy.ner.max_tokens: texts with more WordPiece tokens
// (without [CLS]/[SEP]) are not scanned and reported as ner_unavailable.
// Default 2048.
func WithMaxTokens(n int) Option { return func(c *config) { c.maxTokens = n } }

// WithTimeout sets privacy.ner.timeout for one Detect call, queueing
// included. Default 200ms.
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// WithWindow sets the window length in tokens including [CLS] and [SEP]
// (3..512, default 512) and the overlap between consecutive windows in
// content tokens (default 128).
func WithWindow(tokens, overlap int) Option {
	return func(c *config) { c.window, c.overlap = tokens, overlap }
}

// WithPool bounds concurrent inferences and waiting callers. Defaults: 2 runs,
// 64 waiting; a full queue reports ner_unavailable at once.
func WithPool(p onnxrt.PoolConfig) Option { return func(c *config) { c.pool = p } }

// WithIntraOpThreads sets ORT intra-op threads per run (0 = ORT default).
// Default 2.
func WithIntraOpThreads(n int) Option { return func(c *config) { c.intraOpThreads = n } }

// WithLogger sets the logger for ner_unavailable events. Text is never
// logged, only its byte length and the reason. Default: discard.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// Detector is the English NER PII detector over dslim/bert-base-NER. It is
// safe for concurrent use; Close releases the ONNX session.
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

// New loads the tokenizer, the labels and the ONNX model, and builds the
// bounded session pool. It starts no goroutines. A missing file fails with an
// error matching os.ErrNotExist that names the path.
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

// loadText loads the tokenizer, its vocabulary and the labels.
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

// ID returns "ner".
func (*Detector) ID() string { return ID }

// Detect returns PER, LOC and ORG entities of text with a score of at least
// min_score as person, location and organization findings, ordered by start
// byte offset. A text that cannot be scanned (more than max_tokens tokens,
// timeout, full queue, inference error) yields a single ner_unavailable
// finding without value instead of an error (fail-closed). Only the
// end of ctx itself is returned as an error.
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

// infer runs all windows as one batch and returns per-token probabilities.
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

// Probe runs one inference on an empty sequence and checks that the model's
// logits match the labels of config.json. Call it at startup so a wrong
// model fails there and not as ner_unavailable on every request.
func (d *Detector) Probe(ctx context.Context) error {
	ws := []window{{start: 0, n: 1}}
	_, err := d.infer(ctx, newBatch([]int{d.sep}, ws, d.cls, d.sep), ws)
	return err
}

// Close stops the pool, waiting for in-flight runs until ctx ends, and
// releases the session. Detect afterwards reports ner_unavailable.
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
