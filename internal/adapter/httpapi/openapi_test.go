// SPDX-License-Identifier: Apache-2.0

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/thnhdatle/triage/internal/app/dispatch"
	"github.com/thnhdatle/triage/internal/app/route"
	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
)

type specDoc struct {
	OpenAPI string                              `yaml:"openapi"`
	Paths   map[string]map[string]specOperation `yaml:"paths"`
	Comps   struct {
		Schemas map[string]any `yaml:"schemas"`
	} `yaml:"components"`
}

type specOperation struct {
	Listener  string `yaml:"x-triage-listener"`
	Responses map[string]struct {
		Ref     string                    `yaml:"$ref"`
		Content map[string]map[string]any `yaml:"content"`
	} `yaml:"responses"`
}

type stubRouter struct{}

func (stubRouter) Chat(context.Context, chat.Request) (route.Outcome, error) {
	return route.Outcome{}, errors.New("stub")
}

func (stubRouter) ChatStream(context.Context, chat.Request, dispatch.Sink) (route.Outcome, error) {
	return route.Outcome{}, errors.New("stub")
}

func (stubRouter) Embeddings(context.Context, route.EmbeddingsRequest) (route.Outcome, error) {
	return route.Outcome{}, errors.New("stub")
}

type stubCatalog struct{}

func (stubCatalog) Routes() []string        { return nil }
func (stubCatalog) Targets() []model.Target { return nil }

type stubDecisions struct{}

func (stubDecisions) List(int, string) ([]decision.Decision, string, error) { return nil, "", nil }
func (stubDecisions) Get(string) (decision.Decision, bool)                  { return decision.Decision{}, false }

type stubConfig struct{}

func (stubConfig) Redacted() any { return map[string]any{} }

type stubReloader struct{}

func (stubReloader) Reload(context.Context) error { return nil }

func loadSpec(t *testing.T) specDoc {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "openapi.yaml"))
	require.NoError(t, err)
	var doc specDoc
	require.NoError(t, yaml.Unmarshal(b, &doc))
	require.True(t, strings.HasPrefix(doc.OpenAPI, "3.1"), "openapi version %q", doc.OpenAPI)
	return doc
}

func fullServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(stubRouter{}, stubCatalog{}, func() string { return "id" },
		WithDecisions(stubDecisions{}), WithConfigView(stubConfig{}), WithReloader(stubReloader{}),
		WithMetrics(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })))
	require.NoError(t, err)
	return s
}

func key(method, path string) string { return method + " " + path }

func routeSet(rs []routeSpec) map[string]listener {
	out := map[string]listener{}
	for _, r := range rs {
		out[key(r.method, r.pattern)] = r.listener
	}
	return out
}

func sorted[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestOpenAPIMatchesRoutes(t *testing.T) {
	t.Parallel()
	doc := loadSpec(t)
	s := fullServer(t)

	wantAPI, wantAdmin := map[string]bool{}, map[string]bool{}
	for path, ops := range doc.Paths {
		for m, op := range ops {
			k := key(strings.ToUpper(m), path)
			switch listener(op.Listener) {
			case listenerData:
				wantAPI[k] = true
			case listenerAdmin:
				wantAdmin[k] = true
			case listenerBoth:
				wantAPI[k], wantAdmin[k] = true, true
			default:
				t.Errorf("%s: x-triage-listener %q is not data, admin or both", k, op.Listener)
			}
		}
	}

	check := func(name string, rs []routeSpec, want map[string]bool, l listener) {
		got := routeSet(rs)
		require.Len(t, got, len(rs), "%s: duplicate routes", name)
		for k, lis := range got {
			require.True(t, lis == l || lis == listenerBoth, "%s: %s has listener %s", name, k, lis)
		}
		gotSet := map[string]bool{}
		for k := range got {
			gotSet[k] = true
		}
		require.Equal(t, sorted(want), sorted(gotSet), "%s: spec and registered routes differ", name)
	}
	check("API", s.apiRoutes(), wantAPI, listenerData)
	check("Admin", s.adminRoutes(), wantAdmin, listenerAdmin)

	api, admin := routeSet(s.apiRoutes()), routeSet(s.adminRoutes())
	for path, ops := range doc.Paths {
		for m, op := range ops {
			k := key(strings.ToUpper(m), path)
			switch listener(op.Listener) {
			case listenerData:
				require.Contains(t, api, k)
				require.NotContains(t, admin, k)
			case listenerAdmin:
				require.Contains(t, admin, k)
				require.NotContains(t, api, k)
			case listenerBoth:
				require.Contains(t, api, k)
				require.Contains(t, admin, k)
			}
		}
	}
}

func TestOpenAPIOperationsServed(t *testing.T) {
	t.Parallel()
	doc := loadSpec(t)
	s := fullServer(t)
	handlers := map[listener][]http.Handler{
		listenerData: {s.API()}, listenerAdmin: {s.Admin()}, listenerBoth: {s.API(), s.Admin()},
	}
	for path, ops := range doc.Paths {
		for m, op := range ops {
			for _, h := range handlers[listener(op.Listener)] {
				url := strings.ReplaceAll(path, "{id}", "some-id")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), strings.ToUpper(m), url, strings.NewReader("{}")))
				var body struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &body)
				require.NotEqual(t, http.StatusMethodNotAllowed, rec.Code, "%s %s", m, path)
				require.NotEqual(t, CodeNotFound, body.Error.Code, "%s %s", m, path)
			}
		}
	}
}

func TestOpenAPIUnlistedRoutesAreNotServed(t *testing.T) {
	t.Parallel()
	s := fullServer(t)
	for _, h := range []http.Handler{s.API(), s.Admin()} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/v1/not-documented", nil))
		require.Equal(t, http.StatusNotFound, rec.Code)
	}
}

func TestOpenAPISuccessResponsesHaveSchemas(t *testing.T) {
	t.Parallel()
	doc := loadSpec(t)
	for path, ops := range doc.Paths {
		for m, op := range ops {
			ok, found := op.Responses["200"]
			require.True(t, found, "%s %s has no 200 response", m, path)
			require.NotEmpty(t, ok.Content, "%s %s: 200 response has no content", m, path)
			for mt, c := range ok.Content {
				if mt == "application/json" {
					require.NotNil(t, c["schema"], "%s %s: JSON response has no schema", m, path)
				}
			}
		}
	}
	for _, name := range []string{"ErrorResponse", "Decision", "ChatCompletion", "ChatCompletionChunk"} {
		require.Contains(t, doc.Comps.Schemas, name)
	}
}
