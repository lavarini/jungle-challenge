package main

import (
	"strings"
	"testing"
)

func TestSummarizeKeysByPackageAndTest(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"pass","Package":"p","Test":"TestA","Elapsed":0.5}`,
		`{"Action":"fail","Package":"p","Test":"TestB","Elapsed":1.25}`,
		`{"Action":"pass","Package":"p","Test":"TestB/sub","Elapsed":0.1}`,
		`{"Action":"output","Package":"p","Test":"TestA","Output":"noise"}`,
	}, "\n")
	results, pkgFailures, err := summarize(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgFailures) != 0 {
		t.Fatalf("pkgFailures = %v, want none", pkgFailures)
	}
	if results[testKey{"p", "TestA"}].status != "pass" || results[testKey{"p", "TestB"}].status != "fail" {
		t.Fatalf("results %+v", results)
	}
	if _, ok := results[testKey{"p", "TestB/sub"}]; ok {
		t.Fatalf("subtests must not be recorded on their own: %+v", results)
	}
}

// TestAllPass covers the case where every listed test passed: the
// requirement must render ✅, never a gap.
func TestAllPass(t *testing.T) {
	stream := `{"Action":"pass","Package":"p","Test":"TestA","Elapsed":0.5}`
	results, _, err := summarize(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	reqs := []requirement{{ID: "1", Text: "covered", Tests: []string{"TestA"}}}
	md := render(reqs, results, header{Commit: "abc1234"})
	if !strings.Contains(md, "| 1 | covered | ✅") {
		t.Errorf("want ✅ for an all-pass requirement:\n%s", md)
	}
}

// TestMissingTestIsAGap covers a listed test that never emitted any event
// (renamed, or simply not run): it must be a gap, never a silent ✅.
func TestMissingTestIsAGap(t *testing.T) {
	results, _, err := summarize(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	reqs := []requirement{{ID: "3", Text: "missing", Tests: []string{"TestZ"}}}
	md := render(reqs, results, header{Commit: "abc1234"})
	if !strings.Contains(md, "| 3 | missing | ⚠️ lacuna") || !strings.Contains(md, "TestZ (não executado)") {
		t.Errorf("want a gap for a missing test:\n%s", md)
	}
}

// TestSkipIsAGapNotAFailure covers M1: a skipped test is a gap (⚠️), not a
// false ❌ and not a silent ✅.
func TestSkipIsAGapNotAFailure(t *testing.T) {
	stream := `{"Action":"skip","Package":"p","Test":"TestSkipped","Elapsed":0}`
	results, _, err := summarize(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	reqs := []requirement{{ID: "9", Text: "skipped one", Tests: []string{"TestSkipped"}}}
	md := render(reqs, results, header{Commit: "abc1234"})
	if !strings.Contains(md, "| 9 | skipped one | ⚠️ lacuna") {
		t.Errorf("want a gap for a skipped test:\n%s", md)
	}
	if strings.Contains(md, "❌") {
		t.Errorf("a skip must never render as a failure:\n%s", md)
	}
}

// TestPackageLevelFailureWithNoTestEventsIsAGap covers C1's main scenario:
// TestMain dies (or the package fails to build) and the stream carries only
// a package-level fail event, no test-level event at all. Every requirement
// test expected from that package must still render as a gap, and the
// failed package must be named in the header so a reader knows why.
func TestPackageLevelFailureWithNoTestEventsIsAGap(t *testing.T) {
	stream := `{"Action":"fail","Package":"test/e2e/crash"}`
	results, pkgFailures, err := summarize(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("no test-level results expected: %+v", results)
	}
	if len(pkgFailures) != 1 || pkgFailures[0] != "test/e2e/crash" {
		t.Fatalf("pkgFailures = %v, want [test/e2e/crash]", pkgFailures)
	}
	reqs := []requirement{{ID: "5.4", Text: "crash proves its window", Tests: []string{"TestRelayCrashAfterPublishIsRepublishedWithTheSameEventID"}}}
	md := render(reqs, results, header{Commit: "abc1234", PackageFailures: pkgFailures})
	if !strings.Contains(md, "| 5.4 | crash proves its window | ⚠️ lacuna") {
		t.Errorf("want a gap when the owning package failed at the package level:\n%s", md)
	}
	if !strings.Contains(md, "test/e2e/crash") {
		t.Errorf("want the failed package named in the header:\n%s", md)
	}
	if strings.Contains(md, "✅") {
		t.Errorf("a package-level failure must never render as ✅:\n%s", md)
	}
}

// TestSameNameFailsInOnePackageAndPassesInAnother covers M2: results are
// keyed by package+test, and when one bare name appears in several
// packages, a failure anywhere wins over a pass anywhere else.
func TestSameNameFailsInOnePackageAndPassesInAnother(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"pass","Package":"internal/foo","Test":"TestMain","Elapsed":0.1}`,
		`{"Action":"fail","Package":"test/e2e/crash","Test":"TestMain","Elapsed":0.2}`,
	}, "\n")
	results, _, err := summarize(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	reqs := []requirement{{ID: "x", Text: "shared name", Tests: []string{"TestMain"}}}
	md := render(reqs, results, header{Commit: "abc1234"})
	if !strings.Contains(md, "| x | shared name | ❌") {
		t.Errorf("a failure in one package must win over a pass in another:\n%s", md)
	}
	if !strings.Contains(md, "TestMain [internal/foo] (0.1s)") || !strings.Contains(md, "TestMain [test/e2e/crash] (fail)") {
		t.Errorf("want both packages' outcomes shown:\n%s", md)
	}
}

// TestFullTableStillWorks is a broader regression check mixing every
// outcome in one render, matching the original acceptance test's shape.
func TestFullTableStillWorks(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"pass","Package":"p","Test":"TestA","Elapsed":0.5}`,
		`{"Action":"fail","Package":"p","Test":"TestB","Elapsed":1.25}`,
		`{"Action":"pass","Package":"p","Test":"TestB/sub","Elapsed":0.1}`,
	}, "\n")
	results, _, err := summarize(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
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
