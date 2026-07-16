package mikrotik

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/go-routeros/routeros/v3"
	"github.com/go-routeros/routeros/v3/proto"

	"github.com/jutaz/tiktelemetry/internal/config"
)

func TestIsDeviceTrap(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"trap",
			&routeros.DeviceError{Sentence: &proto.Sentence{
				Word: "!trap",
				Map:  map[string]string{"message": "no such command prefix"},
			}},
			true,
		},
		{
			"fatal is not a trap",
			&routeros.DeviceError{Sentence: &proto.Sentence{
				Word: "!fatal",
				Map:  map[string]string{"message": "session closed"},
			}},
			false,
		},
		{
			"device error with nil sentence",
			&routeros.DeviceError{Sentence: nil},
			false,
		},
		{"plain transport error", errTransport, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDeviceTrap(tt.err); got != tt.want {
				t.Errorf("isDeviceTrap(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// errTransport is a stand-in for a non-DeviceError (e.g. a socket error).
var errTransport = &net.OpError{Op: "read", Err: net.ErrClosed}

// fakeROSServer is a minimal RouterOS API server that speaks just enough of the
// binary protocol (via the library's own proto reader/writer) to complete a
// one-stage /login and then reply to the first data command with a scripted
// terminal word (!trap or !fatal). It counts accepted connections so a test can
// tell whether Client.Run reused the session or re-dialled.
type fakeROSServer struct {
	ln net.Listener

	mu       sync.Mutex
	accepted int

	// replyWord is the terminal word returned for a non-/login command
	// ("!trap" or "!fatal"), carrying a message attribute.
	replyWord string
	replyMsg  string
}

func newFakeROSServer(t *testing.T, replyWord, replyMsg string) *fakeROSServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeROSServer{ln: ln, replyWord: replyWord, replyMsg: replyMsg}
	go s.serve()
	return s
}

func (s *fakeROSServer) addr() string { return s.ln.Addr().String() }

func (s *fakeROSServer) connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accepted
}

func (s *fakeROSServer) close() { _ = s.ln.Close() }

func (s *fakeROSServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.accepted++
		s.mu.Unlock()
		go s.handle(conn)
	}
}

// handle serves one connection: complete the login, then answer subsequent
// commands with the scripted terminal word. The library runs commands in async
// mode and correlates replies by the request's .tag, so every reply must echo
// the tag it received or the client's read loop blocks forever.
func (s *fakeROSServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := proto.NewReader(conn)
	w := proto.NewWriter(conn)

	for {
		sen, err := r.ReadSentence()
		if err != nil {
			return
		}
		// A command sentence carries the command word first (e.g. "/login" or
		// "/system/resource/print") and a .tag the reply must echo.
		switch sen.Word {
		case "/login":
			// Post-6.43 one-stage login: a bare !done with no challenge.
			writeSentence(w, sen.Tag, "!done", "")
		default:
			// Any data command: reply with the scripted terminal word.
			writeSentence(w, sen.Tag, s.replyWord, s.replyMsg)
			// A !trap is a NON-terminal error sentence in the RouterOS
			// protocol: it is always followed by a !done that ends the reply.
			// (A !fatal, by contrast, terminates the reply on its own.)
			if s.replyWord == "!trap" {
				writeSentence(w, sen.Tag, "!done", "")
			}
		}
	}
}

// writeSentence writes a single terminal sentence: the word, the correlating
// .tag, an optional =message=... attribute, then the empty end-of-sentence
// marker.
func writeSentence(w proto.Writer, tag, word, message string) {
	w.BeginSentence()
	w.WriteWord(word)
	if tag != "" {
		w.WriteWord(".tag=" + tag)
	}
	if message != "" {
		w.WriteWord("=message=" + message)
	}
	_ = w.EndSentence()
}

// TestClientRunKeepsSessionOnTrap proves the fix: a !trap reply (a per-command
// error over a healthy session, e.g. querying wireless on a wired router) does
// NOT tear the connection down. The second Run therefore reuses the existing
// session, so the server only ever accepts one connection.
func TestClientRunKeepsSessionOnTrap(t *testing.T) {
	s := newFakeROSServer(t, "!trap", "no such command prefix")
	defer s.close()

	c := New(config.RouterConfig{
		Address:     s.addr(),
		Username:    "admin",
		Password:    "x",
		DialTimeout: 2 * time.Second,
	}, quietLogger())
	defer func() { _ = c.Close() }()

	for i := 0; i < 3; i++ {
		if _, err := c.Run(context.Background(), "/interface/wireless/registration-table/print"); err == nil {
			t.Fatalf("call %d: expected a trap error to surface to the caller", i)
		}
	}

	if got := s.connections(); got != 1 {
		t.Errorf("server accepted %d connections; want 1 (a trap must not trigger a reconnect)", got)
	}
}

// TestClientRunReconnectsOnFatal is the counterpart: a !fatal reply means the
// session is gone, so Client.Run must close and re-dial. Each Run then opens a
// fresh connection.
func TestClientRunReconnectsOnFatal(t *testing.T) {
	s := newFakeROSServer(t, "!fatal", "session closed")
	defer s.close()

	c := New(config.RouterConfig{
		Address:     s.addr(),
		Username:    "admin",
		Password:    "x",
		DialTimeout: 2 * time.Second,
	}, quietLogger())
	defer func() { _ = c.Close() }()

	for i := 0; i < 2; i++ {
		if _, err := c.Run(context.Background(), "/system/resource/print"); err == nil {
			t.Fatalf("call %d: expected a fatal error to surface", i)
		}
	}

	if got := s.connections(); got != 2 {
		t.Errorf("server accepted %d connections; want 2 (a fatal must trigger a reconnect)", got)
	}
}
