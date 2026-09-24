package main

import (
	"strings"
	"testing"
)

func TestSummarizeAndRender(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"pass","Package":"p","Test":"TestA","Elapsed":0.5}`,
		`{"Action":"fail","Package":"p","Test":"TestB","Elapsed":1.25}`,
		`{"Action":"pass","Package":"p","Test":"TestB/sub","Elapsed":0.1}`,
		`{"Action":"output","Package":"p","Test":"TestA","Output":"noise"}`,
	}, "\n")
	results, err := summarize(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if results["TestA"].status != "pass" || results["TestB"].status != "fail" {
		t.Fatalf("results %+v", results)
	}
	reqs := []requirement{
		{ID: "1", Text: "covered", Tests: []string{"TestA"}},
		{ID: "2", Text: "broken", Tests: []string{"TestB"}},
		{ID: "3", Text: "missing", Tests: []string{"TestZ"}},
	}
	md := render(reqs, results, header{Commit: "abc1234", GoVersion: "go1.27.1", Date: "2026-09-26"})
	for _, want := range []string{"abc1234", "| 1 | covered | ✅", "| 2 | broken | ❌", "| 3 | missing | ⚠️ lacuna", "TestZ (não executado)"} {
		if !strings.Contains(md, want) {
			t.Errorf("output lacks %q:\n%s", want, md)
		}
	}
}
