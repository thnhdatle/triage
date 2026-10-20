// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestForward_CoalescesAndStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	sig := make(chan os.Signal, 2)
	reload := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { forward(ctx, sig, reload); close(done) }()
	sig <- os.Interrupt
	sig <- os.Interrupt
	select {
	case <-reload:
	case <-time.After(5 * time.Second):
		t.Fatal("no reload forwarded")
	}
	cancel()
	<-done
}
