package auth

import (
	"testing"
	"time"
)

func TestLimiterBlocksAfterLimit(t *testing.T) {
	l := NewLimiter()
	now := time.Now()
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if !l.Allow("k", 3, time.Minute) {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
	}
	if l.Allow("k", 3, time.Minute) {
		t.Fatal("fourth attempt must be blocked")
	}

	now = now.Add(2 * time.Minute)
	if !l.Allow("k", 3, time.Minute) {
		t.Fatal("window should reset after expiry")
	}
}

func TestLimiterIsKeyed(t *testing.T) {
	l := NewLimiter()
	if !l.Allow("a", 1, time.Minute) {
		t.Fatal("a should be allowed")
	}
	if !l.Allow("b", 1, time.Minute) {
		t.Fatal("different key must have its own budget")
	}
	if l.Allow("a", 1, time.Minute) {
		t.Fatal("a should now be blocked")
	}
}

func TestLimiterDisabledAllowsEverything(t *testing.T) {
	l := NewLimiter()
	l.Disabled = true
	for i := 0; i < 50; i++ {
		if !l.Allow("key", 1, time.Minute) {
			t.Fatal("disabled limiter should allow every request")
		}
	}
}
