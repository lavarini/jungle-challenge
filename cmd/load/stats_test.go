package main

import (
	"testing"
	"time"
)

func TestPercentileNearestRank(t *testing.T) {
	var d []time.Duration
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	for p, want := range map[float64]time.Duration{50: 50 * time.Millisecond, 95: 95 * time.Millisecond, 99: 99 * time.Millisecond, 100: 100 * time.Millisecond} {
		if got := percentile(d, p); got != want {
			t.Fatalf("p%.0f = %s, want %s", p, got, want)
		}
	}
	if got := percentile(nil, 50); got != 0 {
		t.Fatalf("empty: %s", got)
	}
}
