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

type recvResult struct {
	chunk chat.Chunk
	err   error
}

type upstream struct {
	reqs   chan struct{}
	res    chan recvResult
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

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
			_ = s.Close()
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

func (u *upstream) stop() {
	u.cancel()
	close(u.reqs)
	u.wg.Wait()
}
