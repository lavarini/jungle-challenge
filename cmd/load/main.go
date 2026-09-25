// Command load drives concurrent traffic against a running wagering Compose stack and reports
// throughput, latency percentiles, status counts, outbox delay and a reconciliation check.
//
// It reuses the same client_credentials flow as scripts/smoke.sh and the same request/response
// shapes as test/e2e, but talks to an already-running stack instead of starting its own.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Dev-only credentials from deploy/keycloak/realm-wagering.json, the same ones scripts/smoke.sh
// and test/testenv use. Not a secret worth protecting: it only exists in the local Compose stack.
const (
	providerClientID = "provider-a"
	providerSecret   = "provider-a-dev-secret"
	internalClientID = "wallet-internal"
	internalSecret   = "wallet-internal-dev-secret"

	currency       = "BRL"
	walletBalance  = "100000.00"
	betAmount      = "1.00"
	conflictAmount = "2.00" // any value different from betAmount; see withConflictingPayload
	roundID        = "round-1"
	gameID         = "load-game"

	outboxDrainPoll = 500 * time.Millisecond

	// recentOpsCapacity bounds the shared ring buffer that -conflict picks a target from.
	recentOpsCapacity = 512
)

type flags struct {
	apis         []string
	kc           string
	duration     time.Duration
	concurrency  int
	wallets      int
	hot          float64
	dup          float64
	conflict     float64
	db           string
	drainTimeout time.Duration
}

func parseFlags() flags {
	apiList := flag.String("api", "http://localhost:8080,http://localhost:8082,http://localhost:8083",
		"comma-separated list of API base URLs, used round-robin")
	kc := flag.String("kc", "http://localhost:8081/realms/wagering", "Keycloak realm issuer URL")
	duration := flag.Duration("duration", 60*time.Second, "load duration")
	concurrency := flag.Int("concurrency", 32, "number of concurrent worker goroutines")
	wallets := flag.Int("wallets", 50, "number of wallets to open before the load; the first is the hot wallet")
	hot := flag.Float64("hot", 0.2, "fraction of requests targeting the hot wallet")
	dup := flag.Float64("dup", 0.1, "fraction of requests that replay the worker's last operation with the same Idempotency-Key")
	conflict := flag.Float64("conflict", 0.02, "fraction of requests that reuse another worker's recent Idempotency-Key with a different payload (exercises 409 IDEMPOTENCY_PAYLOAD_MISMATCH)")
	db := flag.String("db", "postgres://wager_app:app-dev-only@localhost:5432/wagering?sslmode=disable",
		"DSN used to measure the outbox (wager_app role)")
	drainTimeout := flag.Duration("drain-timeout", 10*time.Minute, "how long to wait for the outbox to fully drain before measuring delay")
	flag.Parse()

	return flags{
		apis:         strings.Split(*apiList, ","),
		kc:           *kc,
		duration:     *duration,
		concurrency:  *concurrency,
		wallets:      *wallets,
		hot:          *hot,
		dup:          *dup,
		conflict:     *conflict,
		db:           *db,
		drainTimeout: *drainTimeout,
	}
}

type opResult struct {
	status       int
	transportErr bool
	latency      time.Duration
}

func main() {
	cfg := parseFlags()
	if err := run(cfg, os.Stdout); err != nil {
		log.Fatalf("load: %v", err)
	}
}

func run(cfg flags, out *os.File) error {
	ctx := context.Background()

	log.Printf("fetching tokens from %s", cfg.kc)
	providerTok, err := fetchToken(ctx, cfg.kc, providerClientID, providerSecret)
	if err != nil {
		return fmt.Errorf("provider token: %w", err)
	}
	internalTok, err := fetchToken(ctx, cfg.kc, internalClientID, internalSecret)
	if err != nil {
		return fmt.Errorf("internal token: %w", err)
	}

	setupAPI := cfg.apis[0]
	log.Printf("opening %d wallets with %s %s via %s", cfg.wallets, walletBalance, currency, setupAPI)
	wallets := make([]wallet, 0, cfg.wallets)
	for i := 0; i < cfg.wallets; i++ {
		w, err := openWallet(ctx, setupAPI, internalTok, uuid.NewString(), walletBalance)
		if err != nil {
			return fmt.Errorf("open wallet %d: %w", i, err)
		}
		wallets = append(wallets, w)
	}

	log.Printf("running %d workers for %s (hot=%.2f dup=%.2f) across %v", cfg.concurrency, cfg.duration, cfg.hot, cfg.dup, cfg.apis)
	loadStart := time.Now()
	results := runLoad(cfg, providerTok, wallets)
	elapsed := time.Since(loadStart)

	latencies := make([]time.Duration, 0)
	statusCounts := map[string]int{}
	rawStatusCounts := map[int]int{}
	transportErrors := 0
	for _, worker := range results {
		for _, r := range worker {
			if r.transportErr {
				transportErrors++
				statusCounts["erro de transporte"]++
				continue
			}
			latencies = append(latencies, r.latency)
			rawStatusCounts[r.status]++
			statusCounts[categorize(r.status)]++
		}
	}
	stats := computeLatencyStats(latencies)

	log.Printf("waiting for the outbox to drain (limit %s)", cfg.drainTimeout)
	pool, err := pgxpool.New(ctx, cfg.db)
	if err != nil {
		return fmt.Errorf("outbox db pool: %w", err)
	}
	defer pool.Close()

	drainOK, err := waitOutboxDrained(ctx, pool, cfg.drainTimeout)
	if err != nil {
		return fmt.Errorf("outbox drain check: %w", err)
	}
	log.Printf("outbox drain check (global, pre-measurement): drained=%v", drainOK)
	outbox, err := measureOutbox(ctx, pool, loadStart)
	if err != nil {
		return fmt.Errorf("outbox measurement: %w", err)
	}

	log.Printf("reconciling %d wallets", len(wallets))
	recon := reconciliationSummary{checked: len(wallets)}
	for _, w := range wallets {
		r, err := reconcileWallet(ctx, setupAPI, internalTok, w.ID)
		if err != nil {
			recon.failed = append(recon.failed, w.ID)
			continue
		}
		if !r.Consistent {
			recon.inconsistent = append(recon.inconsistent, w.ID)
		}
	}

	env := environment{numCPU: runtime.NumCPU(), goos: runtime.GOOS, goarch: runtime.GOARCH, cfg: cfg}
	writeReport(out, env, elapsed, stats, statusCounts, rawStatusCounts, transportErrors, outbox, recon)

	if len(recon.inconsistent) > 0 || len(recon.failed) > 0 {
		return fmt.Errorf("reconciliation found %d divergence(s) and %d failed call(s); see the report above",
			len(recon.inconsistent), len(recon.failed))
	}
	return nil
}

// categorize buckets an HTTP status into the report's rows. Anything outside the contract's
// documented outcomes for POST /wagering/transactions (docs/openapi.yaml) lands in "outros HTTP"
// and is still visible in the raw per-code breakdown - nothing is silently dropped.
func categorize(status int) string {
	switch {
	case status == http.StatusCreated:
		return "201 novo"
	case status == http.StatusOK:
		return "200 replay"
	case status == http.StatusConflict:
		return "409 conflito"
	case status == http.StatusUnprocessableEntity:
		return "422"
	case status >= 500:
		return "5xx"
	default:
		return "outros HTTP"
	}
}

// runLoad fans out cfg.concurrency workers for cfg.duration and returns each worker's results.
// Each iteration round-robins across cfg.apis and picks one of three paths, checked in order:
//  1. probability cfg.dup: replay the worker's own last operation (same key, same payload; 200).
//  2. probability cfg.conflict: reuse a recent operation from the shared recent pool (possibly
//     submitted by another worker) with a different payload (same key, different hash; 409
//     IDEMPOTENCY_PAYLOAD_MISMATCH).
//  3. otherwise: submit a new BET against the hot wallet (probability cfg.hot) or a uniformly
//     chosen non-hot wallet; once the server confirms it with 201, record it in recent so other
//     workers' future conflicts always target an operation that has already committed.
func runLoad(cfg flags, providerTok string, wallets []wallet) [][]opResult {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()

	var apiIdx atomic.Uint64
	recent := newRecentOps(recentOpsCapacity)
	results := make([][]opResult, cfg.concurrency)
	var wg sync.WaitGroup
	for id := 0; id < cfg.concurrency; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(id)+1, uint64(time.Now().UnixNano())))
			var local []opResult
			var lastBody submitRequest
			var lastKey string
			haveLast := false

			for ctx.Err() == nil {
				api := cfg.apis[apiIdx.Add(1)%uint64(len(cfg.apis))]

				var body submitRequest
				var idemKey string
				isNew := false
				switch {
				case haveLast && rng.Float64() < cfg.dup:
					body, idemKey = lastBody, lastKey
				case rng.Float64() < cfg.conflict:
					if target, ok := recent.pick(rng); ok {
						idemKey = target.idemKey
						body = withConflictingPayload(target.body)
						break
					}
					fallthrough
				default:
					wIdx := 0
					if len(wallets) > 1 && rng.Float64() >= cfg.hot {
						wIdx = 1 + rng.IntN(len(wallets)-1)
					}
					w := wallets[wIdx]
					ext := "load-" + uuid.NewString()
					idemKey = providerClientID + ":" + ext
					body = submitRequest{
						ProviderID: providerClientID, ExternalTransactionID: ext,
						PlayerID: w.PlayerID, WalletID: w.ID,
						RoundID: roundID, GameID: gameID, Kind: "BET",
						Money: money{Amount: betAmount, Currency: currency},
					}
					lastBody, lastKey, haveLast = body, idemKey, true
					isNew = true
				}

				start := time.Now()
				status, err := submitBet(ctx, api, providerTok, idemKey, body)
				local = append(local, opResult{status: status, transportErr: err != nil, latency: time.Since(start)})
				// Only publish to the shared pool once the server has confirmed this operation was
				// processed (201): a future -conflict pick must always race against an operation
				// that has already committed, never one still in flight, or the 409 could land on
				// either side of the race depending on which request the server saw first.
				if isNew && status == http.StatusCreated {
					recent.add(recentOp{idemKey: idemKey, body: body})
				}
			}
			results[id] = local
		}(id)
	}
	wg.Wait()
	return results
}

// waitOutboxDrained polls the global (partial-index-backed) unpublished count until it reaches
// zero or until timeout. With a fresh stack per run (docs/CARGA.md's reproducible commands), this
// is equivalent to scoping by the load's own occurred_at window, at a fraction of the query cost.
func waitOutboxDrained(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration) (drained bool, err error) {
	deadline := time.Now().Add(timeout)
	for {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND dead_at IS NULL`).Scan(&n); err != nil {
			return false, err
		}
		if n == 0 {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(outboxDrainPoll)
	}
}

// measureOutbox reads, for events that occurred since the load started: how many exist, how many
// were published, how many were dead-lettered, and the p50/p95/p99 publish delay. published and
// dead are mutually exclusive outcomes (a row's published_at and dead_at are never both set), so
// total - published - dead is the count still genuinely in flight - the only rows that make the
// percentiles right-censored. percentile_cont ignores NULL inputs, so rows still pending
// (published_at IS NULL) are excluded from the percentiles by construction - the
// AND published_at IS NOT NULL below is redundant but documents that on purpose.
func measureOutbox(ctx context.Context, pool *pgxpool.Pool, since time.Time) (outboxReport, error) {
	var rep outboxReport
	if err := pool.QueryRow(ctx, `SELECT count(*), count(published_at), count(dead_at) FROM outbox_events WHERE occurred_at >= $1`,
		since).Scan(&rep.total, &rep.published, &rep.dead); err != nil {
		return outboxReport{}, err
	}
	var seconds []*float64
	err := pool.QueryRow(ctx, `SELECT percentile_cont(ARRAY[0.5,0.95,0.99]) WITHIN GROUP (ORDER BY extract(epoch FROM published_at - occurred_at))
		FROM outbox_events WHERE occurred_at >= $1 AND published_at IS NOT NULL`, since).Scan(&seconds)
	if err != nil {
		return outboxReport{}, err
	}
	if len(seconds) == 3 {
		rep.p50, rep.p95, rep.p99 = seconds[0], seconds[1], seconds[2]
	}
	return rep, nil
}
