package main

import (
	"fmt"
	"io"
	"math"
	"sort"
	"time"
)

// percentile uses nearest rank over an ascending slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	return sorted[i]
}

// latencyStats holds the percentiles and max of a sorted latency sample.
type latencyStats struct {
	count int
	p50   time.Duration
	p95   time.Duration
	p99   time.Duration
	max   time.Duration
}

// computeLatencyStats sorts latencies in place and derives the summary used in the report.
func computeLatencyStats(latencies []time.Duration) latencyStats {
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var max time.Duration
	if len(latencies) > 0 {
		max = latencies[len(latencies)-1]
	}
	return latencyStats{
		count: len(latencies),
		p50:   percentile(latencies, 50),
		p95:   percentile(latencies, 95),
		p99:   percentile(latencies, 99),
		max:   max,
	}
}

// fmtMillis renders a duration in milliseconds with one decimal, matching the report's tables.
func fmtMillis(d time.Duration) string {
	return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000)
}

// fmtSecondsPtr renders an optional seconds value (nil when the outbox had no matching rows).
func fmtSecondsPtr(seconds *float64) string {
	if seconds == nil {
		return "sem dados"
	}
	return fmt.Sprintf("%.3f s", *seconds)
}

// environment describes the machine and flags a run was executed with, for the report header.
type environment struct {
	numCPU int
	goos   string
	goarch string
	cfg    flags
}

// outboxReport holds, for events that occurred since the load started: how many there are, how
// many were published, and the three publish-delay percentiles (nil when nothing was published
// yet). published < total means the percentiles are right-censored - see writeReport's Outbox
// section, which says so explicitly whenever that happens.
type outboxReport struct {
	total, published int
	p50, p95, p99    *float64
}

// reconciliationSummary counts how many opened wallets reconciled cleanly.
type reconciliationSummary struct {
	checked      int
	inconsistent []string // wallet IDs with consistent=false
	failed       []string // wallet IDs where the reconciliation call itself failed
}

// writeReport renders the full Markdown report described in docs/CARGA.md's methodology section:
// environment, throughput, latency percentiles, status counts, outbox delay and reconciliation.
func writeReport(w io.Writer, env environment, elapsed time.Duration, stats latencyStats, statusCounts map[string]int, rawStatusCounts map[int]int, transportErrors int, drainOK bool, outbox outboxReport, recon reconciliationSummary) {
	fmt.Fprintf(w, "# Relatório de carga\n\n")

	fmt.Fprintf(w, "## Ambiente\n\n")
	fmt.Fprintf(w, "- CPUs: %d\n", env.numCPU)
	fmt.Fprintf(w, "- SO/Arquitetura: %s/%s\n", env.goos, env.goarch)
	fmt.Fprintf(w, "- APIs: %s\n", joinComma(env.cfg.apis))
	fmt.Fprintf(w, "- Keycloak: %s\n", env.cfg.kc)
	fmt.Fprintf(w, "- Duração: %s\n", env.cfg.duration)
	fmt.Fprintf(w, "- Concorrência: %d\n", env.cfg.concurrency)
	fmt.Fprintf(w, "- Carteiras: %d\n", env.cfg.wallets)
	fmt.Fprintf(w, "- Fração quente (-hot): %.2f\n", env.cfg.hot)
	fmt.Fprintf(w, "- Fração de replay (-dup): %.2f\n", env.cfg.dup)
	fmt.Fprintf(w, "- Fração de conflito (-conflict): %.2f\n", env.cfg.conflict)
	fmt.Fprintf(w, "- DSN da outbox (-db): %s\n", env.cfg.db)
	fmt.Fprintf(w, "- Limite de espera da outbox (-drain-timeout): %s\n\n", env.cfg.drainTimeout)

	total := stats.count + transportErrors
	throughput := float64(total) / elapsed.Seconds()
	fmt.Fprintf(w, "## Resultado\n\n")
	fmt.Fprintf(w, "- Total de requisições: %d\n", total)
	fmt.Fprintf(w, "- Duração efetiva: %s\n", elapsed)
	fmt.Fprintf(w, "- Throughput: %.1f req/s\n\n", throughput)

	fmt.Fprintf(w, "| Métrica | Valor |\n|---|---|\n")
	fmt.Fprintf(w, "| p50 | %s |\n", fmtMillis(stats.p50))
	fmt.Fprintf(w, "| p95 | %s |\n", fmtMillis(stats.p95))
	fmt.Fprintf(w, "| p99 | %s |\n", fmtMillis(stats.p99))
	fmt.Fprintf(w, "| max | %s |\n\n", fmtMillis(stats.max))

	fmt.Fprintf(w, "| Status | Contagem |\n|---|---|\n")
	for _, key := range []string{"201 novo", "200 replay", "409 conflito", "422", "5xx", "outros HTTP", "erro de transporte"} {
		fmt.Fprintf(w, "| %s | %d |\n", key, statusCounts[key])
	}
	fmt.Fprintf(w, "\n")

	if len(rawStatusCounts) > 0 {
		fmt.Fprintf(w, "Contagem bruta por código HTTP: ")
		codes := make([]int, 0, len(rawStatusCounts))
		for c := range rawStatusCounts {
			codes = append(codes, c)
		}
		sort.Ints(codes)
		for i, c := range codes {
			if i > 0 {
				fmt.Fprintf(w, ", ")
			}
			fmt.Fprintf(w, "%d=%d", c, rawStatusCounts[c])
		}
		fmt.Fprintf(w, "\n\n")
	}

	pending := outbox.total - outbox.published
	fmt.Fprintf(w, "## Outbox\n\n")
	fmt.Fprintf(w, "- Eventos observados desde o início da carga (`occurred_at >= início`): %d\n", outbox.total)
	fmt.Fprintf(w, "- Publicados: %d\n", outbox.published)
	if drainOK && pending == 0 {
		fmt.Fprintf(w, "- Drenou completamente dentro do limite de espera (%s).\n", env.cfg.drainTimeout)
		fmt.Fprintf(w, "- Os percentis abaixo cobrem todos os %d eventos.\n\n", outbox.total)
	} else {
		fmt.Fprintf(w, "- **Não drenou** dentro do limite de espera (%s): %d evento(s) ainda pendentes.\n", env.cfg.drainTimeout, pending)
		fmt.Fprintf(w, "- Os percentis abaixo cobrem só os %d já publicados; percentile_cont ignora os pendentes, então são "+
			"right-censored — a cauda real do atraso é maior do que estes números.\n\n", outbox.published)
	}
	fmt.Fprintf(w, "| Percentil | Atraso (occurred_at -> published_at) |\n|---|---|\n")
	fmt.Fprintf(w, "| p50 | %s |\n", fmtSecondsPtr(outbox.p50))
	fmt.Fprintf(w, "| p95 | %s |\n", fmtSecondsPtr(outbox.p95))
	fmt.Fprintf(w, "| p99 | %s |\n\n", fmtSecondsPtr(outbox.p99))

	fmt.Fprintf(w, "## Reconciliação\n\n")
	fmt.Fprintf(w, "- Carteiras verificadas: %d\n", recon.checked)
	if len(recon.failed) > 0 {
		fmt.Fprintf(w, "- **Falha ao chamar a reconciliação** em %d carteira(s): %s\n", len(recon.failed), joinComma(recon.failed))
	}
	if len(recon.inconsistent) > 0 {
		fmt.Fprintf(w, "- **DIVERGÊNCIA**: %d carteira(s) com consistent=false: %s\n", len(recon.inconsistent), joinComma(recon.inconsistent))
	} else if len(recon.failed) == 0 {
		fmt.Fprintf(w, "- Todas as carteiras reconciliaram com consistent=true.\n")
	}
}

func joinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
