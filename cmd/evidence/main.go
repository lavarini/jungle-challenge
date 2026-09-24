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
	"strings"
)

type requirement struct {
	ID    string   `json:"id"`
	Text  string   `json:"requisito"`
	Tests []string `json:"testes"`
}

type result struct {
	status  string
	elapsed string
}

type header struct {
	Commit, GoVersion, Date string
}

// summarize keeps the final status of each top-level test.
func summarize(r io.Reader) (map[string]result, error) {
	out := map[string]result{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var ev struct {
			Action  string
			Test    string
			Elapsed json.Number
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Test == "" || strings.Contains(ev.Test, "/") {
			continue
		}
		switch ev.Action {
		case "pass", "fail", "skip":
			out[ev.Test] = result{status: ev.Action, elapsed: ev.Elapsed.String()}
		}
	}
	return out, sc.Err()
}

func render(reqs []requirement, results map[string]result, h header) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Evidências\n\n> Gerado por `make evidence` a partir de `go test -json`. Não editar à mão.\n\n")
	fmt.Fprintf(&b, "Commit `%s` · %s · %s\n\n", h.Commit, h.GoVersion, h.Date)
	fmt.Fprintf(&b, "| Req. | Requisito | Situação | Testes |\n|---|---|---|---|\n")
	for _, req := range reqs {
		status := "✅"
		var cells []string
		passed := 0
		for _, name := range req.Tests {
			res, ok := results[name]
			switch {
			case !ok:
				cells = append(cells, name+" (não executado)")
			case res.status == "pass":
				passed++
				cells = append(cells, fmt.Sprintf("%s (%ss)", name, res.elapsed))
			default:
				status = "❌"
				cells = append(cells, fmt.Sprintf("%s (%s)", name, res.status))
			}
		}
		if status != "❌" && passed == 0 {
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
	results := map[string]result{}
	for _, path := range flag.Args() {
		f, err := os.Open(path)
		if err != nil {
			fail(err)
		}
		part, err := summarize(f)
		f.Close()
		if err != nil {
			fail(err)
		}
		for k, v := range part {
			results[k] = v
		}
	}
	md := render(reqs, results, header{Commit: *commit, GoVersion: *goVersion, Date: *date})
	if err := os.WriteFile(*outPath, []byte(md), 0o644); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "evidence:", err)
	os.Exit(1)
}
