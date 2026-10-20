// SPDX-License-Identifier: Apache-2.0

package buildinfo_test

import (
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/thnhdatle/triage/internal/platform/buildinfo"
)

func TestInfo_Get_ReportsRuntimeVersion(t *testing.T) {
	t.Parallel()

	info := buildinfo.Get()

	assert.Equal(t, runtime.Version(), info.GoVersion)
	assert.NotEmpty(t, info.Version)
}
