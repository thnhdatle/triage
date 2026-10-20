// SPDX-License-Identifier: Apache-2.0

package testutil_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/thnhdatle/triage/internal/testutil"
)

func TestEventually_ReturnsWhenConditionHolds(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	testutil.Eventually(t, func() bool { return n.Add(1) >= 3 }, time.Second)
}
