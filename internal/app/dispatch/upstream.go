// SPDX-License-Identifier: Apache-2.0

package dispatch

import (
	"context"
	"sync"
	"time"

	"github.com/thnhdatle/triage/internal/domain/chat"
	"github.com/thnhdatle/triage/internal/domain/provider"
	"github.com/thnhdatle/triage/internal/platform/clock"
)

// recvResult is one Recv outcome handed from the reader to the relay.
type recvResult struct {
	chunk chat.Chunk
	err   error
}

// upstream owns the reader goroutine of one streaming attempt. The reader
// opens the stream on the first request and performs one Recv per request,
// so the relay arms its timer before the reader can block and the time to
// open the stream counts towards the time to the first chunk. Both channels
// have capacity 1 and at most one request is outstanding, so neither side
// ever blocks on a send.
type upstream struct {
	reqs   chan struct{}
	res    chan recvResult
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// startUpstream starts the reader for req on p. The stream is opened with a
// context derived from ctx that stop cancels; providers must return from
// Stream and Recv once it is cancelled.
func startUpstream(ctx context.Context, p provider.Provider, req chat.Request) *upstream {
	actx, cancel := context.WithCancel(ctx)
	u := &upstream{reqs: make(chan struct{}, 1), res: make(chan recvResult, 1), cancel: cancel}
	u.wg.Add(1)
	go u.read(actx, p, req)
	return u
}

func (u *upstream) read(ctx context.Context, p provider.Provider, req chat.Request) {
	defer u.wg.Done()
	var s provider.Stream
	defer func() {
		if s != nil {
			_ = s.Close() // the attempt is over; a close error changes nothing
		}
	}()
	for range u.reqs {
		if s == nil {
			st, err := p.Stream(ctx, req)
			if err != nil {
				u.res <- recvResult{err: err}
				return
			}
			s = st
		}
		c, err := s.Recv()
		u.res <- recvResult{chunk: c, err: err}
		if err != nil {
			return
		}
	}
}

// next returns the next chunk, ErrAttemptTimeout when none arrives within d,
// or why ctx ended. After an error, next must not be called again.
func (u *upstream) next(ctx context.Context, clk clock.Clock, d time.Duration) (chat.Chunk, error) {
	timer := clk.After(d)
	u.reqs <- struct{}{}
	select {
	case r := <-u.res:
		return r.chunk, r.err
	case <-timer:
		return chat.Chunk{}, ErrAttemptTimeout
	case <-ctx.Done():
		return chat.Chunk{}, ctxErr(ctx)
	}
}

// stop cancels the attempt, ends the reader and waits for it; the reader
// closes the stream.
func (u *upstream) stop() {
	u.cancel()
	close(u.reqs)
	u.wg.Wait()
}
