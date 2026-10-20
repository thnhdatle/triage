// SPDX-License-Identifier: Apache-2.0

// Package testutil holds helpers shared by tests only: Eventually and, in
// sub-packages fake<port>, hand-written in-memory implementations of the
// domain ports.
//
//	testutil.Eventually(t, func() bool { return rec.Len() == 1 }, time.Second)
package testutil
