package mikrotik

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/go-routeros/routeros/v3"
	"github.com/go-routeros/routeros/v3/proto"

	"github.com/jutaz/tiktelemetry/internal/config"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func makeReply(n int) *routeros.Reply {
	re := make([]*proto.Sentence, n)
	for i := range re {
		re[i] = &proto.Sentence{Map: map[string]string{}}
	}
	return &routeros.Reply{Re: re}
}

func TestCapRows(t *testing.T) {
	t.Run("truncates when over cap", func(t *testing.T) {
		r := makeReply(100)
		if !capRows(r, 10) {
			t.Fatal("capRows should report truncation")
		}
		if len(r.Re) != 10 {
			t.Errorf("rows after cap = %d, want 10", len(r.Re))
		}
	})

	t.Run("no-op when at or under cap", func(t *testing.T) {
		r := makeReply(5)
		if capRows(r, 10) {
			t.Error("capRows should not truncate when under cap")
		}
		if len(r.Re) != 5 {
			t.Errorf("rows = %d, want 5 (unchanged)", len(r.Re))
		}
	})

	t.Run("zero cap disables truncation", func(t *testing.T) {
		r := makeReply(1000)
		if capRows(r, 0) {
			t.Error("cap of 0 should disable truncation")
		}
		if len(r.Re) != 1000 {
			t.Errorf("rows = %d, want 1000 (unlimited)", len(r.Re))
		}
	})

	t.Run("nil reply is safe", func(t *testing.T) {
		if capRows(nil, 10) {
			t.Error("nil reply should not report truncation")
		}
	})
}

// TestClientRunDialFailure exercises the real Client.Run dial path (which the
// fakeRunner bypasses) against an address that refuses connections, verifying
// the error surfaces and the client stays ready to re-dial on the next call.
func TestClientRunDialFailure(t *testing.T) {
	// Bind and immediately close a listener to get a port that refuses.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // now nothing is listening on addr

	c := New(config.RouterConfig{
		Address:     addr,
		Username:    "admin",
		Password:    "x",
		DialTimeout: 500 * time.Millisecond,
	}, quietLogger())
	defer func() { _ = c.Close() }()

	// First call must fail to dial.
	if _, err := c.Run(context.Background(), "/system/resource/print"); err == nil {
		t.Fatal("expected dial error against a closed port")
	}
	// The client must have left itself in a re-dialable state (cli nil), so a
	// second call also attempts (and fails) rather than panicking on a stale
	// connection.
	if _, err := c.Run(context.Background(), "/system/resource/print"); err == nil {
		t.Fatal("expected second dial to also fail")
	}
}

// TestClientRunReconnectsAfterServerClose stands up a TCP server that accepts a
// connection and then immediately closes it, forcing the go-routeros login to
// fail. This drives the reconnect branch in Client.Run (close + nil the client
// on error) using a real *routeros.Client, which the fakeRunner cannot reach.
func TestClientRunReconnectsAfterServerClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	var accepted int
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2; i++ {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted++
			_ = conn.Close() // drop immediately -> login fails
		}
	}()

	c := New(config.RouterConfig{
		Address:     ln.Addr().String(),
		Username:    "admin",
		Password:    "x",
		DialTimeout: time.Second,
	}, quietLogger())
	defer func() { _ = c.Close() }()

	// Two calls, each should attempt a fresh connection (proving the client
	// nils its handle after a failure and re-dials).
	for i := 0; i < 2; i++ {
		if _, err := c.Run(context.Background(), "/system/resource/print"); err == nil {
			t.Errorf("call %d: expected an error when the server drops the connection", i)
		}
	}

	<-done
	if accepted < 1 {
		t.Errorf("server accepted %d connections, want at least 1 (client did not dial)", accepted)
	}
}
