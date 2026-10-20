// SPDX-License-Identifier: Apache-2.0

// Package privacy defines the Detector port, Finding, Mode with its strictness
// order, the request Scan, the per-request Mapping and the placeholder codec
// used to mask requests to cloud targets and unmask their output,
// non-streamed (Mapping.Unmask, UnmaskJSONString, UnmaskResponse) and streamed
// (StreamUnmasker).
//
//	res, err := privacy.Scan(ctx, detectors, req, privacy.ScanOptions{MaxScanBytes: 256 << 10})
//	if err != nil { /* ErrScanLimit or a detector error: apply policy */ }
//	if res.RequiresLocal(mode) { /* local targets only */ }
//	m := privacy.NewMapping(req, res.Findings)
//	cloudReq := m.MaskRequest(req)              // per cloud attempt
//	out, partial := m.UnmaskResponse(resp)      // X-Triage-Unmask: partial
//	su := m.NewStreamUnmasker()                 // streams: su.UnmaskChunk(c), su.Flush()
package privacy
