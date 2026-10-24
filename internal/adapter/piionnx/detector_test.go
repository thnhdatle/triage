// SPDX-License-Identifier: Apache-2.0

//go:build cgo && !noonnx

package piionnx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/domain/privacy"
	"github.com/thnhdatle/triage/internal/platform/onnxrt"
)

const highScore = 0.98055

var testWords = []struct {
	word  string
	label int
	logit float32
}{
	{"John", lblBPER, 6},
	{"Smith", lblIPER, 6},
	{"Schwarzenegger", lblBPER, 6},
	{"Berlin", lblBLOC, 6},
	{"Google", lblBORG, 6},
	{"Inc", lblIORG, 6},
	{"Paris", lblBLOC, 1},
	{"English", lblBMISC, 6},
}

type fixture struct {
	rt    *onnxrt.Runtime
	files Files
}

func newRuntime(t *testing.T) *onnxrt.Runtime {
	t.Helper()
	rt, err := onnxrt.Open()
	require.NoError(t, err, "ONNX Runtime >= %s is required: run make bootstrap or set %s",
		onnxrt.MinVersion, onnxrt.EnvLibraryPath)
	t.Cleanup(func() { assert.NoError(t, rt.Close()) })
	return rt
}

func newFixture(t *testing.T, labels int, inputs []string) fixture {
	t.Helper()
	tok, al := testTokenizer(t)
	rows := map[int][]float32{}
	for _, w := range testWords {
		ids, err := tok.Encode(w.word, false)
		require.NoError(t, err)
		for _, id := range ids {
			row := make([]float32, labels)
			row[w.label%labels] = w.logit
			rows[id] = row
		}
	}
	dir := t.TempDir()
	model := filepath.Join(dir, "model.onnx")
	require.NoError(t, os.WriteFile(model, tokenModel(len(al.tokens), labels, rows, inputs), 0o600))
	config := filepath.Join(dir, "config.json")
	require.NoError(t, os.WriteFile(config, []byte(dslimLabels), 0o600))
	return fixture{rt: newRuntime(t), files: Files{Model: model, Tokenizer: tokenizerPath(t), Config: config}}
}

func newDetector(t *testing.T, f fixture, opts ...Option) *Detector {
	t.Helper()
	d, err := New(f.rt, f.files, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, d.Close(context.Background())) })
	return d
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type want struct {
	typ, value string
	score      float64
}

func TestDetector_Detect_Entities(t *testing.T) {
	t.Parallel()
	d := newDetector(t, newFixture(t, numLabels, nil))
	tests := []struct {
		name string
		text string
		want []want
	}{
		{name: "empty", text: ""},
		{name: "only spaces", text: " \n\t "},
		{name: "no entity", text: "What is the capital of Australia?"},
		{
			name: "person and location", text: "John Smith lives in Berlin.",
			want: []want{{TypePerson, "John Smith", highScore}, {TypeLocation, "Berlin", highScore}},
		},
		{
			name: "organization B then I", text: "She joined Google Inc last year.",
			want: []want{{TypeOrganization, "Google Inc", highScore}},
		},
		{
			name: "subword entity averaged", text: "Ask Schwarzenegger.",
			want: []want{{TypePerson, "Schwarzenegger", highScore}},
		},
		{
			name: "stray I starts an entity", text: "Smith called.",
			want: []want{{TypePerson, "Smith", highScore}},
		},
		{
			name: "B after B splits", text: "John John",
			want: []want{{TypePerson, "John", highScore}, {TypePerson, "John", highScore}},
		},
		{name: "low score dropped", text: "Visit Paris soon."},
		{name: "MISC ignored", text: "They speak English."},
		{
			name: "multibyte offsets", text: "Héllo — «John» from Berlin 😀",
			want: []want{{TypePerson, "John", highScore}, {TypeLocation, "Berlin", highScore}},
		},
		{
			name: "zero width inside a name keeps exact bytes", text: "Jo\u200bhn Smith",
			want: []want{{TypePerson, "Jo\u200bhn Smith", highScore}},
		},
		{
			name: "invalid utf8 is spanned", text: "Jo\xffhn met Berlin",
			want: []want{{TypePerson, "Jo\xffhn", highScore}, {TypeLocation, "Berlin", highScore}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := d.Detect(context.Background(), tc.text)
			require.NoError(t, err)
			require.Len(t, got, len(tc.want))
			for i, w := range tc.want {
				f := got[i]
				assert.Equal(t, ID, f.DetectorID)
				assert.Equal(t, w.typ, f.Type)
				assert.Equal(t, w.value, f.Value)
				assert.Equal(t, f.Value, tc.text[f.Start:f.End], "value is the text at the byte span")
				assert.InDelta(t, w.score, f.Confidence, 1e-4)
				if i > 0 {
					assert.LessOrEqual(t, got[i-1].End, f.Start, "ordered, non-overlapping")
				}
			}
		})
	}
}

func TestDetector_Detect_MinScore(t *testing.T) {
	t.Parallel()
	d := newDetector(t, newFixture(t, numLabels, nil), WithMinScore(0.2))
	got, err := d.Detect(context.Background(), "Visit Paris soon.")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "Paris", got[0].Value)
	assert.InDelta(t, 0.25365, got[0].Confidence, 1e-4, "softmax(1, 0 x 8)")
}

func TestDetector_Detect_SlidingWindowsMatchSingleWindow(t *testing.T) {
	t.Parallel()
	f := newFixture(t, numLabels, nil)
	whole := newDetector(t, f)
	small := newDetector(t, f, WithWindow(16, 4))
	text := strings.Repeat("John Smith visited Berlin and Google Inc. ", 30)
	want, err := whole.Detect(context.Background(), text)
	require.NoError(t, err)
	got, err := small.Detect(context.Background(), text)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	counts := map[string]int{}
	for _, f := range got {
		counts[f.Type]++
		assert.Equal(t, f.Value, text[f.Start:f.End])
	}
	assert.Equal(t, map[string]int{TypePerson: 30, TypeLocation: 30, TypeOrganization: 30}, counts)
	again, err := small.Detect(context.Background(), text)
	require.NoError(t, err)
	assert.Equal(t, got, again, "deterministic")
}

func assertUnavailable(t *testing.T, got []privacy.Finding, text string) {
	t.Helper()
	require.Len(t, got, 1)
	assert.Equal(t, privacy.Finding{
		DetectorID: ID, Type: privacy.TypeNERUnavailable, End: len(text), Confidence: 1,
	}, got[0])
}

func TestDetector_Detect_Unavailable(t *testing.T) {
	t.Parallel()
	f := newFixture(t, numLabels, nil)
	const text = "John Smith lives in Berlin."
	tests := []struct {
		name   string
		opts   []Option
		hold   bool
		close  bool
		status string
	}{
		{name: "too many tokens", opts: []Option{WithMaxTokens(3)}, status: reasonTooLong},
		{
			name: "queue full", hold: true, status: reasonRejected,
			opts: []Option{WithPool(onnxrt.PoolConfig{MaxConcurrency: 1})},
		},
		{
			name: "timeout while queued", hold: true, status: reasonTimeout,
			opts: []Option{WithPool(onnxrt.PoolConfig{MaxConcurrency: 1, QueueSize: 1}), WithTimeout(20 * time.Millisecond)},
		},
		{name: "closed", close: true, status: reasonRejected},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs syncBuffer
			opts := append([]Option{WithLogger(slog.New(slog.NewJSONHandler(&logs, nil)))}, tc.opts...)
			d := newDetector(t, f, opts...)
			if tc.hold {
				l, err := d.pool.Acquire(context.Background())
				require.NoError(t, err)
				defer l.Release()
			}
			if tc.close {
				require.NoError(t, d.Close(context.Background()))
			}
			got, err := d.Detect(context.Background(), text)
			require.NoError(t, err)
			assertUnavailable(t, got, text)
			out := logs.String()
			assert.Contains(t, out, `"status":"`+tc.status+`"`)
			assert.Contains(t, out, `"bytes":27`)
			assert.NotContains(t, out, "John", "text is never logged")
			assert.NotContains(t, out, "Berlin", "text is never logged")
		})
	}
}

func TestDetector_Detect_CallerContextEnds(t *testing.T) {
	t.Parallel()
	d := newDetector(t, newFixture(t, numLabels, nil),
		WithPool(onnxrt.PoolConfig{MaxConcurrency: 1, QueueSize: 1}), WithTimeout(time.Minute))
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Detect(canceled, "John")
	require.ErrorIs(t, err, context.Canceled)

	l, err := d.pool.Acquire(context.Background())
	require.NoError(t, err)
	defer l.Release()
	ctx, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	_, err = d.Detect(ctx, "John")
	require.ErrorIs(t, err, context.DeadlineExceeded, "the caller's own deadline is an error, not ner_unavailable")
}

func TestDetector_Detect_Concurrent(t *testing.T) {
	t.Parallel()
	d := newDetector(t, newFixture(t, numLabels, nil), WithPool(onnxrt.PoolConfig{MaxConcurrency: 2, QueueSize: 64}),
		WithTimeout(10*time.Second))
	texts := []string{"John Smith lives in Berlin.", "Google Inc hired Schwarzenegger", "nothing here", "東京 John"}
	want := make([][]privacy.Finding, len(texts))
	for i, s := range texts {
		var err error
		want[i], err = d.Detect(context.Background(), s)
		require.NoError(t, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16*len(texts))
	for range 16 {
		for i, s := range texts {
			wg.Go(func() {
				got, err := d.Detect(context.Background(), s)
				if err == nil && !assert.ObjectsAreEqual(want[i], got) {
					err = errors.New("findings differ under concurrency")
				}
				errs <- err
			})
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestDetector_Probe(t *testing.T) {
	t.Parallel()
	d := newDetector(t, newFixture(t, numLabels, nil))
	require.NoError(t, d.Probe(context.Background()))
	assert.Equal(t, ID, d.ID())
}

func TestDetector_Probe_LabelMismatch(t *testing.T) {
	t.Parallel()
	d := newDetector(t, newFixture(t, 5, nil))
	err := d.Probe(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logits shape [1 3 5], want [1 3 9]")
	got, err := d.Detect(context.Background(), "John")
	require.NoError(t, err)
	assertUnavailable(t, got, "John")
}

func TestDetector_Probe_InputNameMismatch(t *testing.T) {
	t.Parallel()
	d := newDetector(t, newFixture(t, numLabels, []string{"ids", "mask", "types"}))
	err := d.Probe(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "piionnx: inference")
}

func TestNew_Errors(t *testing.T) {
	t.Parallel()
	f := newFixture(t, numLabels, nil)
	missing := filepath.Join(t.TempDir(), "absent.json")
	badLabels := writeFile(t, "config.json", `{"id2label": {"0": "O", "1": "X-PER"}}`)
	badTok := writeFile(t, "tokenizer.json", `{"model": {"type": "BPE"}}`)
	tests := []struct {
		name  string
		nilRT bool
		files func(Files) Files
		opts  []Option
		is    error
		msg   string
	}{
		{name: "min score", opts: []Option{WithMinScore(1.5)}, msg: "min_score 1.5"},
		{name: "max tokens", opts: []Option{WithMaxTokens(0)}, msg: "max_tokens 0"},
		{name: "timeout", opts: []Option{WithTimeout(0)}, msg: "timeout 0s"},
		{name: "window too large", opts: []Option{WithWindow(513, 0)}, msg: "window 513"},
		{name: "overlap too large", opts: []Option{WithWindow(16, 14)}, msg: "overlap 14"},
		{name: "threads", opts: []Option{WithIntraOpThreads(-1)}, msg: "intra-op threads -1"},
		{name: "logger", opts: []Option{WithLogger(nil)}, msg: "nil logger"},
		{name: "pool", opts: []Option{WithPool(onnxrt.PoolConfig{})}, is: onnxrt.ErrInvalidConfig},
		{name: "nil runtime", nilRT: true, msg: "nil ONNX runtime"},
		{name: "tokenizer missing", files: func(fs Files) Files { fs.Tokenizer = missing; return fs }, is: os.ErrNotExist},
		{name: "tokenizer unsupported", files: func(fs Files) Files { fs.Tokenizer = badTok; return fs }, msg: "BPE"},
		{name: "config missing", files: func(fs Files) Files { fs.Config = missing; return fs }, is: os.ErrNotExist},
		{name: "config labels", files: func(fs Files) Files { fs.Config = badLabels; return fs }, is: ErrInvalidFile},
		{name: "model missing", files: func(fs Files) Files { fs.Model = missing; return fs }, msg: missing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := f.rt
			if tc.nilRT {
				rt = nil
			}
			files := f.files
			if tc.files != nil {
				files = tc.files(files)
			}
			d, err := New(rt, files, tc.opts...)
			require.Error(t, err)
			assert.Nil(t, d)
			if tc.is != nil {
				require.ErrorIs(t, err, tc.is)
			}
			assert.Contains(t, err.Error(), tc.msg)
		})
	}
}

func TestDetector_Close(t *testing.T) {
	t.Parallel()
	f := newFixture(t, numLabels, nil)
	d, err := New(f.rt, f.files, WithPool(onnxrt.PoolConfig{MaxConcurrency: 1}))
	require.NoError(t, err)
	l, err := d.pool.Acquire(context.Background())
	require.NoError(t, err)
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, d.Close(expired), context.Canceled, "an in-flight run outlives the close deadline")
	l.Release()
	require.NoError(t, d.Close(context.Background()))
	require.NoError(t, d.Close(context.Background()), "idempotent")
}
