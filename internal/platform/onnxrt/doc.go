// SPDX-License-Identifier: Apache-2.0

// Package onnxrt owns the in-process ONNX Runtime: library
// lookup, the process-wide environment lifecycle, sessions with typed
// float32/int64 tensors, and a bounded pool that limits concurrent runs.
//
// The library is loaded with dlopen through github.com/yalue/onnxruntime_go
// (C API v29, so ONNX Runtime 1.29 or newer); nothing is linked at build
// time. [Open] looks it up in the order of onnx.library_path,
// TRIAGE_ONNX_LIBRARY_PATH, the binary's directory, then /opt/homebrew/lib
// and /usr/local/lib. The first Open loads the library and creates the ORT
// environment; every [Runtime] holds a reference and the last Close destroys
// the environment. One Runtime is created in main, passed to the components
// that build sessions and closed at shutdown.
//
// A [Session] is safe for concurrent Run; a [Pool] bounds how many runs share
// it (MaxConcurrency) and how many callers wait (QueueSize). A full queue
// fails fast with [ErrQueueFull], which implements Rejected() bool so the
// route stage records cause "rejected".
//
// Built with -tags noonnx (or without cgo), Open fails with [ErrNotBuilt].
//
// Usage:
//
//	rt, err := onnxrt.Open(onnxrt.WithLibraryPath(cfg.ONNX.LibraryPath))
//	if err != nil {
//		return err
//	}
//	defer rt.Close()
//	sess, err := rt.NewSessionFromFile(graph, onnxrt.SessionConfig{IntraOpThreads: 4})
//	if err != nil {
//		return err
//	}
//	defer sess.Close()
//	pool, err := onnxrt.NewPool(sess, onnxrt.PoolConfig{MaxConcurrency: 2, QueueSize: 64})
//	if err != nil {
//		return err
//	}
//	defer pool.Close(ctx)
//	out, err := pool.Run(ctx, onnxrt.Int64Tensor("input_ids", []int64{1, n}, ids))
package onnxrt
