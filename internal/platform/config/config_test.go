package config

import (
	"strings"
	"testing"
	"time"
)

func env(overrides map[string]string) func(string) string {
	base := map[string]string{
		"DATABASE_URL":        "postgres://wager_app:x@localhost:5432/wagering",
		"OIDC_ISSUER_URL":     "http://localhost:8081/realms/wagering",
		"AWS_REGION":          "us-east-1",
		"SQS_WAGER_QUEUE_URL": "http://localhost:4566/000000000000/wager-transactions.fifo",
	}
	for k, v := range overrides {
		base[k] = v
	}
	return func(k string) string { return base[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Role != RoleAll || c.HTTPAddr != ":8080" || c.Database.LockTimeout != 2*time.Second ||
		c.OIDC.Audience != "wagering-api" || c.OIDC.DiscoveryURL != c.OIDC.IssuerURL || c.ShutdownTimeout != 25*time.Second {
		t.Fatalf("defaults %+v", c)
	}
}

func TestLoadReportsEveryMissingVariable(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error")
	}
	for _, name := range []string{"DATABASE_URL", "OIDC_ISSUER_URL", "AWS_REGION", "SQS_WAGER_QUEUE_URL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s: %v", name, err)
		}
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"WAGERD_ROLE":     "everything",
		"DB_LOCK_TIMEOUT": "soon",
		"DB_MAX_CONNS":    "-1",
	}
	for k, v := range cases {
		if _, err := Load(env(map[string]string{k: v})); err == nil {
			t.Errorf("%s=%s accepted", k, v)
		}
	}
	if _, err := Load(env(map[string]string{"WAGERD_ROLE": "consumer"})); err == nil {
		t.Error("roles without modules must be rejected on day 1")
	}
}

func TestRoleRuns(t *testing.T) {
	if !RoleAll.Runs(RoleAPI) || !RoleAPI.Runs(RoleAPI) || RoleAPI.Runs(RoleConsumer) {
		t.Fatal("Runs mismatch")
	}
}
