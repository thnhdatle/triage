// SPDX-License-Identifier: Apache-2.0

package decision

import (
	"fmt"
	"time"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

type UnmaskStatus string

const (
	UnmaskNone    UnmaskStatus = ""
	UnmaskOK      UnmaskStatus = "ok"
	UnmaskPartial UnmaskStatus = "partial"
)

type Attempt struct {
	Target       string
	Model        string
	Locality     model.Locality
	Masked       bool
	Continuation bool
	Status       string
	HTTPStatus   int
	Latency      time.Duration
	Usage        chat.Usage
	CostUSD      float64
}

// Decision records why a request went where it went.
type Decision struct {
	ID             string
	Route          string
	Time           time.Time
	Operation      string
	Privacy        privacy.Mode
	Findings       map[string]int
	Oversize       bool
	Strategy       string
	Variant        string
	Score          *float64
	Threshold      *float64
	ScoringLatency time.Duration
	ScoringCostUSD float64
	Tier           model.Tier
	Reasons        []string
	Attempts       []Attempt
	Unmask         UnmaskStatus
	Usage          chat.Usage
	CostUSD        float64
	Error          string
}

func (d *Decision) AddReason(r string) { d.Reasons = append(d.Reasons, r) }

func (d *Decision) FinalTarget() string {
	if len(d.Attempts) == 0 {
		return ""
	}
	return d.Attempts[len(d.Attempts)-1].Target
}

// Recorder stores decisions.
type Recorder interface {
	Record(d Decision)
}

func ReasonRule(name string) string { return "rule:" + name }

func ReasonStrategy(name string, p, threshold float64) string {
	op := "<"
	if p >= threshold {
		op = ">="
	}
	return fmt.Sprintf("strategy:%s p=%.2f%s%.2f", name, p, op, threshold)
}

func ReasonStrategyError(name string, tier model.Tier, cause string) string {
	return fmt.Sprintf("strategy:%s error->%s(%s)", name, tier, cause)
}

func ReasonStrategyFallback(from, to, cause string) string {
	return fmt.Sprintf("strategy:%s->%s(%s)", from, to, cause)
}

func ReasonPrivacy(what string) string { return "privacy:" + what }

func ReasonFallback(cause string) string { return "fallback:" + cause }

func ReasonTier(action string) string { return "tier:" + action }
