// SPDX-License-Identifier: Apache-2.0

//go:build !cgo || noonnx

package onnxrt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStubBuild(t *testing.T) {
	t.Parallel()
	rt, err := Open(WithLibraryPath("/any"))
	require.ErrorIs(t, err, ErrNotBuilt)
	assert.Contains(t, err.Error(), "noonnx")

	var r *Runtime
	assert.Empty(t, r.LibraryPath())
	assert.Empty(t, r.Version())
	require.NoError(t, r.Close())
	_, err = r.NewSession(nil, SessionConfig{})
	require.ErrorIs(t, err, ErrNotBuilt)
	_, err = r.NewSessionFromFile("x.onnx", SessionConfig{})
	require.ErrorIs(t, err, ErrNotBuilt)

	var s *Session
	assert.Nil(t, s.InputNames())
	assert.Nil(t, s.OutputNames())
	_, err = s.Run(t.Context())
	require.ErrorIs(t, err, ErrNotBuilt)
	require.NoError(t, s.Close())
	assert.Nil(t, rt)
}
