// SPDX-License-Identifier: Apache-2.0

// Package artifactstore implements the app/artifact LocalStore on a YAML file:
// the local manifest (manifest.local.yaml) under models.cache_dir. Writes are
// atomic (temp file in the same directory, fsync, rename).
package artifactstore
