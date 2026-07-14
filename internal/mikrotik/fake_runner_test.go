package mikrotik

import (
	"context"
	"strings"

	"github.com/go-routeros/routeros/v3"
	"github.com/go-routeros/routeros/v3/proto"
)

// fakeRunner implements Runner for tests. It records all calls and returns
// pre-scripted replies (one per call, keyed by cmd[0]) or a fixed error.
type fakeRunner struct {
	// replies maps a command key (e.g. "/system/resource/print") to a queue of
	// replies. Each call to Run pops the next reply from the front.
	replies map[string][]*routeros.Reply
	// err, when non-nil, is returned by every Run call (overrides replies).
	err error
	// calls records every command passed to Run, joined by spaces.
	calls []string
}

func (f *fakeRunner) Run(_ context.Context, cmd ...string) (*routeros.Reply, error) {
	joined := strings.Join(cmd, " ")
	f.calls = append(f.calls, joined)
	if f.err != nil {
		return nil, f.err
	}
	key := cmd[0]
	queue := f.replies[key]
	if len(queue) == 0 {
		// No reply scripted: return an empty reply (no Re sentences).
		return &routeros.Reply{}, nil
	}
	reply := queue[0]
	f.replies[key] = queue[1:]
	return reply, nil
}

// replySentence is a convenience wrapper that builds a single-sentence Reply
// with the given map. This covers the common case for most tests.
func replySentence(m map[string]string) *routeros.Reply {
	return &routeros.Reply{
		Re: []*proto.Sentence{{Map: m}},
	}
}

// replySentences builds a Reply containing multiple sentences, each with its
// own Map.
func replySentences(maps ...map[string]string) *routeros.Reply {
	sentences := make([]*proto.Sentence, len(maps))
	for i, m := range maps {
		sentences[i] = &proto.Sentence{Map: m}
	}
	return &routeros.Reply{Re: sentences}
}
