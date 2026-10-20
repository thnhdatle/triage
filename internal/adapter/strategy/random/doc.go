// SPDX-License-Identifier: Apache-2.0

// Package random implements the random baseline difficulty strategy: a uniform
// score in [0,1) drawn from a generator seeded by the request id, so a retry of
// the same request is scored identically.
package random
