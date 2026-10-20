// SPDX-License-Identifier: Apache-2.0

// Package hfhub implements artifact.Fetcher for Hugging Face: allow-listed
// hosts, pinned 40-hex commit revisions, listed files only (never
// optimizer.pt), SHA-256 and size verification while streaming to a temporary
// file in the destination directory, and an atomic rename on success.
package hfhub
