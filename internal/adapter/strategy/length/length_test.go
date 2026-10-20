// SPDX-License-Identifier: Apache-2.0

package length_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/thnhdatle/triage/internal/adapter/strategy/length"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
)

func userReq(text string) chat.Request {
	return chat.Request{Messages: []chat.Message{
		{Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartText, Text: text}}},
	}}
}

type fixedCounter int

func (f fixedCounter) CountTokens(string) int { return int(f) }

func TestScoreCurve(t *testing.T) {
	t.Parallel()
	// A midpoint equal to the estimated prompt tokens of the request scores 0.5.
	midReq := userReq(strings.Repeat("a", 8192))
	s, err := length.New(midReq.PromptTokens(chat.Estimator{}), length.DefaultSlope)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "length" {
		t.Fatalf("name %q", s.Name())
	}
	mid, err := s.Score(context.Background(), midReq)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(mid.P-0.5) > 1e-12 {
		t.Fatalf("midpoint score %v", mid.P)
	}
	short, _ := s.Score(context.Background(), userReq("hi"))
	long, _ := s.Score(context.Background(), userReq(strings.Repeat("a", 40000)))
	if !(short.P < mid.P && mid.P < long.P) {
		t.Fatalf("not monotonic: %v %v %v", short.P, mid.P, long.P)
	}
	for _, sc := range []difficulty.Score{short, mid, long} {
		if err := sc.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScoreFormula(t *testing.T) {
	t.Parallel()
	s, _ := length.New(100, 0.1, length.WithCounter(fixedCounter(130)))
	got, err := s.Score(context.Background(), userReq("x"))
	if err != nil {
		t.Fatal(err)
	}
	if want := 1 / (1 + math.Exp(-3)); math.Abs(got.P-want) > 1e-12 {
		t.Fatalf("got %v want %v", got.P, want)
	}
}

func TestScoreExtremesStayInRange(t *testing.T) {
	t.Parallel()
	s, _ := length.New(0, 1e6, length.WithCounter(fixedCounter(1<<30)))
	got, err := s.Score(context.Background(), userReq("x"))
	if err != nil || got.Validate() != nil || got.P != 1 {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestWholePromptCounts(t *testing.T) {
	t.Parallel()
	s, _ := length.New(100, 0.1)
	req := userReq("short")
	req.Messages = append([]chat.Message{
		{Role: chat.RoleSystem, Content: []chat.Part{{Type: chat.PartText, Text: strings.Repeat("a", 100000)}}},
		{Role: chat.RoleUser, Content: []chat.Part{{Type: chat.PartText, Text: strings.Repeat("a", 100000)}}},
	}, req.Messages...)
	a, _ := s.Score(context.Background(), req)
	b, _ := s.Score(context.Background(), userReq("short"))
	if !(a.P > b.P) {
		t.Fatalf("earlier messages must raise the score: %v %v", a.P, b.P)
	}
}

func TestCounterSeesWholePrompt(t *testing.T) {
	t.Parallel()
	var got string
	s, _ := length.New(100, 0.1, length.WithCounter(captureCounter{&got}))
	req := userReq("question")
	req.Messages = append([]chat.Message{
		{Role: chat.RoleSystem, Content: []chat.Part{{Type: chat.PartText, Text: "sysprompt"}}},
	}, req.Messages...)
	if _, err := s.Score(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sysprompt") || !strings.Contains(got, "question") {
		t.Fatalf("counter saw %q", got)
	}
}

type captureCounter struct{ got *string }

func (c captureCounter) CountTokens(s string) int { *c.got = s; return 1 }

func TestNoUserMessage(t *testing.T) {
	t.Parallel()
	s, _ := length.New(100, 0.1)
	_, err := s.Score(context.Background(), chat.Request{})
	if !errors.Is(err, difficulty.ErrNoScoredText) {
		t.Fatalf("err %v", err)
	}
}

func TestCanceledContext(t *testing.T) {
	t.Parallel()
	s, _ := length.New(100, 0.1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Score(ctx, userReq("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestNewValidation(t *testing.T) {
	t.Parallel()
	cases := map[string]func() error{
		"negative midpoint": func() error { _, err := length.New(-1, 1); return err },
		"zero slope":        func() error { _, err := length.New(1, 0); return err },
		"negative slope":    func() error { _, err := length.New(1, -1); return err },
		"nan slope":         func() error { _, err := length.New(1, math.NaN()); return err },
		"inf slope":         func() error { _, err := length.New(1, math.Inf(1)); return err },
		"nil option":        func() error { _, err := length.New(1, 1, nil); return err },
		"nil counter":       func() error { _, err := length.New(1, 1, length.WithCounter(nil)); return err },
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if f() == nil {
				t.Fatal("expected error")
			}
		})
	}
}
