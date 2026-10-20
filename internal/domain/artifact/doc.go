// SPDX-License-Identifier: Apache-2.0

// Package artifact defines the model artifact Manifest, Entry, File and
// Derived types with the Fetcher and Deriver ports.
//
//	e, ok := manifest.Entry("bert")
//	err := fetcher.Fetch(ctx, e, e.Files[0], dir)
package artifact
