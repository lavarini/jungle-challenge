// Package config loads and validates process configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

type Role string

const (
	RoleAPI             Role = "api"
	RoleConsumer        Role = "consumer"
	RoleOutboxRelay     Role = "outbox-relay"
	RoleReferenceWorker Role = "reference-worker"
	RoleAll             Role = "all"
)

// Runs reports whether a process with role r runs the given component.
func (r Role) Runs(component Role) bool { return r == RoleAll || r == component }

type DatabaseConfig struct {
	URL              string
	MaxConns         int32
	LockTimeout      time.Duration
	StatementTimeout time.Duration
}

type OIDCConfig struct {
	IssuerURL, DiscoveryURL, Audience string
	ClockSkew                         time.Duration
}

// AWSConfig carries only what the SDK does not read by itself. Credentials and
// AWS_ENDPOINT_URL come from the SDK's standard environment variables.
type AWSConfig struct {
	Region        string
	WagerQueueURL string
}

type Config struct {
	Role            Role
	HTTPAddr        string
	ShutdownTimeout time.Duration
	Database        DatabaseConfig
	OIDC            OIDCConfig
	AWS             AWSConfig
}

// Load reads configuration and reports every problem at once.
func Load(getenv func(string) string) (Config, error) {
	var errs []error
	str := func(name, def string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return def
	}
	required := func(name string) string {
		v := getenv(name)
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
		return v
	}
	duration := func(name, def string) time.Duration {
		d, err := time.ParseDuration(str(name, def))
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be a positive duration", name))
		}
		return d
	}

	c := Config{
		Role:            Role(str("WAGERD_ROLE", string(RoleAll))),
		HTTPAddr:        str("HTTP_ADDR", ":8080"),
		ShutdownTimeout: duration("SHUTDOWN_TIMEOUT", "25s"),
		Database: DatabaseConfig{
			URL:              required("DATABASE_URL"),
			LockTimeout:      duration("DB_LOCK_TIMEOUT", "2s"),
			StatementTimeout: duration("DB_STATEMENT_TIMEOUT", "5s"),
		},
		OIDC: OIDCConfig{
			IssuerURL: required("OIDC_ISSUER_URL"),
			Audience:  str("OIDC_AUDIENCE", "wagering-api"),
			ClockSkew: duration("OIDC_CLOCK_SKEW", "30s"),
		},
		AWS: AWSConfig{
			Region:        required("AWS_REGION"),
			WagerQueueURL: required("SQS_WAGER_QUEUE_URL"),
		},
	}
	c.OIDC.DiscoveryURL = str("OIDC_DISCOVERY_URL", c.OIDC.IssuerURL)

	maxConns, err := strconv.ParseInt(str("DB_MAX_CONNS", "20"), 10, 32)
	if err != nil || maxConns < 1 {
		errs = append(errs, errors.New("DB_MAX_CONNS must be a positive integer"))
	}
	c.Database.MaxConns = int32(maxConns)

	switch c.Role {
	case RoleAPI, RoleAll:
	case RoleConsumer, RoleOutboxRelay, RoleReferenceWorker:
		errs = append(errs, fmt.Errorf("WAGERD_ROLE %q is not available yet", c.Role))
	default:
		errs = append(errs, fmt.Errorf("WAGERD_ROLE %q is unknown", c.Role))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return c, nil
}
