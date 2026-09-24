package bootstrap

import (
	"testing"

	"go.uber.org/fx"

	"github.com/lavarini/backend-challenge-go/internal/platform/config"
)

// ValidateApp checks the graph without running constructors, for every
// configured role: API, consumer, outbox relay, reference worker and all.
func TestGraphIsValidForEveryRole(t *testing.T) {
	for _, role := range []config.Role{config.RoleAPI, config.RoleConsumer, config.RoleOutboxRelay, config.RoleReferenceWorker, config.RoleAll} {
		cfg := config.Config{Role: role}
		if err := fx.ValidateApp(Options(cfg)); err != nil {
			t.Errorf("role %s: %v", role, err)
		}
	}
}
