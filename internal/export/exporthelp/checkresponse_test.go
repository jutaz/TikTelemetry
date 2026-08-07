package exporthelp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// trackingBody records how much of the body was consumed and whether it was
// closed, standing in for the response body net/http hands back. Consumption is
// measured by bytes remaining rather than by observing io.EOF: io.LimitReader
// short-circuits at its cap without ever calling through, so an EOF-based flag
// would miss a fully-drained body whose length lands on a buffer boundary.
type trackingBody struct {
	remaining *strings.Reader
	closed    bool
}

func (b *trackingBody) Read(p []byte) (int, error) { return b.remaining.Read(p) }

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

// drained reports whether every byte of the body was consumed.
func (b *trackingBody) drained() bool { return b.remaining.Len() == 0 }

func newResponse(status int, body string) (*http.Response, *trackingBody) {
	tb := &trackingBody{remaining: strings.NewReader(body)}
	return &http.Response{
		StatusCode:    status,
		Body:          tb,
		ContentLength: int64(len(body)),
	}, tb
}

func TestCheckResponseSuccess(t *testing.T) {
	for _, status := range []int{200, 201, 204, 299} {
		resp, _ := newResponse(status, "")
		if err := CheckResponse("test: push", resp); err != nil {
			t.Errorf("status %d: unexpected error: %v", status, err)
		}
	}
}

// CheckResponse must leave the body open: the caller defers the Close and would
// otherwise be closing it twice.
func TestCheckResponseLeavesClosingToTheCaller(t *testing.T) {
	resp, body := newResponse(200, "")
	if err := CheckResponse("test: push", resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body.closed {
		t.Error("CheckResponse closed the body; that is the caller's job")
	}
}

func TestCheckResponseError(t *testing.T) {
	resp, _ := newResponse(500, "boom")
	err := CheckResponse("prometheus: remote_write", resp)
	if err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	want := "prometheus: remote_write returned 500: boom"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// A non-2xx body is quoted back in the error, but only up to the cap — a
// misconfigured endpoint serving an HTML page must not flood the logs.
func TestCheckResponseTruncatesLargeErrorBody(t *testing.T) {
	resp, body := newResponse(400, strings.Repeat("x", maxDrainBytes*4))
	err := CheckResponse("loki: push", resp)
	if err == nil {
		t.Fatal("expected an error for a 400 response")
	}
	if n := strings.Count(err.Error(), "x"); n != maxErrorBodyBytes {
		t.Errorf("quoted %d body bytes, want %d", n, maxErrorBodyBytes)
	}
	// Content-Length exceeds the drain cap here, so the drain is skipped: the
	// connection is unrecoverable either way and reading on would only add
	// latency to the next push.
	if body.drained() {
		t.Error("oversized body should not have been drained")
	}
}

// A non-2xx body that fits under the cap is still drained, so the connection
// survives a transient backend error rather than being torn down.
func TestCheckResponseDrainsSmallErrorBody(t *testing.T) {
	resp, body := newResponse(429, "slow down")
	if err := CheckResponse("loki: push", resp); err == nil {
		t.Fatal("expected an error for a 429 response")
	}
	if !body.drained() {
		t.Error("small error body was not fully drained")
	}
}

// Reading on past the cap buys nothing once Content-Length rules out reuse, so
// the drain must bail out instead of spending push latency on it.
func TestCheckResponseSkipsDrainWhenBodyExceedsCap(t *testing.T) {
	resp, body := newResponse(200, strings.Repeat("y", maxDrainBytes+1))
	if err := CheckResponse("test: push", resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body.drained() {
		t.Error("drain should have been skipped for an oversized body")
	}
}

func TestCheckResponseTrimsWhitespaceFromErrorBody(t *testing.T) {
	resp, _ := newResponse(503, "  unavailable\n\n")
	err := CheckResponse("loki: push", resp)
	if err == nil {
		t.Fatal("expected an error for a 503 response")
	}
	if !strings.HasSuffix(err.Error(), "unavailable") {
		t.Errorf("error should end with the trimmed body, got %q", err.Error())
	}
}

// The body must be read to EOF even on the success path, otherwise net/http
// will not put the connection back in the idle pool.
func TestCheckResponseDrainsSuccessBody(t *testing.T) {
	resp, body := newResponse(200, "ignored payload")
	if err := CheckResponse("test: push", resp); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !body.drained() {
		t.Error("body was not fully drained")
	}
}

// The regression this helper exists to prevent: without draining, every POST
// opens a fresh TCP connection. The agent pushes forever on an interval, so
// that would mean a new handshake per push on a small MikroTik board.
func TestCheckResponseAllowsConnectionReuse(t *testing.T) {
	var mu sync.Mutex
	conns := make(map[string]struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		conns[r.RemoteAddr] = struct{}{}
		mu.Unlock()
		w.WriteHeader(200)
		_, _ = w.Write([]byte("accepted"))
	}))
	defer server.Close()

	client := server.Client()
	for i := 0; i < 5; i++ {
		resp, err := client.Post(server.URL, "application/x-protobuf", strings.NewReader("payload"))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		err = CheckResponse("test: push", resp)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(conns) != 1 {
		t.Errorf("used %d connections across 5 requests, want 1 (body not drained?)", len(conns))
	}
}
