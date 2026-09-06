// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: MIT OR Apache-2.0

package zip_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
)

// One service, written twice, projected once.
//
// examples/info is the Lux node's info service in Go; cpp/example/info is the
// same service in C++. Neither knows about the other. Each is read by its own
// language's front end — Go by reflecting on itself, C++ by a pass over its
// source with the compiler's parser — and both hand the SAME projector a
// manifest.
//
// So this test compares bytes. If the OpenAPI document, the MCP tool list, the
// CLI command tree or the ZAP schema differ, then zip is not one framework with
// two front ends; it is one framework and one client that agrees for now. There
// is no weaker way to state the claim, which is why it is stated this way.
func TestTwoFrontEndsOneDocument(t *testing.T) {
	// Two services, because one of them is all reads. The store uses the half a
	// read-only service never reaches: a body, a path parameter, a header, a
	// declared status and a declared id.
	for _, corpus := range []string{"info", "store"} {
		t.Run(corpus, func(t *testing.T) {
			dir := both(t, corpus)
			for _, name := range []string{"openapi.json", "mcp.json", "cli.json", corpus + ".zap"} {
				want, err := os.ReadFile(filepath.Join(dir, "go", name))
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(dir, "cpp", name))
				if err != nil {
					t.Fatal(err)
				}
				if string(want) != string(got) {
					t.Errorf("%s differs between the Go source and the C++ source\n--- go\n%s\n--- cpp\n%s",
						name, first(want), first(got))
				}
			}
		})
	}
}

// both runs the whole build for both languages and answers with the directory
// holding what each produced: go/ and cpp/ hold the four artifacts, and the C++
// service is compiled and ready to run.
//
// It is the pipeline exactly as CMakeLists.txt runs it — zipgen, then the pass,
// then zipc, then the compiler — spelled here so the claim is checked by `go
// test` and not only by a build somebody ran once.
func both(t *testing.T, corpus string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("the C++ front end is built from source; -short skips it")
	}
	clang, err := exec.LookPath("clang++")
	if err != nil {
		t.Skip("no clang++: the C++ front end is a pass over C++ source and needs one")
	}
	if _, err := os.Stat("/usr/lib/llvm-18/include/clang-c/Index.h"); err != nil {
		t.Skip("no libclang headers: the pass reads the source with the compiler's own parser")
	}

	dir := t.TempDir()
	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = "."
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}

	// Go's side: the app describes itself, and zipc projects it.
	run("go", "run", "./examples/"+corpus+"/main", "manifest", filepath.Join(dir, "go.json"))
	run("go", "run", "./cmd/zipc", "-o", filepath.Join(dir, "go"), "-pkg", corpus, filepath.Join(dir, "go.json"))

	// C++'s side: the pass reads the source, and the SAME zipc projects it.
	pass := filepath.Join(dir, "zipc-cpp")
	run(clang, "-std=c++23", "-I", "cpp/include", "-I", "/usr/lib/llvm-18/include",
		"-L", "/usr/lib/llvm-18/lib", "-lclang", "cpp/zipc/main.cpp", "-o", pass)
	// The wire runtime the C++ source includes, and the stub the first parse
	// needs — what the pass is on its way to writing.
	if err := os.MkdirAll(filepath.Join(dir, "zip"), 0o755); err != nil {
		t.Fatal(err)
	}
	run("go", "run", "./cmd/zipgen", "zap", "-lang", "cpp", "-ns", "zip::zap", "-o", filepath.Join(dir, "zip/zap.hpp"))
	if err := os.WriteFile(filepath.Join(dir, corpus+".zip.hpp"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(pass, "cpp/example/"+corpus+"/ops.cpp",
		"-o", filepath.Join(dir, "cpp.json"), "--",
		"-std=c++23", "-Icpp/include", "-Icpp/example", "-I"+dir)
	cmd.Env = append(os.Environ(), "LD_LIBRARY_PATH=/usr/lib/llvm-18/lib")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the pass could not read the C++ ops: %v\n%s", err, out)
	}
	run("go", "run", "./cmd/zipc", "-o", filepath.Join(dir, "cpp"), "-lang", "cpp", "-pkg", corpus, filepath.Join(dir, "cpp.json"))

	// And the service itself, against the bindings that were just written.
	if err := os.Rename(filepath.Join(dir, "cpp", corpus+".zip.hpp"), filepath.Join(dir, corpus+".zip.hpp")); err != nil {
		t.Fatal(err)
	}
	run(clang, "-std=c++23", "-O1", "-I", "cpp/include", "-I", "cpp/example", "-I", dir,
		"cpp/example/"+corpus+"/ops.cpp", "cpp/example/"+corpus+"/main.cpp", "-o", filepath.Join(dir, corpus))
	return dir
}

// The C++ service answers, on both doors, exactly what the Go one answers.
//
// The projections agreeing says the two DESCRIBE the same service. This says
// they ARE one: the same fourteen questions, asked over HTTP and over ZAP, come
// back with the same bytes — and the ZAP client is zip's own, the one a Go
// service is called with, so what it proves is interoperation and not a private
// agreement between two things I wrote.
//
// There is no Go in the C++ process. It links libstdc++ and nothing else.
func TestTheCppServiceAnswersOverBothDoors(t *testing.T) {
	dir := both(t, "info")

	zapAddr, httpAddr := spare(t), spare(t)
	cpp := exec.Command(filepath.Join(dir, "info"), zapAddr, "http://"+httpAddr)
	cpp.Stderr = os.Stderr
	if err := cpp.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cpp.Process.Kill() }()

	goZap, goHTTP := spare(t), spare(t)
	golang := exec.Command("go", "run", "./examples/info/main", goZap, "http://"+goHTTP)
	_ = goZap
	if err := golang.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = golang.Process.Kill() }()

	for _, addr := range []string{httpAddr, goHTTP} {
		if !listening(addr) {
			t.Fatalf("%s never came up", addr)
		}
	}

	for _, path := range []string{
		"/node/version", "/node/id", "/node/ip", "/network/id", "/network/name",
		"/chains", "/lps", "/vms", "/upgrades", "/uptime", "/fees", "/peers",
		"/chain/id?alias=X", "/chain/bootstrapped?chain=X",
	} {
		want := overHTTP(t, "http://"+goHTTP+path)
		if got := overHTTP(t, "http://"+httpAddr+path); got != want {
			t.Errorf("GET %s over HTTP:\n go : %s\n cpp: %s", path, want, got)
		}
		if got := overZAP(t, zapAddr, path); got != want {
			t.Errorf("GET %s over ZAP:\n go : %s\n cpp: %s", path, want, got)
		}
	}
}

// The C++ store refuses, answers and addresses exactly as the Go store does.
//
// The info service is all reads, so it never exercises a body, a path
// parameter, a header, a declared status or a refusal. This does: a document
// that publishes required:true and a 201 describes a service that REFUSES
// without the value and answers 201 with it, and a service that does neither
// has a document that lies about it.
func TestTheCppStoreKeepsWhatItsDocumentPromises(t *testing.T) {
	dir := both(t, "store")

	cppHTTP, goHTTP := spare(t), spare(t)
	cpp := exec.Command(filepath.Join(dir, "store"), spare(t), "http://"+cppHTTP)
	cpp.Stderr = os.Stderr
	if err := cpp.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cpp.Process.Kill() }()

	golang := exec.Command("go", "run", "./examples/store/main", spare(t), "http://"+goHTTP)
	if err := golang.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = golang.Process.Kill() }()
	for _, addr := range []string{cppHTTP, goHTTP} {
		if !listening(addr) {
			t.Fatalf("%s never came up", addr)
		}
	}

	for _, ask := range []struct {
		what   string
		method string
		path   string
		tenant string
		body   string
	}{
		{"a body and a header", "POST", "/v1/items", "hanzo", `{"name":"anvil","count":3}`},
		{"the address", "GET", "/v1/items/item-1", "", ""},
		{"a body over an address", "PATCH", "/v1/items/item-9", "", `{"name":"vise","count":4}`},
		{"an address alone", "DELETE", "/v1/items/item-3", "", ""},
		{"a missing header", "POST", "/v1/items", "", `{"name":"anvil"}`},
		{"a missing member", "POST", "/v1/items", "hanzo", `{"count":3}`},
		{"an address nothing answers", "GET", "/v1/nothing", "", ""},
	} {
		wantCode, wantBody := send(t, "http://"+goHTTP+ask.path, ask.method, ask.tenant, ask.body)
		gotCode, gotBody := send(t, "http://"+cppHTTP+ask.path, ask.method, ask.tenant, ask.body)
		if wantCode != gotCode || wantBody != gotBody {
			t.Errorf("%s (%s %s):\n go : %d %s\n cpp: %d %s",
				ask.what, ask.method, ask.path, wantCode, wantBody, gotCode, gotBody)
		}
	}
}

// send asks one question and answers with the status and the body, whatever
// they are: a refusal is as much of the contract as an answer.
func send(t *testing.T, url, method, tenant, body string) (int, string) {
	t.Helper()
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI(url)
	req.Header.SetMethod(method)
	if tenant != "" {
		req.Header.Set("X-Tenant", tenant)
	}
	if body != "" {
		req.SetBodyString(body)
	}
	if err := fasthttp.Do(req, resp); err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp.StatusCode(), strings.TrimSpace(string(resp.Body()))
}

// spare is an address nothing is listening on, found by listening on one.
func spare(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func listening(addr string) bool {
	for i := 0; i < 200; i++ {
		if c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond); err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

func overHTTP(t *testing.T, url string) string {
	t.Helper()
	code, body, err := fasthttp.Get(nil, url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	if code != 200 {
		t.Fatalf("GET %s: %d %s", url, code, body)
	}
	return strings.TrimSpace(string(body))
}

// overZAP asks the same question on the other door, with zip's own client.
func overZAP(t *testing.T, addr, path string) string {
	t.Helper()
	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.Header.SetMethod("GET")
	req.SetRequestURI(path)
	transport := zaphttp.Dial("tcp", addr)
	if err := transport.Do(req, resp); err != nil {
		t.Fatalf("ZAP %s: %v", path, err)
	}
	if resp.StatusCode() != 200 {
		t.Fatalf("ZAP %s: %d %s", path, resp.StatusCode(), resp.Body())
	}
	return strings.TrimSpace(string(resp.Body()))
}

// first is the head of an artifact, for a failure message that says what
// changed without printing twenty-five kilobytes of document.
func first(b []byte) string {
	if len(b) > 2000 {
		return string(b[:2000]) + "\n…"
	}
	return string(b)
}
