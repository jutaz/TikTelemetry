package mikrotik

import (
	"testing"

	"github.com/go-routeros/routeros/v3"
	"github.com/go-routeros/routeros/v3/proto"
)

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
