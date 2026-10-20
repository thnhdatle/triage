// SPDX-License-Identifier: Apache-2.0

// Package artifact implements the models pull, list and require use cases:
// fetch the allow-listed files of a pinned revision, verify them, run the
// derivers and write the local manifest.
//
// File verification (SHA-256 and size of files already on disk) uses a small
// stdlib implementation, [FileVerifier], behind the [Verifier] port. The
// choice keeps the use case testable without a second adapter and costs only
// os and crypto/sha256. The local manifest is read and written through the
// [LocalStore] port, implemented by an adapter.
//
// Usage:
//
//	svc, err := artifact.New(manifest, fetcher, derivers, store, cacheDir)
//	if err != nil {
//		return err
//	}
//	report, err := svc.Pull(ctx, []string{"all"}, artifact.PullOptions{INT8: true})
//	statuses, err := svc.List(ctx)
//	err = svc.Require(ctx, "bert") // wraps artifact.ErrMissing with a hint
package artifact
