// SPDX-License-Identifier: Apache-2.0

package route

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/thnhdatle/triage/internal/domain/difficulty"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/domain/privacy"
)

const (
	HeaderPrivacy = "X-Triage-Privacy"
	HeaderTier    = "X-Triage-Tier"
)

const (
	StrategyNone = "none"
	StrategyMF   = "mf"
)

type TierAction string

const (
	TierEscalate TierAction = "escalate"
	TierDegrade  TierAction = "degrade"
	TierFail     TierAction = "fail"
)

func (a TierAction) Valid() bool { return a == TierEscalate || a == TierDegrade || a == TierFail }

type Rule struct {
	MinPromptTokens int
	LastUserRegex   *regexp.Regexp
	Tier            model.Tier
}

type Rules struct {
	AllowClientOverride bool
	Match               []Rule
}

type Privacy struct {
	Mode          privacy.Mode
	MaxClientMode privacy.Mode
}

type Fallback struct {
	Tier   model.Tier
	Target string
}

func (f Fallback) String() string {
	if f.Tier != model.TierNone {
		return "tier:" + string(f.Tier)
	}
	return f.Target
}

func ParseFallback(s string) (Fallback, error) {
	if rest, ok := strings.CutPrefix(s, "tier:"); ok {
		t := model.Tier(rest)
		if !t.Valid() {
			return Fallback{}, fmt.Errorf("route: fallback %q: unknown tier (want tier:weak or tier:strong)", s)
		}
		return Fallback{Tier: t}, nil
	}
	if s == "" {
		return Fallback{}, errors.New("route: empty fallback entry")
	}
	return Fallback{Target: s}, nil
}

type Route struct {
	Name              string
	Pool              []string
	Privacy           Privacy
	Rules             Rules
	Strategy          string
	Threshold         difficulty.Threshold
	DefaultTier       model.Tier
	OnTierUnavailable TierAction
	Fallback          []Fallback
}
