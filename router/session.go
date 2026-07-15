package router

import (
	"hash/fnv"
	"strconv"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Sessions tracks a sliding window of complexity scores per conversation so
// an escalating conversation can reach the frontier one rung earlier.
type Sessions struct {
	mu     sync.Mutex
	m      map[string]*session
	window int
	ttl    time.Duration
	now    func() time.Time // injectable for tests
}

type session struct {
	complexities []int
	lastSeen     time.Time
}

func NewSessions(window int, ttl time.Duration) *Sessions {
	return &Sessions{m: make(map[string]*session), window: window, ttl: ttl, now: time.Now}
}

// Observe records a complexity score for a conversation and reports whether
// its complexity is trending up: at least 3 scores in the window and a
// first-to-last delta of 2 or more.
func (s *Sessions) Observe(key string, complexity int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	// ponytail: full-map sweep once the map is large; shard or background-sweep
	// if concurrent sessions ever exceed ~100k.
	if len(s.m) > 10000 {
		for k, v := range s.m {
			if now.Sub(v.lastSeen) > s.ttl {
				delete(s.m, k)
			}
		}
	}
	st := s.m[key]
	if st == nil || now.Sub(st.lastSeen) > s.ttl {
		st = &session{}
		s.m[key] = st
	}
	st.lastSeen = now
	st.complexities = append(st.complexities, complexity)
	if len(st.complexities) > s.window {
		st.complexities = st.complexities[len(st.complexities)-s.window:]
	}
	c := st.complexities
	return len(c) >= 3 && c[len(c)-1]-c[0] >= 2
}

// SessionKey identifies a conversation: the session header when the client
// sends one, otherwise a hash of the conversation's first user message
// (stable across turns because history is resent every turn). "" means the
// request has no usable identity and must not be tracked.
func SessionKey(headers map[string]string, headerName string, msgs []schemas.ChatMessage) string {
	if v := headers[headerName]; v != "" {
		return "h:" + v
	}
	first := ExtractFirstUserText(msgs, 200)
	if first == "" {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(first))
	return "m:" + strconv.FormatUint(h.Sum64(), 16)
}
