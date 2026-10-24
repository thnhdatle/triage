// SPDX-License-Identifier: Apache-2.0

package wire

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"golang.org/x/sync/errgroup"

	"github.com/thnhdatle/triage/internal/adapter/decisionlog"
	"github.com/thnhdatle/triage/internal/adapter/httpapi"
	"github.com/thnhdatle/triage/internal/app/health"
	"github.com/thnhdatle/triage/internal/config"
	"github.com/thnhdatle/triage/internal/platform/buildinfo"
	"github.com/thnhdatle/triage/internal/platform/clock"
	"github.com/thnhdatle/triage/internal/platform/telemetry"
)

const (
	DefaultShutdownTimeout = 30 * time.Second
	readHeaderTimeout      = 10 * time.Second
	readTimeout            = time.Minute
	idleTimeout            = 2 * time.Minute
	writeMargin            = 30 * time.Second
	adminWriteTimeout      = 30 * time.Second
	maxHeaderBytes         = 1 << 20
	buildTimeout           = 2 * time.Minute
)

// ServeOptions configures Serve.
type ServeOptions struct {
	ConfigPath      string
	Logger          *slog.Logger
	Clock           clock.Clock
	Env             Env
	SpanExporter    sdktrace.SpanExporter
	Reload          <-chan struct{}
	WatchInterval   time.Duration
	ShutdownTimeout time.Duration
	Started         func(data, admin net.Addr)
}

type server struct {
	o        ServeOptions
	log      *slog.Logger
	tel      *telemetry.Telemetry
	tracker  *health.Tracker
	b        *builder
	lv       *live
	mgr      *config.Manager
	api      *httpapi.Server
	mu       sync.Mutex
	pending  *generation
	closed   bool
	authKeys atomic.Pointer[httpapi.KeyAuth]
	mfCost   atomic.Pointer[string]
	initial  *config.Config
}

// Serve runs the server until ctx is canceled, then shuts it down gracefully.
func Serve(ctx context.Context, o ServeOptions) error {
	s, err := newServer(ctx, o)
	if err != nil {
		return err
	}
	return s.run(ctx)
}

// newServer builds the server from the configuration.
func newServer(ctx context.Context, o ServeOptions) (*server, error) {
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.ShutdownTimeout <= 0 {
		o.ShutdownTimeout = DefaultShutdownTimeout
	}
	initial, err := config.Load(o.ConfigPath)
	if err != nil {
		return nil, asUsage(err)
	}
	s := &server{o: o, log: o.Logger, initial: initial, lv: &live{}}
	if s.tel, err = telemetry.Setup(ctx, telemetryOptions(initial, o)); err != nil {
		return nil, err
	}
	s.tracker, err = health.New(o.Clock, health.Options{}, health.WithOnChange(s.tel.SetTargetHealth))
	if err != nil {
		return nil, err
	}
	dlog, err := decisionlog.New(initial.Decisions.BufferSize)
	if err != nil {
		return nil, fmt.Errorf("decisions.buffer_size: %w", err)
	}
	libPath, err := o.Env.expandHome(initial.ONNX.LibraryPath)
	if err != nil {
		return nil, err
	}
	s.b = &builder{
		env: o.Env, log: s.log, tel: s.tel, clk: o.Clock, health: s.tracker,
		recorder: observedRecorder{next: dlog, tel: s.tel, costTarget: &s.mfCost},
		ort:      &ortHolder{path: libPath, log: s.log},
	}
	opts := []config.Option{config.WithCheck(s.check(ctx)), config.WithOnReload(s.onReload(ctx))}
	if o.WatchInterval > 0 {
		opts = append(opts, config.WithWatchInterval(o.WatchInterval))
	}
	if s.mgr, err = config.NewManager(o.ConfigPath, opts...); err != nil {
		s.closeAll(ctx)
		return nil, asUsage(err)
	}
	s.activate(ctx)
	if err := s.newAPI(dlog); err != nil {
		s.closeAll(ctx)
		return nil, err
	}
	return s, nil
}

func telemetryOptions(cfg *config.Config, o ServeOptions) telemetry.Options {
	info := buildinfo.Get()
	env := o.Env
	endpoint := env.getenv("OTEL_EXPORTER_OTLP_ENDPOINT") + env.getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	protocol := env.getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")
	if protocol == "" {
		protocol = env.getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	}
	return telemetry.Options{
		Version: info.Version, Commit: info.Commit, GoVersion: info.GoVersion,
		TracingEnabled: endpoint != "" && env.getenv("OTEL_SDK_DISABLED") != "true",
		Protocol:       protocol, CaptureContent: cfg.Tracing.CaptureContent, SpanExporter: o.SpanExporter,
	}
}

func (s *server) check(ctx context.Context) func(*config.Config) error {
	return func(cfg *config.Config) error {
		if cfg.Auth.Enabled {
			if _, err := s.o.Env.authKeys(cfg.Auth.KeysFile, cfg.Auth.KeysEnv); err != nil {
				return err
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			return errServerClosed
		}
		bctx, cancel := context.WithTimeout(ctx, buildTimeout)
		defer cancel()
		g, err := s.b.build(bctx, cfg)
		if err != nil {
			return err
		}
		if s.pending != nil {
			s.b.retire(ctx, s.pending)
		}
		s.pending = g
		return nil
	}
}

var errServerClosed = errors.New("server is shutting down")

func (s *server) activate(ctx context.Context) {
	g := s.pending
	s.pending = nil
	if g.cfg.Auth.Enabled {
		if keys, err := s.o.Env.authKeys(g.cfg.Auth.KeysFile, g.cfg.Auth.KeysEnv); err == nil {
			s.authKeys.Store(httpapi.NewKeyAuth(keys))
		}
	}
	cost := g.plan.mfCostTarget
	s.mfCost.Store(&cost)
	s.b.commit(ctx, s.lv, g)
}

func (s *server) onReload(ctx context.Context) func(config.ReloadEvent) {
	return func(ev config.ReloadEvent) {
		if ev.Err != nil {
			s.tel.ObserveConfigReload("error")
			s.log.Error("config reload rejected, the previous configuration stays active",
				"component", "config", "error", ev.Err)
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		s.activate(ctx)
		s.mu.Unlock()
		s.tel.ObserveConfigReload("success")
		s.log.Info("config reloaded", "component", "config")
		for _, w := range restartOnly(s.initial, ev.New) {
			s.log.Warn(w+" changed; the new value applies after a restart", "component", "config")
		}
	}
}

func restartOnly(a, b *config.Config) []string {
	var out []string
	if a.Server != b.Server {
		out = append(out, "server")
	}
	if a.Admin != b.Admin {
		out = append(out, "admin.listen")
	}
	if a.Auth.Enabled != b.Auth.Enabled {
		out = append(out, "auth.enabled")
	}
	if a.ONNX != b.ONNX {
		out = append(out, "onnx.library_path")
	}
	if a.Decisions != b.Decisions {
		out = append(out, "decisions.buffer_size")
	}
	if a.Tracing != b.Tracing {
		out = append(out, "tracing.capture_content")
	}
	return out
}

type reloadAuth struct {
	keys *atomic.Pointer[httpapi.KeyAuth]
}

func (a reloadAuth) Authenticate(token string) bool {
	k := a.keys.Load()
	return k != nil && k.Authenticate(token)
}

type reloader struct{ m *config.Manager }

func (r reloader) Reload(context.Context) error { return r.m.Reload() }

func (s *server) newAPI(dlog *decisionlog.Log) error {
	rt := &router{live: s.lv}
	opts := []httpapi.Option{
		httpapi.WithSettings(rt.settings), httpapi.WithDecisions(decisionStore{log: dlog}),
		httpapi.WithHealth(s.tracker), httpapi.WithConfigView(rt), httpapi.WithReloader(reloader{m: s.mgr}),
		httpapi.WithReady(func(context.Context) error {
			if s.lv.cur.Load() == nil {
				return errors.New("no configuration loaded")
			}
			return nil
		}),
		httpapi.WithMetrics(s.tel.MetricsHandler()), httpapi.WithLogger(s.log),
		httpapi.WithMiddleware(s.tel.Middleware()), httpapi.WithClock(s.o.Clock),
	}
	if s.initial.Auth.Enabled {
		opts = append(opts, httpapi.WithAuth(reloadAuth{keys: &s.authKeys}))
	}
	api, err := httpapi.New(rt, rt, NewID, opts...)
	if err != nil {
		return fmt.Errorf("http api: %w", err)
	}
	s.api = api
	return nil
}

func (s *server) run(ctx context.Context) error {
	cfg := s.initial
	lc := net.ListenConfig{}
	dataLn, err := lc.Listen(ctx, "tcp", cfg.Server.Listen)
	if err != nil {
		s.closeAll(ctx)
		return fmt.Errorf("listen %s: %w", cfg.Server.Listen, err)
	}
	adminLn, err := lc.Listen(ctx, "tcp", cfg.Admin.Listen)
	if err != nil {
		_ = dataLn.Close()
		s.closeAll(ctx)
		return fmt.Errorf("listen %s: %w", cfg.Admin.Listen, err)
	}
	data := &http.Server{
		Handler: s.api.API(), ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout,
		WriteTimeout: time.Duration(cfg.Dispatch.TotalTimeout) + writeMargin, IdleTimeout: idleTimeout,
		MaxHeaderBytes: maxHeaderBytes,
	}
	admin := &http.Server{
		Handler: s.api.Admin(), ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout,
		WriteTimeout: adminWriteTimeout, IdleTimeout: idleTimeout, MaxHeaderBytes: maxHeaderBytes,
	}
	bg, stopBG := context.WithCancel(context.WithoutCancel(ctx))
	defer stopBG()
	grp, gctx := errgroup.WithContext(bg)
	grp.Go(func() error { return serveHTTP(data, dataLn) })
	grp.Go(func() error { return serveHTTP(admin, adminLn) })
	grp.Go(func() error { return s.background(gctx) })
	s.log.Info(fmt.Sprintf("triage %s listening on %s (admin %s)", buildinfo.Get().Version, dataLn.Addr(),
		adminLn.Addr()), "component", "serve")
	if s.o.Started != nil {
		s.o.Started(dataLn.Addr(), adminLn.Addr())
	}
	select {
	case <-ctx.Done():
	case <-gctx.Done():
	}
	shutErr := s.shutdown(ctx, data, admin)
	stopBG()
	runErr := grp.Wait()
	s.closeAll(ctx)
	return errors.Join(runErr, shutErr)
}

func serveHTTP(srv *http.Server, ln net.Listener) error {
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve %s: %w", ln.Addr(), err)
	}
	return nil
}

func (s *server) background(ctx context.Context) error {
	grp, ctx := errgroup.WithContext(ctx)
	if s.o.WatchInterval >= 0 {
		grp.Go(func() error { _ = s.mgr.Run(ctx); return nil })
	}
	grp.Go(func() error {
		_ = s.tracker.Run(ctx, func() []health.Check {
			if g := s.lv.cur.Load(); g != nil {
				return g.checks
			}
			return nil
		})
		return nil
	})
	if s.o.Reload != nil {
		grp.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-s.o.Reload:
					_ = s.mgr.Reload()
				}
			}
		})
	}
	return grp.Wait() //nolint:wrapcheck // the loops never fail
}

func (s *server) shutdown(ctx context.Context, data, admin *http.Server) error {
	s.api.SetShuttingDown()
	s.log.Info("shutting down: draining in-flight requests", "component", "serve")
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.o.ShutdownTimeout)
	defer cancel()
	var errs []error
	for _, srv := range []*http.Server{data, admin} {
		if err := srv.Shutdown(sctx); err != nil {
			errs = append(errs, fmt.Errorf("drain: %w", err), srv.Close())
		}
	}
	return errors.Join(errs...)
}

func (s *server) closeAll(ctx context.Context) {
	if s.b != nil {
		s.mu.Lock()
		s.closed = true
		if s.pending != nil {
			s.b.retire(ctx, s.pending)
			s.pending = nil
		}
		s.b.shutdown(ctx, s.lv)
		if err := s.b.ort.close(); err != nil {
			s.log.Warn("close onnx runtime", "component", "onnxrt", "error", err)
		}
		s.mu.Unlock()
	}
	if s.tel != nil {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		if err := s.tel.Shutdown(fctx); err != nil {
			s.log.Warn("flush telemetry", "component", "telemetry", "error", err)
		}
	}
}
