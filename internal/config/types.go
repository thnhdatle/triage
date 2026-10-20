// SPDX-License-Identifier: Apache-2.0

package config

// Config is the whole configuration file. See deploy/config.example.yaml for
// the meaning and default of every option.
type Config struct {
	Server     Server              `yaml:"server"`
	Admin      Admin               `yaml:"admin"`
	Auth       Auth                `yaml:"auth"`
	API        API                 `yaml:"api"`
	ONNX       ONNX                `yaml:"onnx"`
	Models     Models              `yaml:"models"`
	Providers  map[string]Provider `yaml:"providers"`
	Health     Health              `yaml:"health"`
	Selection  Selection           `yaml:"selection"`
	Targets    []Target            `yaml:"targets"`
	Routes     []Route             `yaml:"routes"`
	Privacy    Privacy             `yaml:"privacy"`
	Difficulty Difficulty          `yaml:"difficulty"`
	Dispatch   Dispatch            `yaml:"dispatch"`
	Decisions  Decisions           `yaml:"decisions"`
	Tracing    Tracing             `yaml:"tracing"`
}

// Server is the data-plane listener.
type Server struct {
	Listen               string `yaml:"listen"`
	AllowUnauthenticated bool   `yaml:"allow_unauthenticated"`
}

// Admin is the admin listener.
type Admin struct {
	Listen string `yaml:"listen"`
}

// Auth configures bearer-token authentication of the data plane.
type Auth struct {
	Enabled  bool   `yaml:"enabled"`
	KeysFile string `yaml:"keys_file"`
	KeysEnv  string `yaml:"keys_env"`
}

// API configures the OpenAI-compatible API.
type API struct {
	MaxBodyBytes         Size `yaml:"max_body_bytes"`
	ExposeTargets        bool `yaml:"expose_targets"`
	ExposeDecisionDetail bool `yaml:"expose_decision_detail"`
}

// ONNX configures the ONNX Runtime library lookup.
type ONNX struct {
	LibraryPath string `yaml:"library_path"`
}

// Models configures the model artifact cache.
type Models struct {
	CacheDir string `yaml:"cache_dir"`
}

// Provider is an upstream API endpoint.
type Provider struct {
	Type                string `yaml:"type"`
	BaseURL             string `yaml:"base_url"`
	APIKeyEnv           string `yaml:"api_key_env"`
	APIKeyFile          string `yaml:"api_key_file"`
	MaxIdleConnsPerHost int    `yaml:"max_idle_conns_per_host"`
}

// Health configures target health probing.
type Health struct {
	Probe            string   `yaml:"probe"`
	Interval         Duration `yaml:"interval"`
	FailureThreshold int      `yaml:"failure_threshold"`
	Cooldown         Duration `yaml:"cooldown"`
}

// Selection configures target selection inside a tier.
type Selection struct {
	Mode      string `yaml:"mode"`
	Tokenizer string `yaml:"tokenizer"`
}

// Pricing is USD per one million tokens.
type Pricing struct {
	InputPerMTok       float64 `yaml:"input_per_mtok"`
	OutputPerMTok      float64 `yaml:"output_per_mtok"`
	CachedInputPerMTok float64 `yaml:"cached_input_per_mtok"`
}

// Target is a provider + model with routing attributes.
type Target struct {
	Name           string            `yaml:"name"`
	Provider       string            `yaml:"provider"`
	Model          string            `yaml:"model"`
	Locality       string            `yaml:"locality"`
	Tier           string            `yaml:"tier"`
	ContextWindow  int               `yaml:"context_window"`
	Capabilities   []string          `yaml:"capabilities"`
	ExtraHeaders   map[string]string `yaml:"extra_headers"`
	AttemptTimeout Duration          `yaml:"attempt_timeout"`
	Pricing        Pricing           `yaml:"pricing"`
}

// RoutePrivacy is the privacy setting of a route.
type RoutePrivacy struct {
	Mode          string `yaml:"mode"`
	MaxClientMode string `yaml:"max_client_mode"`
}

// Match is one rule: exactly one of MinPromptTokens or LastUserRegex, and a tier.
type Match struct {
	MinPromptTokens int    `yaml:"min_prompt_tokens"`
	LastUserRegex   string `yaml:"last_user_regex"`
	Tier            string `yaml:"tier"`
}

// Rules is the rule-based routing of a route.
type Rules struct {
	AllowClientOverride bool    `yaml:"allow_client_override"`
	Match               []Match `yaml:"match"`
}

// Route is a virtual model.
type Route struct {
	Name              string       `yaml:"name"`
	Pool              []string     `yaml:"pool"`
	Privacy           RoutePrivacy `yaml:"privacy"`
	Rules             Rules        `yaml:"rules"`
	Strategy          string       `yaml:"strategy"`
	Threshold         float64      `yaml:"threshold"`
	DefaultTier       string       `yaml:"default_tier"`
	OnTierUnavailable string       `yaml:"on_tier_unavailable"`
	Fallback          []string     `yaml:"fallback"`
}

// NER configures the optional NER detector.
type NER struct {
	Enabled   bool     `yaml:"enabled"`
	MinScore  float64  `yaml:"min_score"`
	MaxTokens int      `yaml:"max_tokens"`
	Timeout   Duration `yaml:"timeout"`
}

// Privacy configures the privacy stage.
type Privacy struct {
	ScanSystem   bool            `yaml:"scan_system"`
	MaxScanBytes Size            `yaml:"max_scan_bytes"`
	OnOversize   string          `yaml:"on_oversize"`
	Detectors    map[string]bool `yaml:"detectors"`
	NER          NER             `yaml:"ner"`
}

// Length configures the length strategy.
type Length struct {
	MidpointTokens int     `yaml:"midpoint_tokens"`
	Slope          float64 `yaml:"slope"`
}

// BERT configures the bert strategy.
type BERT struct {
	Variant        string   `yaml:"variant"`
	Timeout        Duration `yaml:"timeout"`
	MaxInputTokens int      `yaml:"max_input_tokens"`
	IntraOpThreads int      `yaml:"intra_op_threads"`
	MaxConcurrency int      `yaml:"max_concurrency"`
	QueueSize      int      `yaml:"queue_size"`
}

// MF configures the mf strategy.
type MF struct {
	AllowExternalEmbedding bool     `yaml:"allow_external_embedding"`
	Provider               string   `yaml:"provider"`
	Timeout                Duration `yaml:"timeout"`
	Fallback               string   `yaml:"fallback"`
	CacheSize              int      `yaml:"cache_size"`
}

// Difficulty configures the difficulty strategies.
type Difficulty struct {
	Timeout         Duration `yaml:"timeout"`
	OnError         string   `yaml:"on_error"`
	MissingArtifact string   `yaml:"missing_artifact"`
	Length          Length   `yaml:"length"`
	BERT            BERT     `yaml:"bert"`
	MF              MF       `yaml:"mf"`
}

// Backoff is the retry backoff.
type Backoff struct {
	Base Duration `yaml:"base"`
	Max  Duration `yaml:"max"`
}

// Dispatch configures attempts, timeouts and stream continuation.
type Dispatch struct {
	MaxAttempts           int      `yaml:"max_attempts"`
	AttemptTimeout        Duration `yaml:"attempt_timeout"`
	TotalTimeout          Duration `yaml:"total_timeout"`
	Backoff               Backoff  `yaml:"backoff"`
	MaxRetryAfter         Duration `yaml:"max_retry_after"`
	StreamFailure         string   `yaml:"stream_failure"`
	ContextLengthFallback bool     `yaml:"context_length_fallback"`
}

// Decisions configures the decision ring buffer.
type Decisions struct {
	BufferSize int `yaml:"buffer_size"`
}

// Tracing configures tracing options.
type Tracing struct {
	CaptureContent bool `yaml:"capture_content"`
}
