// SPDX-License-Identifier: Apache-2.0

// Package length implements the prompt-length heuristic difficulty strategy:
// p_strong = sigma(slope * (estimated tokens - midpoint_tokens)) over the
// scored text. It needs no artifact.
package length
