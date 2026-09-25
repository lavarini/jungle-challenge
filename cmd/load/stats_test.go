package main

import (
	"strings"
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

func TestOutboxSummaryDrainedIgnoresDeadLetteredRows(t *testing.T) {
	// Regression test: a prior version computed pending as total-published, so 10 dead-lettered
	// rows (published=90, dead=10, total=100 - nothing genuinely still in flight) were wrongly
	// counted as pending, and the report claimed the outbox had not drained.
	status, coverage := outboxSummary(100, 90, 10, 10*time.Minute)
	if !strings.Contains(status, "Drenou completamente") {
		t.Fatalf("status = %q, want it to say the outbox drained (dead-lettered rows are not pending)", status)
	}
	if strings.Contains(status, "pendente") {
		t.Fatalf("status = %q, should not mention pending rows when total = published + dead", status)
	}
	if !strings.Contains(coverage, "todos os 90 eventos publicados") {
		t.Fatalf("coverage = %q, want it to cover all 90 published events", coverage)
	}
}

func TestOutboxSummaryStillPendingIsCensored(t *testing.T) {
	// 100 total, 70 published, 5 dead -> 25 genuinely still in flight (neither published nor
	// dead-lettered), which is what must show up as "pending", not total-published (30).
	status, coverage := outboxSummary(100, 70, 5, 10*time.Minute)
	if !strings.Contains(status, "Não drenou") {
		t.Fatalf("status = %q, want it to say the outbox did not drain", status)
	}
	if !strings.Contains(status, "25 evento(s) ainda pendente(s)") {
		t.Fatalf("status = %q, want it to report 25 pending (100 total - 70 published - 5 dead), not 30", status)
	}
	if !strings.Contains(coverage, "right-censored") {
		t.Fatalf("coverage = %q, want it to say the percentiles are right-censored", coverage)
	}
	if !strings.Contains(coverage, "70 já publicados") {
		t.Fatalf("coverage = %q, want it to say the percentiles cover only the 70 published events", coverage)
	}
}

func TestOutboxSummaryFullyDrainedNoDeadLetters(t *testing.T) {
	status, coverage := outboxSummary(50, 50, 0, 10*time.Minute)
	if !strings.Contains(status, "Drenou completamente") {
		t.Fatalf("status = %q, want it to say the outbox drained", status)
	}
	if !strings.Contains(coverage, "todos os 50 eventos publicados") {
		t.Fatalf("coverage = %q, want it to cover all 50 published events", coverage)
	}
}
