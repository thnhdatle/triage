// SPDX-License-Identifier: Apache-2.0

package random_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/thnhdatle/triage/internal/adapter/strategy/random"
	"github.com/thnhdatle/triage/internal/domain/chat"
)

func score(t *testing.T, s *random.Strategy, id string) float64 {
	t.Helper()
	sc, err := s.Score(context.Background(), chat.Request{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Validate(); err != nil {
		t.Fatal(err)
	}
	return sc.P
}

func TestScoreDeterministicPerID(t *testing.T) {
	t.Parallel()
	s, err := random.New()
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "random" {
		t.Fatalf("name %q", s.Name())
	}
	if a, b := score(t, s, "req-1"), score(t, s, "req-1"); a != b {
		t.Fatalf("not deterministic: %v %v", a, b)
	}
	if score(t, s, "req-1") == score(t, s, "req-2") {
		t.Fatal("different ids gave the same score")
	}
}

func TestScoreUniform(t *testing.T) {
	t.Parallel()
	s, _ := random.New()
	const n = 20000
	var sum float64
	below := 0
	for i := range n {
		p := score(t, s, "id-"+strconv.Itoa(i))
		sum += p
		if p < 0.3 {
			below++
		}
	}
	if m := sum / n; m < 0.48 || m > 0.52 {
		t.Fatalf("mean %v", m)
	}
	if f := float64(below) / n; f < 0.28 || f > 0.32 {
		t.Fatalf("P(<0.3) = %v", f)
	}
}

func TestSalt(t *testing.T) {
	t.Parallel()
	a, _ := random.New()
	b, _ := random.New(random.WithSalt(7))
	if score(t, a, "x") == score(t, b, "x") {
		t.Fatal("salt had no effect")
	}
	if _, err := random.New(nil); err == nil {
		t.Fatal("nil option accepted")
	}
}

func TestCanceledContext(t *testing.T) {
	t.Parallel()
	s, _ := random.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Score(ctx, chat.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}
