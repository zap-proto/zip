// SPDX-License-Identifier: BSD-3-Clause-Eco
// Copyright (C) 2026, Lux Industries Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package zip_test

// The ZAP-HTTP frames, checked from the Go side of the same files.
//
// rust/zip/tests/wire.rs reads testdata/zaphttp/go.* and asserts the Rust door
// understands what this implementation writes. This reads testdata/zaphttp/
// rust.* and asserts the reverse. Neither test needs the other's toolchain, and
// between them a frame cannot change on one side alone: a Rust service and a Go
// one either speak the same wire or one of these two tests fails.
//
// It is here because a door tested only by its own client passes whatever it
// does. The Rust door shipped writing the frame's length prefix little-endian
// while this package writes it big-endian, and every test on both sides passed
// while no Go client could reach the service at all.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
)

func frame(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "zaphttp", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestFrameTheOtherSideWrote reads the frames the Rust door builds.
func TestFrameTheOtherSideWrote(t *testing.T) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	if err := zaphttp.UnmarshalRequest(frame(t, "rust.request"), req); err != nil {
		t.Fatalf("the Rust request frame does not read here: %v", err)
	}
	if got, want := string(req.Header.Method()), "GET"; got != want {
		t.Errorf("method = %q, want %q", got, want)
	}
	if got, want := string(req.RequestURI()), "/node/version"; got != want {
		t.Errorf("target = %q, want %q", got, want)
	}

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	if err := zaphttp.UnmarshalResponse(frame(t, "rust.response"), resp); err != nil {
		t.Fatalf("the Rust response frame does not read here: %v", err)
	}
	if got, want := resp.StatusCode(), 200; got != want {
		t.Errorf("status = %d, want %d", got, want)
	}
	if got, want := string(resp.Body()), `{"ok":true}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got, want := string(resp.Header.ContentType()), "application/json"; got != want {
		t.Errorf("content type = %q, want %q", got, want)
	}
}

// TestFrameThisSideWrites keeps the frames the other side reads current: they
// are this encoder's output, so a change here that Rust does not follow fails
// there rather than in production.
func TestFrameThisSideWrites(t *testing.T) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	req.Header.SetMethod("GET")
	req.SetRequestURI("/node/version")
	got, err := zaphttp.MarshalRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if want := frame(t, "go.request"); !bytes.Equal(got, want) {
		t.Errorf("the request frame moved:\n got % x\nwant % x", got, want)
	}

	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.SetStatusCode(200)
	resp.Header.SetContentType("application/json")
	resp.SetBody([]byte(`{"ok":true}`))
	got, err = zaphttp.MarshalResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	if want := frame(t, "go.response"); !bytes.Equal(got, want) {
		t.Errorf("the response frame moved:\n got % x\nwant % x", got, want)
	}
}

// TestPrefixIsBigEndian states the one thing that was wrong, in the one place
// both sides can be compared: the length ahead of a frame is the transport's,
// and it is big-endian. A ZAP buffer is little-endian throughout, which is
// exactly why this is easy to get backwards.
func TestPrefixIsBigEndian(t *testing.T) {
	var head [4]byte
	binary.BigEndian.PutUint32(head[:], uint32(len(frame(t, "go.request"))))
	if want := [4]byte{0x00, 0x00, 0x00, 0x58}; head != want {
		t.Errorf("prefix = % x, want % x", head, want)
	}
}
