package router

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestSessionsObserveTrend(t *testing.T) {
	s := NewSessions(5, time.Hour)
	if s.Observe("k", 2) {
		t.Fatal("one observation is not a trend")
	}
	if s.Observe("k", 3) {
		t.Fatal("two observations are not a trend")
	}
	if !s.Observe("k", 4) {
		t.Fatal("2->3->4 should trend up (delta >= 2)")
	}
	// A flat conversation never trends.
	for i := 0; i < 5; i++ {
		if s.Observe("flat", 3) {
			t.Fatal("flat conversation must not trend")
		}
	}
}

func TestSessionsWindowSlides(t *testing.T) {
	s := NewSessions(3, time.Hour)
	// 5,1,1,1: once the 5 slides out of the window, no trend.
	s.Observe("k", 5)
	s.Observe("k", 1)
	s.Observe("k", 1)
	if s.Observe("k", 1) {
		t.Fatal("window should have slid past the 5")
	}
	// ...and 1,1,3 within the window trends.
	if !s.Observe("k", 3) {
		t.Fatal("1->1->3 within window should trend")
	}
}

func TestSessionsTTL(t *testing.T) {
	s := NewSessions(3, time.Minute)
	now := time.Now()
	s.now = func() time.Time { return now }
	s.Observe("k", 1)
	s.Observe("k", 2)
	now = now.Add(2 * time.Minute) // conversation went idle past TTL
	if s.Observe("k", 4) {
		t.Fatal("expired session must restart, not trend")
	}
}

func TestSessionKey(t *testing.T) {
	msgs := []schemas.ChatMessage{userMsg(schemas.ChatMessageContent{ContentStr: strPtr("first question")})}
	if got := SessionKey(map[string]string{"x-tumnus-session": "abc"}, "x-tumnus-session", msgs); got != "h:abc" {
		t.Fatalf("header key: got %q", got)
	}
	k1 := SessionKey(nil, "x-tumnus-session", msgs)
	k2 := SessionKey(map[string]string{}, "x-tumnus-session", msgs)
	if k1 == "" || k1 != k2 {
		t.Fatalf("message-hash key not stable: %q vs %q", k1, k2)
	}
	if got := SessionKey(nil, "x-tumnus-session", nil); got != "" {
		t.Fatalf("no identity: got %q", got)
	}
}
