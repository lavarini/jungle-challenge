// Command evidence renders docs/EVIDENCIAS.md from `go test -json` streams
// and the requirement map (ADR 0018). It is not edited by hand.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type requirement struct {
	ID    string   `json:"id"`
	Text  string   `json:"requisito"`
	Tests []string `json:"testes"`
}

// testKey identifies one test by where it ran, not just its name: the same
// test name can appear in several packages (unit, integration, e2e, crash),
// and a failure in any one of them must not be masked by a pass in another.
type testKey struct {
	Package, Test string
}

type result struct {
	status  string
	elapsed string
}

type header struct {
	Commit, GoVersion, Date string
	// PackageFailures lists packages that failed at the package level (a
	// build failure or a TestMain that died before any test event was
	// emitted). Every test the requirement map expects from such a package
	// never gets a pass/fail/skip event of its own, so it already renders as
	// "não executado" (a gap) in the table below; this list exists only so a
	// reader can see *why*, instead of guessing between a renamed test and a
	// broken package.
	PackageFailures []string
}

// summarize keeps the final status of each top-level test, keyed by
// package+test, plus the set of packages that failed at the package level
// (an Action:"fail" event with no Test name: TestMain died, or the package
// failed to build).
func summarize(r io.Reader) (map[testKey]result, []string, error) {
	out := map[testKey]result{}
	pkgFailures := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var ev struct {
			Action  string
			Package string
			Test    string
			Elapsed json.Number
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Test == "" {
			if ev.Action == "fail" {
				pkgFailures[ev.Package] = true
			}
			continue
		}
		if strings.Contains(ev.Test, "/") {
			continue // subtests: a parent already fails when a subtest fails
		}
		switch ev.Action {
		case "pass", "fail", "skip":
			out[testKey{Package: ev.Package, Test: ev.Test}] = result{status: ev.Action, elapsed: ev.Elapsed.String()}
		}
	}
	var failures []string
	for pkg := range pkgFailures {
		failures = append(failures, pkg)
	}
	sort.Strings(failures)
	return out, failures, sc.Err()
}

// matchesFor returns every (package, result) pair recorded for a bare test
// name, across every package it ran in, sorted by package for determinism.
func matchesFor(results map[testKey]result, name string) []struct {
	key testKey
	res result
} {
	var matches []struct {
		key testKey
		res result
	}
	for k, v := range results {
		if k.Test == name {
			matches = append(matches, struct {
				key testKey
				res result
			}{k, v})
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].key.Package < matches[j].key.Package })
	return matches
}

func cellsFor(name string, matches []struct {
	key testKey
	res result
}) []string {
	if len(matches) == 1 {
		m := matches[0]
		if m.res.status == "pass" {
			return []string{fmt.Sprintf("%s (%ss)", name, m.res.elapsed)}
		}
		return []string{fmt.Sprintf("%s (%s)", name, m.res.status)}
	}
	cells := make([]string, 0, len(matches))
	for _, m := range matches {
		if m.res.status == "pass" {
			cells = append(cells, fmt.Sprintf("%s [%s] (%ss)", name, m.key.Package, m.res.elapsed))
		} else {
			cells = append(cells, fmt.Sprintf("%s [%s] (%s)", name, m.key.Package, m.res.status))
		}
	}
	return cells
}

// render turns the requirement map and the merged test results into the
// evidence table. A requirement is ✅ only when every one of its listed
// tests passed in this run, in every package it appeared in. A listed test
// that never ran (never emitted a pass/fail/skip event, whether because it
// was renamed, its package failed to build, or TestMain died) or that was
// skipped makes the requirement a gap (⚠️), never a false ✅. ❌ is reserved
// for a listed test that actually failed somewhere.
func render(reqs []requirement, results map[testKey]result, h header) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Evidências\n\n> Gerado por `make evidence` a partir de `go test -json`. Não editar à mão.\n\n")
	fmt.Fprintf(&b, "Commit `%s` · %s · %s\n\n", h.Commit, h.GoVersion, h.Date)
	if len(h.PackageFailures) > 0 {
		fmt.Fprintf(&b, "**Pacotes que falharam ao construir ou iniciar (TestMain):** %s\n\n", strings.Join(h.PackageFailures, ", "))
	}
	fmt.Fprintf(&b, "| Req. | Requisito | Situação | Testes |\n|---|---|---|---|\n")
	for _, req := range reqs {
		status := "✅"
		gap := false
		var cells []string
		for _, name := range req.Tests {
			matches := matchesFor(results, name)
			if len(matches) == 0 {
				cells = append(cells, name+" (não executado)")
				gap = true
				continue
			}
			failed, skipped := false, false
			for _, m := range matches {
				switch m.res.status {
				case "fail":
					failed = true
				case "skip":
					skipped = true
				}
			}
			cells = append(cells, cellsFor(name, matches)...)
			switch {
			case failed:
				status = "❌"
			case skipped:
				gap = true
			}
		}
		if status != "❌" && gap {
			status = "⚠️ lacuna"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", req.ID, req.Text, status, strings.Join(cells, "<br>"))
	}
	return b.String()
}

func main() {
	reqPath := flag.String("requirements", "docs/evidencias/requisitos.json", "requirement map")
	outPath := flag.String("out", "docs/EVIDENCIAS.md", "output file")
	commit := flag.String("commit", "", "commit sha")
	goVersion := flag.String("go", "", "go version")
	date := flag.String("date", "", "run date")
	flag.Parse()

	raw, err := os.ReadFile(*reqPath)
	if err != nil {
		fail(err)
	}
	var reqs []requirement
	if err := json.Unmarshal(raw, &reqs); err != nil {
		fail(err)
	}
	results := map[testKey]result{}
	pkgFailures := map[string]bool{}
	for _, path := range flag.Args() {
		f, err := os.Open(path)
		if err != nil {
			fail(err)
		}
		part, failures, err := summarize(f)
		f.Close()
		if err != nil {
			fail(err)
		}
		for k, v := range part {
			results[k] = v
		}
		for _, pkg := range failures {
			pkgFailures[pkg] = true
		}
	}
	var failures []string
	for pkg := range pkgFailures {
		failures = append(failures, pkg)
	}
	sort.Strings(failures)
	md := render(reqs, results, header{Commit: *commit, GoVersion: *goVersion, Date: *date, PackageFailures: failures})
	if err := os.WriteFile(*outPath, []byte(md), 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "evidence:", err)
	os.Exit(1)
}
