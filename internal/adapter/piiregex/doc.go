// SPDX-License-Identifier: Apache-2.0

// Package piiregex implements the regex, checksum and structural PII detectors
// of the privacy stage behind a literal prefilter.
//
// New returns one composite privacy.Detector (id "piiregex"). Each finding's
// DetectorID names the sub-detector that produced it: credit_card and iban
// (checksum); email, ip, us_ssn, secret and kr_rrn (structural); phone and
// vn_cccd (context-anchored, a context word within 32 characters); and the
// optional street_address heuristic. Overlapping findings are resolved
// deterministically: checksum over structural over context, then the longer
// span. Offsets are byte offsets and Value always equals text[Start:End].
// Formats and context word lists are documented in testdata/pii/README.md.
//
//	det, err := piiregex.New(piiregex.WithStreetAddress(false))
//	if err != nil {
//		return err
//	}
//	findings, err := det.Detect(ctx, "mail me at jo@example.com")
//	// findings[0].Type == "email", findings[0].Value == "jo@example.com"
package piiregex
