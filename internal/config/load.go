// SPDX-License-Identifier: Apache-2.0

package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

func Default() Config {
	return Config{
		Server: Server{Listen: "127.0.0.1:8080"},
		Admin:  Admin{Listen: "127.0.0.1:9091"},
		Auth:   Auth{Enabled: true},
		API:    API{MaxBodyBytes: 8 << 20},
		Models: Models{CacheDir: "~/.cache/rayld/triage/models"},
		Health: Health{
			Probe:            "models",
			Interval:         Duration(10 * time.Second),
			FailureThreshold: 3,
			Cooldown:         Duration(30 * time.Second),
		},
		Selection: Selection{Mode: "priority", Tokenizer: "estimate"},
		Privacy: Privacy{
			MaxScanBytes: 256 << 10,
			OnOversize:   valLocalOnly,
			Detectors:    map[string]bool{"street_address": false},
			NER: NER{
				MinScore:  0.8,
				MaxTokens: 2048,
				Timeout:   Duration(200 * time.Millisecond),
			},
		},
		Difficulty: Difficulty{
			Timeout:         Duration(50 * time.Millisecond),
			OnError:         "strong",
			MissingArtifact: valError,
			Length:          Length{MidpointTokens: 2048, Slope: 0.004},
			BERT: BERT{
				Variant:        "auto",
				Timeout:        Duration(300 * time.Millisecond),
				MaxInputTokens: 512,
				IntraOpThreads: 4,
				QueueSize:      64,
			},
			MF: MF{
				Provider:  "openai",
				Timeout:   Duration(time.Second),
				Fallback:  valBERT,
				CacheSize: 10000,
			},
		},
		Dispatch: Dispatch{
			MaxAttempts:    3,
			AttemptTimeout: Duration(60 * time.Second),
			TotalTimeout:   Duration(120 * time.Second),
			Backoff:        Backoff{Base: Duration(100 * time.Millisecond), Max: Duration(2 * time.Second)},
			MaxRetryAfter:  Duration(10 * time.Second),
			StreamFailure:  "continue",
		},
		Decisions: Decisions{BufferSize: 10000},
	}
}

const (
	valLocalOnly     = "local_only"
	valError         = "error"
	valBERT          = "bert"
	typeOpenAICompat = "openaicompat"
)

const (
	defaultStrategy          = "none"
	defaultThreshold         = 0.5
	defaultTier              = "weak"
	defaultOnTierUnavailable = "escalate"
	defaultPrivacyMode       = "mask"
)

func (c *Config) applyRouteDefaults(thresholdSet []bool) {
	for i := range c.Routes {
		r := &c.Routes[i]
		if r.Strategy == "" {
			r.Strategy = defaultStrategy
		}
		if i >= len(thresholdSet) || !thresholdSet[i] {
			r.Threshold = defaultThreshold
		}
		if r.DefaultTier == "" {
			r.DefaultTier = defaultTier
		}
		if r.OnTierUnavailable == "" {
			r.OnTierUnavailable = defaultOnTierUnavailable
		}
		if r.Privacy.Mode == "" {
			r.Privacy.Mode = defaultPrivacyMode
		}
		if r.Privacy.MaxClientMode == "" {
			r.Privacy.MaxClientMode = valLocalOnly
		}
	}
}

// Parse decodes YAML over Default and applies per-route defaults.
func Parse(data []byte) (*Config, error) {
	cfg := Default()
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: parse: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("config: parse: multiple YAML documents")
	}
	cfg.applyRouteDefaults(explicitThresholds(data))
	return &cfg, nil
}

// Load reads, parses and validates the file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-chosen config path
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	return loadBytes(path, data)
}

func loadBytes(path string, data []byte) (*Config, error) {
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	return cfg, nil
}

func explicitThresholds(data []byte) []bool {
	var raw struct {
		Routes []struct {
			Threshold *float64 `yaml:"threshold"`
		} `yaml:"routes"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	set := make([]bool, len(raw.Routes))
	for i, r := range raw.Routes {
		set[i] = r.Threshold != nil
	}
	return set
}
