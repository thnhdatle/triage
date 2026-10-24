// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/thnhdatle/triage/internal/adapter/anthropic"
	"github.com/thnhdatle/triage/internal/adapter/openaicompat"
	"github.com/thnhdatle/triage/internal/config"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
)

type upstream interface {
	provider.Provider
	CloseIdleConnections()
}

type clientSpec struct {
	Provider string                       `json:"provider"`
	Type     string                       `json:"type"`
	BaseURL  string                       `json:"base_url"`
	KeyHash  string                       `json:"key_hash"`
	MaxIdle  int                          `json:"max_idle"`
	Timeout  time.Duration                `json:"timeout"`
	Headers  map[string]map[string]string `json:"headers"`

	key string
}

func (s clientSpec) cacheKey() string {
	data, _ := json.Marshal(s) //nolint:errchkjson // plain strings, ints and maps cannot fail
	sum := sha256.Sum256(data)
	return "client:" + hex.EncodeToString(sum[:])
}

func newUpstream(s clientSpec) (upstream, error) {
	hc := newHTTPClient(s.MaxIdle, s.Timeout)
	switch s.Type {
	case "anthropic":
		opts := []anthropic.Option{
			anthropic.WithAPIKey(s.key), anthropic.WithTimeout(s.Timeout), anthropic.WithHTTPClient(hc),
		}
		if s.BaseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(s.BaseURL))
		}
		for _, m := range slices.Sorted(maps.Keys(s.Headers)) {
			opts = append(opts, anthropic.WithModelHeaders(m, s.Headers[m]))
		}
		c, err := anthropic.New(opts...)
		if err != nil {
			return nil, fmt.Errorf("provider %s: %w", s.Provider, err)
		}
		return c, nil
	default:
		opts := []openaicompat.Option{
			openaicompat.WithAPIKey(s.key), openaicompat.WithTimeout(s.Timeout), openaicompat.WithHTTPClient(hc),
		}
		for _, m := range slices.Sorted(maps.Keys(s.Headers)) {
			opts = append(opts, openaicompat.WithModelHeaders(m, s.Headers[m]))
		}
		c, err := openaicompat.New(s.BaseURL, opts...)
		if err != nil {
			return nil, fmt.Errorf("provider %s: %w", s.Provider, err)
		}
		return c, nil
	}
}

type providerLayout struct {
	specs        map[string]clientSpec
	targetClient map[string]string
}

func layoutProviders(cfg *config.Config, env Env, mfProvider string) (*providerLayout, error) {
	l := &providerLayout{specs: map[string]clientSpec{}, targetClient: map[string]string{}}
	base := map[string]clientSpec{}
	for _, name := range slices.Sorted(maps.Keys(cfg.Providers)) {
		pc := cfg.Providers[name]
		key, err := env.apiKey(pc.APIKeyEnv, pc.APIKeyFile)
		if err != nil {
			return nil, fmt.Errorf("providers.%s: %w", name, err)
		}
		base[name] = clientSpec{
			Provider: name, Type: pc.Type, BaseURL: pc.BaseURL, KeyHash: hashKey(key), MaxIdle: pc.MaxIdleConnsPerHost,
			Timeout: providerTimeout(cfg, name, mfProvider), Headers: map[string]map[string]string{}, key: key,
		}
	}
	for _, t := range cfg.Targets {
		ck := t.Provider
		spec := base[ck]
		if len(t.ExtraHeaders) > 0 {
			if prev, ok := spec.Headers[t.Model]; ok && !maps.Equal(prev, t.ExtraHeaders) {
				ck = t.Provider + "#" + t.Name
				own := spec
				own.Headers = map[string]map[string]string{t.Model: maps.Clone(t.ExtraHeaders)}
				l.specs[ck] = own
				l.targetClient[t.Name] = ck
				continue
			}
			spec.Headers[t.Model] = maps.Clone(t.ExtraHeaders)
		}
		l.targetClient[t.Name] = ck
	}
	for name, s := range base {
		l.specs[name] = s
	}
	return l, nil
}

func hashKey(k string) string {
	if k == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(k))
	return hex.EncodeToString(sum[:8])
}

func providerTimeout(cfg *config.Config, name, mfProvider string) time.Duration {
	d := time.Duration(cfg.Dispatch.AttemptTimeout)
	for _, t := range cfg.Targets {
		if t.Provider == name {
			d = max(d, time.Duration(t.AttemptTimeout))
		}
	}
	if name == mfProvider {
		d = max(d, time.Duration(cfg.Difficulty.MF.Timeout))
	}
	return d
}

func probe(p provider.Provider, kind, modelName string) func(context.Context) error {
	if kind == "completion" {
		return func(ctx context.Context) error {
			one := 1
			_, err := p.Complete(ctx, chat.Request{
				Model: modelName, MaxTokens: &one,
				Messages: []chat.Message{chat.TextMessage(chat.RoleUser, "ping")},
			})
			return err //nolint:wrapcheck // health only needs success or failure
		}
	}
	return func(ctx context.Context) error {
		_, err := p.Models(ctx)
		return err //nolint:wrapcheck // health only needs success or failure
	}
}
