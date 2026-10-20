// SPDX-License-Identifier: Apache-2.0

package httpapi_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"

	"github.com/thnhdatle/triage/internal/adapter/httpapi"
	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/domain/model"
)

func compileDecisionSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "api", "schema", "decision.v1.json"))
	require.NoError(t, err)
	sch, err := jsonschema.NewCompiler().Compile(path)
	require.NoError(t, err)
	return sch
}

func TestAdmin_DecisionOutputMatchesSchema(t *testing.T) {
	t.Parallel()
	sch := compileDecisionSchema(t)
	minimal := decision.Decision{ID: "d0", Route: "auto", Operation: "chat", Privacy: "off"}
	failed := sampleDecision()
	failed.ID, failed.Error = "d2", "all targets failed"
	failed.Attempts = append(failed.Attempts, decision.Attempt{
		Target: "local", Model: "m", Locality: model.LocalityLocal, Status: "rate_limited", HTTPStatus: 429,
	})
	fd := &fakeDecisions{items: []decision.Decision{sampleDecision(), minimal, failed}}
	h := newServer(t, newFakeRouter(), httpapi.WithDecisions(fd)).Admin()

	for _, id := range []string{"d1", "d0", "d2"} {
		rec := do(t, h, http.MethodGet, "/v1/routing-decisions/"+id, "")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		inst, err := jsonschema.UnmarshalJSON(rec.Body)
		require.NoError(t, err)
		require.NoError(t, sch.Validate(inst), id)
	}

	rec := do(t, h, http.MethodGet, "/v1/routing-decisions", "")
	require.Equal(t, http.StatusOK, rec.Code)
	list := decodeBody(t, rec)
	for _, d := range list["data"].([]any) {
		require.NoError(t, sch.Validate(d))
	}
}

func TestDecisionSchema_RejectsInvalid(t *testing.T) {
	t.Parallel()
	sch := compileDecisionSchema(t)

	for name, doc := range map[string]string{
		"missing required": `{"id":"x"}`,
		"unknown field":    `{"id":"x","bogus":1}`,
	} {
		inst, err := jsonschema.UnmarshalJSON(strings.NewReader(doc))
		require.NoError(t, err)
		require.Error(t, sch.Validate(inst), name)
	}
}
