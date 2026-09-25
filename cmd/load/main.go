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

	currency      = "BRL"
	walletBalance = "100000.00"
	betAmount     = "1.00"
	roundID       = "round-1"
	gameID        = "load-game"

	outboxDrainTimeout = 60 * time.Second
	outboxDrainPoll    = 500 * time.Millisecond
)

type flags struct {
	apis        []string
	kc          string
	duration    time.Duration
	concurrency int
	wallets     int
	hot         float64
	dup         float64
	db          string
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
	db := flag.String("db", "postgres://wager_app:app-dev-only@localhost:5432/wagering?sslmode=disable",
		"DSN used to measure the outbox (wager_app role; falls back to postgres-dev-only if it lacks SELECT)")
	flag.Parse()

	return flags{
		apis:        strings.Split(*apiList, ","),
		kc:          *kc,
		duration:    *duration,
		concurrency: *concurrency,
		wallets:     *wallets,
		hot:         *hot,
		dup:         *dup,
		db:          *db,
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

	log.Printf("waiting for the outbox to drain (limit %s)", outboxDrainTimeout)
	pool, err := pgxpool.New(ctx, cfg.db)
	if err != nil {
		return fmt.Errorf("outbox db pool: %w", err)
	}
	defer pool.Close()

	drainOK, drainRemaining, err := waitOutboxDrained(ctx, pool, outboxDrainTimeout)
	if err != nil {
		return fmt.Errorf("outbox drain check: %w", err)
	}
	outbox, err := outboxDelays(ctx, pool, loadStart)
	if err != nil {
		return fmt.Errorf("outbox delay query: %w", err)
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
	writeReport(out, env, elapsed, stats, statusCounts, rawStatusCounts, transportErrors, drainOK, drainRemaining, outbox, recon)

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
// Each iteration round-robins across cfg.apis and either replays the worker's last operation
// (probability cfg.dup) or submits a new BET against the hot wallet (probability cfg.hot) or a
// uniformly chosen non-hot wallet.
func runLoad(cfg flags, providerTok string, wallets []wallet) [][]opResult {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.duration)
	defer cancel()

	var apiIdx atomic.Uint64
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
				if haveLast && rng.Float64() < cfg.dup {
					body, idemKey = lastBody, lastKey
				} else {
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
				}

				start := time.Now()
				status, err := submitBet(ctx, api, providerTok, idemKey, body)
				local = append(local, opResult{status: status, transportErr: err != nil, latency: time.Since(start)})
			}
			results[id] = local
		}(id)
	}
	wg.Wait()
	return results
}

// waitOutboxDrained polls until no outbox row is left unpublished and un-dead, or until timeout.
func waitOutboxDrained(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration) (drained bool, remaining int, err error) {
	deadline := time.Now().Add(timeout)
	for {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND dead_at IS NULL`).Scan(&n); err != nil {
			return false, 0, err
		}
		if n == 0 {
			return true, 0, nil
		}
		if time.Now().After(deadline) {
			return false, n, nil
		}
		time.Sleep(outboxDrainPoll)
	}
}

// outboxDelays reads p50/p95/p99 of the publish delay for events occurred since the load started.
// Each percentile is nil when the aggregate had no matching rows (e.g. nothing occurred yet).
func outboxDelays(ctx context.Context, pool *pgxpool.Pool, since time.Time) (outboxDelayReport, error) {
	var seconds []*float64
	err := pool.QueryRow(ctx, `SELECT percentile_cont(ARRAY[0.5,0.95,0.99]) WITHIN GROUP (ORDER BY extract(epoch FROM published_at - occurred_at))
		FROM outbox_events WHERE occurred_at >= $1`, since).Scan(&seconds)
	if err != nil {
		return outboxDelayReport{}, err
	}
	if len(seconds) != 3 {
		return outboxDelayReport{}, nil
	}
	return outboxDelayReport{p50: seconds[0], p95: seconds[1], p99: seconds[2]}, nil
}
