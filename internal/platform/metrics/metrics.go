// Package metrics owns the Prometheus registry. Labels never carry wallet,
// provider, transaction or event ids (design.md §7): every label value
// here comes from a closed set (kinds, statuses, sources, reasons, results).
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lavarini/backend-challenge-go/internal/wagering"
)

// SourceResolver labels operations concluded by the reference worker; HTTP
// and SQS use app.SourceHTTP and app.SourceSQS.
const SourceResolver = "resolver"

type Metrics struct {
	registry *prometheus.Registry

	// design.md §7.
	Transactions              *prometheus.CounterVec
	Replays                   *prometheus.CounterVec
	InboxDuplicates           prometheus.Counter
	LockConflicts             *prometheus.CounterVec
	Processing                *prometheus.HistogramVec
	SQSRetries                prometheus.Counter
	SQSDLQ                    *prometheus.CounterVec
	PendingReferences         prometheus.Gauge
	OutboxPending             prometheus.Gauge
	OutboxLag                 prometheus.Gauge
	OutboxPublish             *prometheus.CounterVec
	OutboxDead                prometheus.Counter
	InvariantViolations       *prometheus.CounterVec
	ReconciliationDivergences prometheus.Counter

	// Failure and contention signals: invariant violations reaching FAILED,
	// pendencies close to their deadline, lock contention on the reference
	// worker, and outbox claim/publish bookkeeping (ADR 0011, ADR 0013, ADR 0014).
	ReferenceFailed           prometheus.Counter
	PendingNearDeadline       prometheus.Gauge
	ReferenceLockConflicts    *prometheus.CounterVec
	OutboxClaimLost           prometheus.Counter
	OutboxBookkeepingFailures prometheus.Counter
	OutboxPublishDuration     prometheus.Histogram
	SQSOperationFailures      *prometheus.CounterVec
}

func New() *Metrics {
	r := prometheus.NewRegistry()
	f := factory{r: r}
	m := &Metrics{
		registry:                  r,
		Transactions:              f.counterVec("wager_transactions_total", "Operation outcomes recorded, by kind, status and source.", "kind", "status", "source"),
		Replays:                   f.counterVec("wager_idempotent_replays_total", "Idempotent replays, by source.", "source"),
		InboxDuplicates:           f.counter("inbox_duplicates_total", "SQS deliveries answered from the inbox."),
		LockConflicts:             f.counterVec("wallet_lock_conflicts_total", "Wallet contention surfaced to callers.", "reason"),
		Processing:                f.histogramVec("wager_processing_seconds", "Time to process an operation.", "source", "status"),
		SQSRetries:                f.counter("sqs_retries_total", "Messages returned to the queue for a retry."),
		SQSDLQ:                    f.counterVec("sqs_dlq_total", "Messages dead-lettered by the consumer, by reason.", "reason"),
		PendingReferences:         f.gauge("pending_references", "Operations waiting for a reference."),
		OutboxPending:             f.gauge("outbox_pending", "Events not yet published."),
		OutboxLag:                 f.gauge("outbox_lag_seconds", "Age of the oldest unpublished event."),
		OutboxPublish:             f.counterVec("outbox_publish_total", "Publish outcomes recorded by the relay, by result.", "result"),
		OutboxDead:                f.counter("outbox_dead_total", "Events quarantined after permanent failures."),
		InvariantViolations:       f.counterVec("invariant_violations_total", "Database invariant violations, by path.", "path"),
		ReconciliationDivergences: f.counter("reconciliation_divergences_total", "Reconciliations whose balance differs from the ledger."),

		ReferenceFailed:           f.counter("reference_failed_total", "Pending operations moved to FAILED by an invariant violation."),
		PendingNearDeadline:       f.gauge("pending_references_near_deadline", "Pending operations within 10% of the reference TTL of their deadline."),
		ReferenceLockConflicts:    f.counterVec("reference_lock_conflicts_total", "Lock timeouts and deadlocks met by the reference worker.", "reason"),
		OutboxClaimLost:           f.counter("outbox_claim_lost_total", "Relay outcomes fenced off because another relay took the claim."),
		OutboxBookkeepingFailures: f.counter("outbox_bookkeeping_failures_total", "Relay outcomes whose database bookkeeping failed."),
		OutboxPublishDuration:     f.histogram("outbox_publish_seconds", "Time spent in one broker publish."),
		SQSOperationFailures:      f.counterVec("sqs_operation_failures_total", "Consumer calls to SQS that failed, by operation.", "operation"),
	}
	m.preset()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// preset creates the series of every closed label set, so a scrape shows
// zeros instead of absent series and rate() works from the first event.
func (m *Metrics) preset() {
	statuses := []wagering.Status{wagering.PendingReference, wagering.Processed, wagering.Rejected, wagering.Failed}
	kinds := []wagering.Kind{wagering.Bet, wagering.Win, wagering.Loss, wagering.Refund, wagering.Rollback}
	for _, source := range []string{"http", "sqs", SourceResolver} {
		for _, k := range kinds {
			for _, s := range statuses {
				m.Transactions.WithLabelValues(string(k), string(s), source)
			}
		}
	}
	for _, source := range []string{"http", "sqs"} {
		m.Replays.WithLabelValues(source)
	}
	for _, reason := range []string{"lock_timeout", "unique_retry"} {
		m.LockConflicts.WithLabelValues(reason)
	}
	for _, result := range []string{"published", "retried", "quarantined"} {
		m.OutboxPublish.WithLabelValues(result)
	}
	for _, path := range []string{"sync", "async"} {
		m.InvariantViolations.WithLabelValues(path)
	}
	for _, reason := range []string{"lock_timeout", "deadlock"} {
		m.ReferenceLockConflicts.WithLabelValues(reason)
	}
	for _, op := range []string{"dlq_copy", "delete", "visibility"} {
		m.SQSOperationFailures.WithLabelValues(op)
	}
}

// PresetDLQReasons creates the sqs_dlq_total series of the consumer's codes.
func (m *Metrics) PresetDLQReasons(codes ...string) {
	for _, c := range codes {
		m.SQSDLQ.WithLabelValues(c)
	}
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

// The methods below are what adapters call. They take time.Duration and
// integers so adapters stay free of floating point (archtest).

func (m *Metrics) ObserveProcessing(source, status string, d time.Duration) {
	m.Processing.WithLabelValues(source, status).Observe(d.Seconds())
}

func (m *Metrics) Transaction(kind, status, source string) {
	m.Transactions.WithLabelValues(kind, status, source).Inc()
}

func (m *Metrics) Replay(source string, fromInbox bool) {
	m.Replays.WithLabelValues(source).Inc()
	if fromInbox {
		m.InboxDuplicates.Inc()
	}
}

func (m *Metrics) LockConflict(reason string)     { m.LockConflicts.WithLabelValues(reason).Inc() }
func (m *Metrics) InvariantViolation(path string) { m.InvariantViolations.WithLabelValues(path).Inc() }
func (m *Metrics) ReferenceLockConflict(reason string) {
	m.ReferenceLockConflicts.WithLabelValues(reason).Inc()
}

func (m *Metrics) SetOutbox(pending, oldestMillis int64) {
	m.OutboxPending.Set(float64(pending))
	m.OutboxLag.Set(float64(oldestMillis) / 1000)
}

func (m *Metrics) SetPendingReferences(n, nearDeadline int64) {
	m.PendingReferences.Set(float64(n))
	m.PendingNearDeadline.Set(float64(nearDeadline))
}

// ResolverConcluded records an operation the reference worker settled,
// expired or failed.
func (m *Metrics) ResolverConcluded(kind wagering.Kind, status wagering.Status) {
	m.Transactions.WithLabelValues(string(kind), string(status), SourceResolver).Inc()
	if status == wagering.Failed {
		m.ReferenceFailed.Inc()
	}
}

// ConsumerObserver adapts Metrics to sqsin.Observer.
type ConsumerObserver struct{ M *Metrics }

func (o ConsumerObserver) DeadLettered(code string) { o.M.SQSDLQ.WithLabelValues(code).Inc() }
func (o ConsumerObserver) Retried()                 { o.M.SQSRetries.Inc() }
func (o ConsumerObserver) DLQCopyFailed()           { o.M.SQSOperationFailures.WithLabelValues("dlq_copy").Inc() }
func (o ConsumerObserver) DeleteFailed()            { o.M.SQSOperationFailures.WithLabelValues("delete").Inc() }
func (o ConsumerObserver) VisibilityFailed() {
	o.M.SQSOperationFailures.WithLabelValues("visibility").Inc()
}

// RelayObserver adapts Metrics to outbox.Observer.
type RelayObserver struct{ M *Metrics }

func (o RelayObserver) Published() { o.M.OutboxPublish.WithLabelValues("published").Inc() }
func (o RelayObserver) Retried()   { o.M.OutboxPublish.WithLabelValues("retried").Inc() }
func (o RelayObserver) Quarantined() {
	o.M.OutboxPublish.WithLabelValues("quarantined").Inc()
	o.M.OutboxDead.Inc()
}
func (o RelayObserver) ClaimLost()         { o.M.OutboxClaimLost.Inc() }
func (o RelayObserver) BookkeepingFailed() { o.M.OutboxBookkeepingFailures.Inc() }
func (o RelayObserver) PublishDuration(d time.Duration) {
	o.M.OutboxPublishDuration.Observe(d.Seconds())
}

type factory struct{ r *prometheus.Registry }

func (f factory) counter(name, help string) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	f.r.MustRegister(c)
	return c
}

func (f factory) counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	f.r.MustRegister(c)
	return c
}

func (f factory) gauge(name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	f.r.MustRegister(g)
	return g
}

func (f factory) histogram(name, help string) prometheus.Histogram {
	h := prometheus.NewHistogram(prometheus.HistogramOpts{Name: name, Help: help, Buckets: prometheus.DefBuckets})
	f.r.MustRegister(h)
	return h
}

func (f factory) histogramVec(name, help string, labels ...string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: prometheus.DefBuckets}, labels)
	f.r.MustRegister(h)
	return h
}
