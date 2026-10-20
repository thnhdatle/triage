// SPDX-License-Identifier: Apache-2.0

package random

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/difficulty"
)

// Name is the strategy name used in configuration and decisions.
const Name = "random"

// Strategy is the random difficulty strategy. It holds no state and is safe
// for concurrent use.
type Strategy struct {
	salt uint64
}

var _ difficulty.Strategy = (*Strategy)(nil)

// Option configures a Strategy.
type Option func(*Strategy)

// WithSalt mixes salt into the seed, giving an independent stream per salt
// (used by evaluation runs that need several random baselines).
func WithSalt(salt uint64) Option { return func(s *Strategy) { s.salt = salt } }

// New returns a random Strategy.
func New(opts ...Option) (*Strategy, error) {
	s := &Strategy{}
	for _, o := range opts {
		if o == nil {
			return nil, errors.New("random: nil option")
		}
		o(s)
	}
	return s, nil
}

// Name implements difficulty.Strategy.
func (*Strategy) Name() string { return Name }

// Score implements difficulty.Strategy. The score depends only on req.ID (and
// the salt); the request text is ignored.
func (s *Strategy) Score(ctx context.Context, req chat.Request) (difficulty.Score, error) {
	if err := ctx.Err(); err != nil {
		return difficulty.Score{}, fmt.Errorf("random: %w", err)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(req.ID))
	seed := h.Sum64()
	r := rand.New(rand.NewPCG(seed, seed^s.salt^0x9e3779b97f4a7c15)) //nolint:gosec // not security relevant
	return difficulty.Score{P: r.Float64()}, nil
}
