// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

// Defaults applied when an option or setting is zero.
const (
	DefaultMaxBodyBytes       = 8 << 20
	DefaultStreamWriteTimeout = 30 * time.Second
)

// Router is the routing pipeline as the handlers use it.
type Router interface {
	Chat(ctx context.Context, req chat.Request) (route.Outcome, error)
	ChatStream(ctx context.Context, req chat.Request, sink dispatch.Sink) (route.Outcome, error)
	Embeddings(ctx context.Context, in route.EmbeddingsRequest) (route.Outcome, error)
}

// Catalog lists what /v1/models and the admin model view report. It is read on
// every request so a hot-reloaded configuration is visible immediately.
type Catalog interface {
	// Routes returns the route names (virtual models).
	Routes() []string
	// Targets returns the configured targets in pool order.
	Targets() []model.Target
}

// Decisions is the decision store behind the admin API.
type Decisions interface {
	// List returns up to limit decisions, newest first, and the cursor of the
	// next page ("" when there is none). A malformed cursor is an error.
	List(limit int, cursor string) ([]decision.Decision, string, error)
	// Get returns the decision with the id.
	Get(id string) (decision.Decision, bool)
}

// Authenticator validates bearer tokens.
type Authenticator interface {
	Authenticate(token string) bool
}

// HealthReader reports target health for the admin model view.
type HealthReader interface {
	IsHealthy(target string) bool
}

// ConfigView returns the redacted active configuration as a JSON-encodable value.
type ConfigView interface {
	Redacted() any
}

// Reloader reloads the configuration (POST /v1/admin/reload).
type Reloader interface {
	Reload(ctx context.Context) error
}

// Settings are the reloadable api.* options.
type Settings struct {
	// MaxBodyBytes limits request bodies; 0 means DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// ExposeTargets lists targets in GET /v1/models.
	ExposeTargets bool
	// ExposeDecisionDetail adds X-Triage-Route-Reason to non-streamed responses.
	ExposeDecisionDetail bool
}

// Server holds the handlers' dependencies. Create it with New.
type Server struct {
	router    Router
	catalog   Catalog
	newID     func() string
	clk       clock.Clock
	log       *slog.Logger
	accessLog bool
	auth      Authenticator
	settings  func() Settings
	decisions Decisions
	health    HealthReader
	cfg       ConfigView
	reloader  Reloader
	ready     func(ctx context.Context) error
	metrics   http.Handler
	wrap      []func(http.Handler) http.Handler
	writeTO   time.Duration
	started   time.Time

	shuttingDown atomic.Bool
}

// Option configures a Server.
type Option func(*Server)

// WithAuth enables bearer authentication on the data-plane /v1 endpoints.
func WithAuth(a Authenticator) Option { return func(s *Server) { s.auth = a } }

// WithSettings sets the source of the reloadable api.* options.
func WithSettings(f func() Settings) Option { return func(s *Server) { s.settings = f } }

// WithDecisions enables the decision endpoints of the admin API.
func WithDecisions(d Decisions) Option { return func(s *Server) { s.decisions = d } }

// WithHealth adds target health to the admin model view.
func WithHealth(h HealthReader) Option { return func(s *Server) { s.health = h } }

// WithConfigView enables GET /v1/admin/config.
func WithConfigView(c ConfigView) Option { return func(s *Server) { s.cfg = c } }

// WithReloader enables POST /v1/admin/reload.
func WithReloader(r Reloader) Option { return func(s *Server) { s.reloader = r } }

// WithReady sets the readiness check behind /readyz; nil means always ready.
func WithReady(f func(ctx context.Context) error) Option { return func(s *Server) { s.ready = f } }

// WithMetrics serves h on GET /metrics of the admin handler.
func WithMetrics(h http.Handler) Option { return func(s *Server) { s.metrics = h } }

// WithLogger sets the logger; the default discards everything.
func WithLogger(l *slog.Logger) Option {
	return func(s *Server) {
		if l != nil {
			s.log = l
		}
	}
}

// WithAccessLog logs one info line per request when on.
func WithAccessLog(on bool) Option { return func(s *Server) { s.accessLog = on } }

// WithClock sets the time source (response timestamps, stream write deadlines).
func WithClock(c clock.Clock) Option {
	return func(s *Server) {
		if c != nil {
			s.clk = c
		}
	}
}

// WithStreamWriteTimeout sets the per-write deadline of SSE responses.
func WithStreamWriteTimeout(d time.Duration) Option {
	return func(s *Server) {
		if d > 0 {
			s.writeTO = d
		}
	}
}

// WithMiddleware adds middleware between request-id handling and the access
// log, where tracing and metrics belong. The first one is the
// outermost.
func WithMiddleware(m ...func(http.Handler) http.Handler) Option {
	return func(s *Server) { s.wrap = append(s.wrap, m...) }
}

// New returns a Server. newID returns a fresh ULID-like identifier used for
// request ids and decision ids.
func New(router Router, catalog Catalog, newID func() string, opts ...Option) (*Server, error) {
	switch {
	case router == nil:
		return nil, errors.New("httpapi: nil router")
	case catalog == nil:
		return nil, errors.New("httpapi: nil catalog")
	case newID == nil:
		return nil, errors.New("httpapi: nil id generator")
	}
	s := &Server{
		router: router, catalog: catalog, newID: newID,
		clk:      clock.Real(),
		log:      slog.New(slog.DiscardHandler),
		settings: func() Settings { return Settings{} },
		writeTO:  DefaultStreamWriteTimeout,
	}
	for _, o := range opts {
		o(s)
	}
	s.started = s.clk.Now()
	return s, nil
}

// SetShuttingDown makes /healthz and /readyz answer 503 so load balancers
// drain the instance before the listeners close.
func (s *Server) SetShuttingDown() { s.shuttingDown.Store(true) }

func (s *Server) current() Settings {
	st := s.settings()
	if st.MaxBodyBytes <= 0 {
		st.MaxBodyBytes = DefaultMaxBodyBytes
	}
	return st
}

// Listener names the listener a route is served on. The values are those of
// the x-triage-listener extension in api/openapi.yaml.
type listener string

const (
	listenerData  listener = "data"
	listenerAdmin listener = "admin"
	listenerBoth  listener = "both"
)

// routeSpec is one registered route: the mux pattern (path only, the method is
// enforced by the handler so a wrong method answers 405 in the API error shape),
// the HTTP method, the listener and the handler.
type routeSpec struct {
	method   string
	pattern  string
	listener listener
	handler  http.Handler
}

// standardRoutes are the health, readiness, metrics and version endpoints every server has.
func (s *Server) standardRoutes() []routeSpec {
	return []routeSpec{
		{http.MethodGet, "/healthz", listenerBoth, http.HandlerFunc(s.handleHealthz)},
		{http.MethodGet, "/readyz", listenerBoth, http.HandlerFunc(s.handleReadyz)},
		{http.MethodGet, "/v1/version", listenerBoth, http.HandlerFunc(s.handleVersion)},
	}
}

// apiRoutes are the routes of the data-plane handler.
func (s *Server) apiRoutes() []routeSpec {
	return append([]routeSpec{
		{http.MethodPost, "/v1/chat/completions", listenerData, s.authed(s.handleChat)},
		{http.MethodPost, "/v1/embeddings", listenerData, s.authed(s.handleEmbeddings)},
		{http.MethodGet, "/v1/models", listenerData, s.authed(s.handleModels)},
	}, s.standardRoutes()...)
}

// adminRoutes are the routes of the admin-plane handler; optional ones exist
// only when their dependency is set.
func (s *Server) adminRoutes() []routeSpec {
	rs := s.standardRoutes()
	if s.metrics != nil {
		rs = append(rs, routeSpec{http.MethodGet, "/metrics", listenerAdmin, s.metrics})
	}
	if s.decisions != nil {
		rs = append(rs,
			routeSpec{http.MethodGet, "/v1/routing-decisions", listenerAdmin, http.HandlerFunc(s.handleDecisionList)},
			routeSpec{http.MethodGet, "/v1/routing-decisions/{id}", listenerAdmin, http.HandlerFunc(s.handleDecisionGet)})
	}
	if s.cfg != nil {
		rs = append(rs, routeSpec{http.MethodGet, "/v1/admin/config", listenerAdmin, http.HandlerFunc(s.handleConfig)})
	}
	if s.reloader != nil {
		rs = append(rs, routeSpec{http.MethodPost, "/v1/admin/reload", listenerAdmin, http.HandlerFunc(s.handleReload)})
	}
	return append(rs, routeSpec{http.MethodGet, "/v1/admin/models", listenerAdmin, http.HandlerFunc(s.handleAdminModels)})
}

func register(mux *http.ServeMux, rs []routeSpec) {
	for _, r := range rs {
		mux.Handle(r.pattern, method(r.method, r.handler))
	}
	mux.HandleFunc("/", notFound)
}

// API returns the data-plane handler.
func (s *Server) API() http.Handler {
	mux := http.NewServeMux()
	register(mux, s.apiRoutes())
	return s.chain(mux)
}

// Admin returns the admin-plane handler.
func (s *Server) Admin() http.Handler {
	mux := http.NewServeMux()
	register(mux, s.adminRoutes())
	return s.chain(mux)
}

func (s *Server) deadline() time.Time { return s.clk.Now().Add(s.writeTO) }
