// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip

import (
	"context"
	"io"
	"testing"
	"time"
)

type tick struct {
	N int `json:"n"`
}

// A tool call collecting a stream that goes quiet answers with what it has
// when its time is up, and the producer sees its ctx end, so a producer that
// waits on it stops instead of outliving the call.
func TestCollect_AQuietStreamEnds(t *testing.T) {
	stopped := make(chan struct{})
	s := &Sse[tick]{Send: func(ctx context.Context, emit func(Event[tick]) error) error {
		if err := emit(Event[tick]{Data: tick{N: 1}}); err != nil {
			return err
		}
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	data, media, truncated, err := collect(ctx, s)
	if err != nil || !truncated || media != "application/jsonl" || string(data) != "{\"n\":1}\n" {
		t.Fatalf("collect = %q %q %v %v", data, media, truncated, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("collect took %v", time.Since(start))
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Error("the producer never saw its ctx end")
	}
}

// A producer that ignores its ctx cannot hold the call: collect still answers.
func TestCollect_AStuckProducerCannotHoldTheCall(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	s := &Sse[tick]{Send: func(_ context.Context, emit func(Event[tick]) error) error {
		<-block
		return nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_, _, _, _ = collect(ctx, s)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("collect waited on a producer that never emits")
	}
}

// A reader that never yields cannot hold a tool call: the read is bounded in
// time as a stream is, and the reader is closed.
func TestReadBounded_AQuietReaderEnds(t *testing.T) {
	r, w := io.Pipe()
	defer func() { _ = w.Close() }()
	go func() { _, _ = w.Write([]byte("first")) }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	data, truncated, err := readBounded(ctx, r)
	if err != nil || !truncated || string(data) != "first" {
		t.Errorf("readBounded = %q %v %v", data, truncated, err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Error("the reader was left open")
	}
}
