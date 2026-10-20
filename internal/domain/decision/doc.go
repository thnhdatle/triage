// SPDX-License-Identifier: Apache-2.0

// Package decision defines Decision, Reason, Attempt and the Recorder port.
// A Decision records why a request went where it went; it never holds prompt
// text or finding values.
//
//	d := decision.Decision{ID: id, Route: "triage-auto"}
//	d.AddReason(decision.ReasonRule("length"))
//	recorder.Record(d)
package decision
