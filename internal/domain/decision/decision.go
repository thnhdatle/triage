// SPDX-License-Identifier: Apache-2.0

package decision

import (
	"fmt"
	"time"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

// UnmaskStatus is the outcome of unmasking a masked response.
type UnmaskStatus string

// Unmask statuses. UnmaskNone means no attempt was masked.
const (
	UnmaskNone    UnmaskStatus = ""
	UnmaskOK      UnmaskStatus = "ok"
	UnmaskPartial UnmaskStatus = "partial"
)

// Attempt is one call to one target.
type Attempt struct {
	Target   string
	Model    string
	Locality model.Locality
	Masked   bool
	// Continuation is true for a mid-stream continuation attempt.
	Continuation bool
	// Status is "ok" or the error class, e.g. "rate_limited", "timeout", "cut".
	Status string
	// HTTPStatus is the upstream status when one was received.
	HTTPStatus int
	Latency    time.Duration
	Usage      chat.Usage
	CostUSD    float64
}

// Decision is the routing record of one request.
type Decision struct {
	ID        string
	Route     string
	Time      time.Time
	Operation string // "chat" or "embeddings"
	Privacy   privacy.Mode
	// Findings counts findings by type; never values.
	Findings map[string]int
	Oversize bool
	Strategy string
	Variant  string
	// Score is nil when no strategy ran.
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
	// Error is the client-facing error code when the request failed.
	Error string
}

// AddReason appends a reason.
func (d *Decision) AddReason(r string) { d.Reasons = append(d.Reasons, r) }

// FinalTarget returns the target of the last attempt, or "".
func (d *Decision) FinalTarget() string {
	if len(d.Attempts) == 0 {
		return ""
	}
	return d.Attempts[len(d.Attempts)-1].Target
}

// Recorder stores decisions. Implementations must be safe for concurrent use
// and must not block the request path.
type Recorder interface {
	Record(d Decision)
}

// ReasonRule is the reason for a rule match, e.g. "rule:length".
func ReasonRule(name string) string { return "rule:" + name }

// ReasonStrategy is the reason for a strategy decision, e.g.
// "strategy:bert p=0.71>=0.62".
func ReasonStrategy(name string, p, threshold float64) string {
	op := "<"
	if p >= threshold {
		op = ">="
	}
	return fmt.Sprintf("strategy:%s p=%.2f%s%.2f", name, p, op, threshold)
}

// ReasonStrategyError is the reason for a strategy failure, e.g.
// "strategy:bert error->strong(timeout)".
func ReasonStrategyError(name string, tier model.Tier, cause string) string {
	return fmt.Sprintf("strategy:%s error->%s(%s)", name, tier, cause)
}

// ReasonStrategyFallback is the reason for a strategy substitution, e.g.
// "strategy:mf->bert(privacy)".
func ReasonStrategyFallback(from, to, cause string) string {
	return fmt.Sprintf("strategy:%s->%s(%s)", from, to, cause)
}

// ReasonPrivacy is the reason for a privacy constraint, e.g. "privacy:local_only".
func ReasonPrivacy(what string) string { return "privacy:" + what }

// ReasonFallback is the reason for a fallback attempt, e.g. "fallback:429".
func ReasonFallback(cause string) string { return "fallback:" + cause }

// ReasonTier is the reason for an on_tier_unavailable action, e.g. "tier:escalate".
func ReasonTier(action string) string { return "tier:" + action }
