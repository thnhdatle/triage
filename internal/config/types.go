// SPDX-License-Identifier: Apache-2.0

package config

// Config is the configuration file.
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

type Server struct {
	Listen               string `yaml:"listen"`
	AllowUnauthenticated bool   `yaml:"allow_unauthenticated"`
}

type Admin struct {
	Listen string `yaml:"listen"`
}

type Auth struct {
	Enabled  bool   `yaml:"enabled"`
	KeysFile string `yaml:"keys_file"`
	KeysEnv  string `yaml:"keys_env"`
}

type API struct {
	MaxBodyBytes         Size `yaml:"max_body_bytes"`
	ExposeTargets        bool `yaml:"expose_targets"`
	ExposeDecisionDetail bool `yaml:"expose_decision_detail"`
}

type ONNX struct {
	LibraryPath string `yaml:"library_path"`
}

type Models struct {
	CacheDir string `yaml:"cache_dir"`
}

type Provider struct {
	Type                string `yaml:"type"`
	BaseURL             string `yaml:"base_url"`
	APIKeyEnv           string `yaml:"api_key_env"`
	APIKeyFile          string `yaml:"api_key_file"`
	MaxIdleConnsPerHost int    `yaml:"max_idle_conns_per_host"`
}

type Health struct {
	Probe            string   `yaml:"probe"`
	Interval         Duration `yaml:"interval"`
	FailureThreshold int      `yaml:"failure_threshold"`
	Cooldown         Duration `yaml:"cooldown"`
}

type Selection struct {
	Mode      string `yaml:"mode"`
	Tokenizer string `yaml:"tokenizer"`
}

type Pricing struct {
	InputPerMTok       float64 `yaml:"input_per_mtok"`
	OutputPerMTok      float64 `yaml:"output_per_mtok"`
	CachedInputPerMTok float64 `yaml:"cached_input_per_mtok"`
}

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

type RoutePrivacy struct {
	Mode          string `yaml:"mode"`
	MaxClientMode string `yaml:"max_client_mode"`
}

type Match struct {
	MinPromptTokens int    `yaml:"min_prompt_tokens"`
	LastUserRegex   string `yaml:"last_user_regex"`
	Tier            string `yaml:"tier"`
}

type Rules struct {
	AllowClientOverride bool    `yaml:"allow_client_override"`
	Match               []Match `yaml:"match"`
}

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

type NER struct {
	Enabled   bool     `yaml:"enabled"`
	MinScore  float64  `yaml:"min_score"`
	MaxTokens int      `yaml:"max_tokens"`
	Timeout   Duration `yaml:"timeout"`
}

type Privacy struct {
	ScanSystem   bool            `yaml:"scan_system"`
	MaxScanBytes Size            `yaml:"max_scan_bytes"`
	OnOversize   string          `yaml:"on_oversize"`
	Detectors    map[string]bool `yaml:"detectors"`
	NER          NER             `yaml:"ner"`
}

type Length struct {
	MidpointTokens int     `yaml:"midpoint_tokens"`
	Slope          float64 `yaml:"slope"`
}

type BERT struct {
	Variant        string   `yaml:"variant"`
	Timeout        Duration `yaml:"timeout"`
	MaxInputTokens int      `yaml:"max_input_tokens"`
	IntraOpThreads int      `yaml:"intra_op_threads"`
	MaxConcurrency int      `yaml:"max_concurrency"`
	QueueSize      int      `yaml:"queue_size"`
}

type MF struct {
	AllowExternalEmbedding bool     `yaml:"allow_external_embedding"`
	Provider               string   `yaml:"provider"`
	Timeout                Duration `yaml:"timeout"`
	Fallback               string   `yaml:"fallback"`
	CacheSize              int      `yaml:"cache_size"`
}

type Difficulty struct {
	Timeout         Duration `yaml:"timeout"`
	OnError         string   `yaml:"on_error"`
	MissingArtifact string   `yaml:"missing_artifact"`
	Length          Length   `yaml:"length"`
	BERT            BERT     `yaml:"bert"`
	MF              MF       `yaml:"mf"`
}

type Backoff struct {
	Base Duration `yaml:"base"`
	Max  Duration `yaml:"max"`
}

type Dispatch struct {
	MaxAttempts           int      `yaml:"max_attempts"`
	AttemptTimeout        Duration `yaml:"attempt_timeout"`
	TotalTimeout          Duration `yaml:"total_timeout"`
	Backoff               Backoff  `yaml:"backoff"`
	MaxRetryAfter         Duration `yaml:"max_retry_after"`
	StreamFailure         string   `yaml:"stream_failure"`
	ContextLengthFallback bool     `yaml:"context_length_fallback"`
}

type Decisions struct {
	BufferSize int `yaml:"buffer_size"`
}

type Tracing struct {
	CaptureContent bool `yaml:"capture_content"`
}
