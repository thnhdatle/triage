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

const (
	DefaultMaxBodyBytes       = 8 << 20
	DefaultStreamWriteTimeout = 30 * time.Second
)

type Router interface {
	Chat(ctx context.Context, req chat.Request) (route.Outcome, error)
	ChatStream(ctx context.Context, req chat.Request, sink dispatch.Sink) (route.Outcome, error)
	Embeddings(ctx context.Context, in route.EmbeddingsRequest) (route.Outcome, error)
}

type Catalog interface {
	Routes() []string
	Targets() []model.Target
}

type Decisions interface {
	List(limit int, cursor string) ([]decision.Decision, string, error)
	Get(id string) (decision.Decision, bool)
}

type Authenticator interface {
	Authenticate(token string) bool
}

type HealthReader interface {
	IsHealthy(target string) bool
}

type ConfigView interface {
	Redacted() any
}

type Reloader interface {
	Reload(ctx context.Context) error
}

type Settings struct {
	MaxBodyBytes         int64
	ExposeTargets        bool
	ExposeDecisionDetail bool
}

// Server serves the data plane and admin HTTP APIs.
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

type Option func(*Server)

func WithAuth(a Authenticator) Option { return func(s *Server) { s.auth = a } }

func WithSettings(f func() Settings) Option { return func(s *Server) { s.settings = f } }

func WithDecisions(d Decisions) Option { return func(s *Server) { s.decisions = d } }

func WithHealth(h HealthReader) Option { return func(s *Server) { s.health = h } }

func WithConfigView(c ConfigView) Option { return func(s *Server) { s.cfg = c } }

func WithReloader(r Reloader) Option { return func(s *Server) { s.reloader = r } }

func WithReady(f func(ctx context.Context) error) Option { return func(s *Server) { s.ready = f } }

func WithMetrics(h http.Handler) Option { return func(s *Server) { s.metrics = h } }

func WithLogger(l *slog.Logger) Option {
	return func(s *Server) {
		if l != nil {
			s.log = l
		}
	}
}

func WithAccessLog(on bool) Option { return func(s *Server) { s.accessLog = on } }

func WithClock(c clock.Clock) Option {
	return func(s *Server) {
		if c != nil {
			s.clk = c
		}
	}
}

func WithStreamWriteTimeout(d time.Duration) Option {
	return func(s *Server) {
		if d > 0 {
			s.writeTO = d
		}
	}
}

func WithMiddleware(m ...func(http.Handler) http.Handler) Option {
	return func(s *Server) { s.wrap = append(s.wrap, m...) }
}

// New returns a Server.
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

func (s *Server) SetShuttingDown() { s.shuttingDown.Store(true) }

func (s *Server) current() Settings {
	st := s.settings()
	if st.MaxBodyBytes <= 0 {
		st.MaxBodyBytes = DefaultMaxBodyBytes
	}
	return st
}

type listener string

const (
	listenerData  listener = "data"
	listenerAdmin listener = "admin"
	listenerBoth  listener = "both"
)

type routeSpec struct {
	method   string
	pattern  string
	listener listener
	handler  http.Handler
}

func (s *Server) standardRoutes() []routeSpec {
	return []routeSpec{
		{http.MethodGet, "/healthz", listenerBoth, http.HandlerFunc(s.handleHealthz)},
		{http.MethodGet, "/readyz", listenerBoth, http.HandlerFunc(s.handleReadyz)},
		{http.MethodGet, "/v1/version", listenerBoth, http.HandlerFunc(s.handleVersion)},
	}
}

func (s *Server) apiRoutes() []routeSpec {
	return append([]routeSpec{
		{http.MethodPost, "/v1/chat/completions", listenerData, s.authed(s.handleChat)},
		{http.MethodPost, "/v1/embeddings", listenerData, s.authed(s.handleEmbeddings)},
		{http.MethodGet, "/v1/models", listenerData, s.authed(s.handleModels)},
	}, s.standardRoutes()...)
}

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

// API returns the data plane handler.
func (s *Server) API() http.Handler {
	mux := http.NewServeMux()
	register(mux, s.apiRoutes())
	return s.chain(mux)
}

// Admin returns the admin handler.
func (s *Server) Admin() http.Handler {
	mux := http.NewServeMux()
	register(mux, s.adminRoutes())
	return s.chain(mux)
}

func (s *Server) deadline() time.Time { return s.clk.Now().Add(s.writeTO) }
