package bundle

import (
	"testing"
	"time"
)

func TestValidCodeTTL(t *testing.T) {
	for _, n := range []int{30, 120, 600} {
		if !ValidCodeTTL(n) {
			t.Errorf("ValidCodeTTL(%d) = false", n)
		}
	}
	for _, n := range []int{-1, 0, 29, 601, 3600} {
		if ValidCodeTTL(n) {
			t.Errorf("ValidCodeTTL(%d) = true", n)
		}
	}
}

func TestCodeStoreSingleUseAndExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := &codeStore{now: func() time.Time { return now }}

	code, expires := s.issue("user1", time.Minute)
	if len(code) != 48 || !expires.Equal(now.Add(time.Minute)) {
		t.Fatalf("issue = %q, %v", code, expires)
	}
	if user, ok := s.take(code); !ok || user != "user1" {
		t.Fatalf("first take = %q, %v", user, ok)
	}
	if _, ok := s.take(code); ok {
		t.Fatal("a code was accepted twice")
	}
	if _, ok := s.take("unknown"); ok {
		t.Fatal("an unknown code was accepted")
	}

	late, _ := s.issue("user2", 30*time.Second)
	now = now.Add(31 * time.Second)
	if _, ok := s.take(late); ok {
		t.Fatal("an expired code was accepted")
	}

	stale, _ := s.issue("user3", 30*time.Second)
	now = now.Add(time.Minute)
	fresh, _ := s.issue("user4", time.Minute) // purges expired codes
	if _, kept := s.codes[stale]; kept {
		t.Fatal("expired codes are not purged")
	}
	if user, ok := s.take(fresh); !ok || user != "user4" {
		t.Fatalf("fresh take = %q, %v", user, ok)
	}
}
