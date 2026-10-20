// SPDX-License-Identifier: Apache-2.0

// Package fakellmcontainer runs the fakellm image (built from
// Dockerfile.fakellm at the repository root) with Testcontainers for
// integration tests. The code is compiled only with the
// integration build tag.
//
// One container serves a whole test package: TestMain starts it through
// RunTests, which also terminates it and checks for goroutine leaks.
//
//	var fake *fakellmcontainer.Container
//
//	func TestMain(m *testing.M) {
//		os.Exit(fakellmcontainer.RunTests(m, func(c *fakellmcontainer.Container) { fake = c }))
//	}
//
// The container is shared, so tests isolate themselves by model name (the
// request log is filtered with Container.Requests) and select scenarios with
// X-Fake-* headers instead of the global script.
package fakellmcontainer
