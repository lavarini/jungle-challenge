// Policies live in deploy/iam/. This test parses each one and checks it
// against a hand-written table of the AWS actions the corresponding role's
// code actually calls (found by grepping internal/adapters/sqsin,
// internal/adapters/snsout, internal/adapters/outbox and internal/bootstrap
// for SDK calls). A Go-AST derivation was considered but rejected: mapping an
// SDK method name to its IAM action string needs per-package knowledge of
// which AWS service the package talks to, and a couple of call sites (like
// the boot-time sqs:GetQueueAttributes health probe wired into every role by
// internal/bootstrap/core.go) are not owned by any single role's adapter
// package, so attributing them correctly needs human judgement, not a
// mechanical scan. See deploy/iam/README.md.
package archtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type iamStatement struct {
	Sid      string   `json:"Sid"`
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource string   `json:"Resource"`
}

type iamPolicy struct {
	Version   string         `json:"Version"`
	Statement []iamStatement `json:"Statement"`
}

// expectedIAMActions is the minimal action set per process role (ADR 0016,
// spec section 5). Every role runs /health/ready, which probes the input
// queue with sqs:GetQueueAttributes (spec section 7); beyond that, api and
// reference-worker call no AWS API.
var expectedIAMActions = map[string][]string{
	"api": {"sqs:GetQueueAttributes"},
	"consumer": {
		"sqs:ReceiveMessage",
		"sqs:DeleteMessage",
		"sqs:ChangeMessageVisibility",
		"sqs:SendMessage",
		"sqs:GetQueueAttributes",
	},
	"outbox-relay": {
		"sns:Publish",
		"sns:GetTopicAttributes",
		"sqs:GetQueueAttributes",
	},
	"reference-worker": {"sqs:GetQueueAttributes"},
}

func loadIAMPolicy(t *testing.T, role string) iamPolicy {
	t.Helper()
	path := filepath.Join(repoRoot(t), "deploy", "iam", role+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var p iamPolicy
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return p
}

func actionSet(p iamPolicy) []string {
	seen := map[string]bool{}
	for _, s := range p.Statement {
		for _, a := range s.Action {
			seen[a] = true
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func TestIAMPolicyActionsMatchCode(t *testing.T) {
	for role, want := range expectedIAMActions {
		t.Run(role, func(t *testing.T) {
			got := actionSet(loadIAMPolicy(t, role))
			wantSorted := append([]string(nil), want...)
			sort.Strings(wantSorted)
			if len(got) != len(wantSorted) {
				t.Fatalf("%s: got actions %v, want %v", role, got, wantSorted)
			}
			for i := range got {
				if got[i] != wantSorted[i] {
					t.Fatalf("%s: got actions %v, want %v", role, got, wantSorted)
				}
			}
		})
	}
}

func TestIAMPolicyHasNoWildcards(t *testing.T) {
	for role := range expectedIAMActions {
		t.Run(role, func(t *testing.T) {
			p := loadIAMPolicy(t, role)
			for _, s := range p.Statement {
				for _, a := range s.Action {
					if strings.Contains(a, "*") {
						t.Errorf("%s: wildcard action %q in statement %q", role, a, s.Sid)
					}
				}
				if strings.Contains(s.Resource, "*") {
					t.Errorf("%s: wildcard resource %q in statement %q", role, s.Resource, s.Sid)
				}
			}
		})
	}
}

func TestIAMPolicyFilesCoverEveryRole(t *testing.T) {
	dir := filepath.Join(repoRoot(t), "deploy", "iam")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	found := map[string]bool{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			found[strings.TrimSuffix(e.Name(), ".json")] = true
		}
	}
	for role := range expectedIAMActions {
		if !found[role] {
			t.Errorf("no deploy/iam/%s.json for role %q", role, role)
		}
	}
}
