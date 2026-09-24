package app

import (
	"testing"
	"time"
)

func TestNextAttemptBacksOffExponentiallyUpToTheCap(t *testing.T) {
	p := ReferencePolicy{TTL: time.Hour, InitialBackoff: time.Second, MaxBackoff: 5 * time.Minute}
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	cases := map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 3: 8 * time.Second, 8: 256 * time.Second, 9: 5 * time.Minute, 40: 5 * time.Minute}
	for attempts, want := range cases {
		if got := p.NextAttempt(attempts, now).Sub(now); got != want {
			t.Errorf("attempts %d: delay %s, want %s", attempts, got, want)
		}
	}
}

func TestJitterStaysWithinTwentyPercent(t *testing.T) {
	for i := 0; i < 1000; i++ {
		d := UpToTwentyPercent(10 * time.Second)
		if d < 10*time.Second || d > 12*time.Second {
			t.Fatalf("jittered delay %s outside [10s, 12s]", d)
		}
	}
}

func TestReferencePolicyValidate(t *testing.T) {
	if err := DefaultReferencePolicy().Validate(); err != nil {
		t.Fatalf("default policy invalid: %v", err)
	}
	bad := []ReferencePolicy{
		{TTL: 0, InitialBackoff: time.Second, MaxBackoff: time.Minute},
		{TTL: time.Hour, InitialBackoff: 0, MaxBackoff: time.Minute},
		{TTL: time.Hour, InitialBackoff: time.Minute, MaxBackoff: time.Second},
	}
	for i, p := range bad {
		if p.Validate() == nil {
			t.Errorf("policy %d accepted: %+v", i, p)
		}
	}
}
