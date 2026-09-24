package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Spec, section 7: exact names and label sets. The amendment metrics follow.
var want = map[string][]string{
	"wager_transactions_total":         {"kind", "source", "status"},
	"wager_idempotent_replays_total":   {"source"},
	"inbox_duplicates_total":           nil,
	"wallet_lock_conflicts_total":      {"reason"},
	"wager_processing_seconds":         {"source", "status"},
	"sqs_retries_total":                nil,
	"sqs_dlq_total":                    {"reason"},
	"pending_references":               nil,
	"outbox_pending":                   nil,
	"outbox_lag_seconds":               nil,
	"outbox_publish_total":             {"result"},
	"outbox_dead_total":                nil,
	"invariant_violations_total":       {"path"},
	"reconciliation_divergences_total": nil,

	"reference_failed_total":            nil,
	"pending_references_near_deadline":  nil,
	"reference_lock_conflicts_total":    {"reason"},
	"outbox_claim_lost_total":           nil,
	"outbox_bookkeeping_failures_total": nil,
	"outbox_publish_seconds":            nil,
	"sqs_operation_failures_total":      {"operation"},
}

func TestEveryMetricIsExposedWithItsLabelsOnly(t *testing.T) {
	m := New()
	// Series without a preset appear on first use.
	m.ObserveProcessing("sqs", "PROCESSED", time.Millisecond)
	m.SQSDLQ.WithLabelValues("INVALID_MESSAGE").Inc()
	m.OutboxPublishDuration.Observe(0)

	families, err := m.registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, f := range families {
		labels, ok := want[f.GetName()]
		if !ok {
			continue
		}
		seen[f.GetName()] = true
		for _, metric := range f.GetMetric() {
			got := map[string]bool{}
			for _, l := range metric.GetLabel() {
				got[l.GetName()] = true
			}
			if len(got) != len(labels) {
				t.Errorf("%s labels = %v, want %v", f.GetName(), got, labels)
			}
			for _, l := range labels {
				if !got[l] {
					t.Errorf("%s lacks label %s", f.GetName(), l)
				}
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("%s is not exposed", name)
		}
	}
}

func TestQuarantineCountsBothPublishResultAndDead(t *testing.T) {
	m := New()
	RelayObserver{M: m}.Quarantined()
	if testutil.ToFloat64(m.OutboxDead) != 1 || testutil.ToFloat64(m.OutboxPublish.WithLabelValues("quarantined")) != 1 {
		t.Fatal("quarantine not recorded")
	}
}

func TestResolverFailureCountsReferenceFailed(t *testing.T) {
	m := New()
	m.ResolverConcluded("REFUND", "FAILED")
	m.ResolverConcluded("REFUND", "REJECTED")
	if got := testutil.ToFloat64(m.ReferenceFailed); got != 1 {
		t.Fatalf("reference_failed_total = %v", got)
	}
	if got := testutil.ToFloat64(m.Transactions.WithLabelValues("REFUND", "REJECTED", SourceResolver)); got != 1 {
		t.Fatalf("resolver transactions = %v", got)
	}
}
