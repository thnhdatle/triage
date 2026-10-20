// SPDX-License-Identifier: Apache-2.0

// Package httpapi implements the OpenAI-compatible HTTP handlers, the SSE
// writer, error mapping and the admin API.
//
// Server exposes two handlers: API (the data plane: /v1/chat/completions,
// /v1/embeddings, /v1/models, plus /healthz, /readyz and /v1/version) and
// Admin (decisions, config, model health, reload, /metrics and the standard
// endpoints). The package depends on the routing pipeline and the decision
// store only through the small interfaces Router, Catalog and Decisions, and
// takes every optional collaborator as an Option. Errors on the OpenAI
// endpoints use the OpenAI error shape; the code and status come from
// route.Code and are registered in docs/guides/errors.md.
//
//	srv, err := httpapi.New(pipeline, catalog, newID,
//		httpapi.WithAuth(httpapi.NewKeyAuth(keys)),
//		httpapi.WithDecisions(decisions))
//	if err != nil {
//		return err
//	}
//	go http.Serve(dataListener, srv.API())
//	go http.Serve(adminListener, srv.Admin())
package httpapi
