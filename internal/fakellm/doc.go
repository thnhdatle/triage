// SPDX-License-Identifier: Apache-2.0

// Package fakellm implements contract C2: a fake OpenAI-compatible provider
// with deterministic completions, embeddings, failure scenarios selected by
// X-Fake-Scenario headers or per-model scripts, and a request log.
//
// The main type is Server; cmd/fakellm wires it to a listener.
//
//	srv, err := fakellm.New(fakellm.WithRecord(true, 10000))
//	if err != nil {
//		return err
//	}
//	return http.ListenAndServe(":8090", srv.Handler())
package fakellm
