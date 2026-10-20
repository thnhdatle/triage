// SPDX-License-Identifier: Apache-2.0

package fakerecorder_test

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/thnhdatle/triage/internal/domain/decision"
	"github.com/thnhdatle/triage/internal/testutil/fakerecorder"
)

var _ decision.Recorder = (*fakerecorder.Recorder)(nil)

func TestRecorder_Record_StoresInOrder(t *testing.T) {
	t.Parallel()
	r := fakerecorder.New()
	assert.Equal(t, 0, r.Len())
	assert.Empty(t, r.Decisions())
	r.Record(decision.Decision{ID: "a"})
	r.Record(decision.Decision{ID: "b"})
	assert.Equal(t, 2, r.Len())
	got := r.Decisions()
	assert.Equal(t, "a", got[0].ID)
	assert.Equal(t, "b", got[1].ID)
}

func TestRecorder_Decisions_ReturnsCopy(t *testing.T) {
	t.Parallel()
	r := fakerecorder.New()
	r.Record(decision.Decision{ID: "a"})
	got := r.Decisions()
	got[0].ID = "changed"
	assert.Equal(t, "a", r.Decisions()[0].ID)
}

func TestRecorder_Record_Concurrent(t *testing.T) {
	t.Parallel()
	r := fakerecorder.New()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Record(decision.Decision{ID: strconv.Itoa(i)})
			_ = r.Len()
		}()
	}
	wg.Wait()
	assert.Equal(t, 50, r.Len())
}
