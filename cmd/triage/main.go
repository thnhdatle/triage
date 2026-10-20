// SPDX-License-Identifier: Apache-2.0

// Command triage routes LLM requests by difficulty and privacy behind an
// OpenAI-compatible API. All wiring lives in internal/wire; main only owns
// the process signals.
//
// Usage:
//
//	triage serve --config triage.yaml
//	triage models pull bert
//	triage config validate --config triage.yaml
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/thnhdatle/triage/internal/wire"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	reload := make(chan struct{}, 1)
	go forward(ctx, hup, reload)
	code := (&wire.CLI{Reload: reload}).Run(ctx, os.Args[1:])
	signal.Stop(hup)
	stop()
	os.Exit(code)
}

// forward turns SIGHUP into reload requests; a reload already pending
// absorbs further signals.
func forward(ctx context.Context, sig <-chan os.Signal, reload chan<- struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-sig:
			select {
			case reload <- struct{}{}:
			default:
			}
		}
	}
}
