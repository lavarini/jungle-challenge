// Package config loads and validates process configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	Region         string
	WagerQueueURL  string
	DLQURL         string
	Senders        map[string]string // SenderId -> providerId (ADR 0016)
	EventsTopicARN string
}

// ParseSenders reads "senderId=providerId,senderId=providerId".
func ParseSenders(s string) (map[string]string, error) {
	out := map[string]string{}
	if s == "" {
		return out, nil
	}
	for _, pair := range strings.Split(s, ",") {
		sender, provider, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || sender == "" || provider == "" {
			return nil, fmt.Errorf("SQS_SENDER_PROVIDERS: invalid pair %q", pair)
		}
		out[sender] = provider
	}
	return out, nil
}

type ReferenceConfig struct {
	TTL, InitialBackoff, MaxBackoff time.Duration
}

type WorkersConfig struct {
	PollInterval time.Duration
	Lease        time.Duration
	Batch        int
}

type Config struct {
	Role            Role
	HTTPAddr        string
	ShutdownTimeout time.Duration
	Database        DatabaseConfig
	OIDC            OIDCConfig
	AWS             AWSConfig
	Reference       ReferenceConfig
	Workers         WorkersConfig
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
		Reference: ReferenceConfig{
			TTL:            duration("REFERENCE_TTL", "24h"),
			InitialBackoff: duration("REFERENCE_BACKOFF_INITIAL", "1s"),
			MaxBackoff:     duration("REFERENCE_BACKOFF_MAX", "5m"),
		},
		Workers: WorkersConfig{
			PollInterval: duration("WORKER_POLL_INTERVAL", "500ms"),
			Lease:        duration("WORKER_LEASE", "30s"),
		},
	}
	c.OIDC.DiscoveryURL = str("OIDC_DISCOVERY_URL", c.OIDC.IssuerURL)

	maxConns, err := strconv.ParseInt(str("DB_MAX_CONNS", "20"), 10, 32)
	if err != nil || maxConns < 1 {
		errs = append(errs, errors.New("DB_MAX_CONNS must be a positive integer"))
	}
	c.Database.MaxConns = int32(maxConns)

	batch, err := strconv.Atoi(str("WORKER_BATCH", "50"))
	if err != nil || batch < 1 {
		errs = append(errs, errors.New("WORKER_BATCH must be a positive integer"))
	}
	c.Workers.Batch = batch
	if c.Reference.MaxBackoff < c.Reference.InitialBackoff {
		errs = append(errs, errors.New("REFERENCE_BACKOFF_MAX must be >= REFERENCE_BACKOFF_INITIAL"))
	}

	c.AWS.DLQURL = getenv("SQS_WAGER_DLQ_URL")
	c.AWS.EventsTopicARN = getenv("SNS_EVENTS_TOPIC_ARN")
	senders, err := ParseSenders(getenv("SQS_SENDER_PROVIDERS"))
	if err != nil {
		errs = append(errs, err)
	}
	c.AWS.Senders = senders
	if c.Role.Runs(RoleConsumer) && (c.AWS.DLQURL == "" || len(c.AWS.Senders) == 0) {
		errs = append(errs, errors.New("the consumer role requires SQS_WAGER_DLQ_URL and SQS_SENDER_PROVIDERS"))
	}
	if c.Role.Runs(RoleOutboxRelay) && c.AWS.EventsTopicARN == "" {
		errs = append(errs, errors.New("the outbox-relay role requires SNS_EVENTS_TOPIC_ARN"))
	}

	switch c.Role {
	case RoleAPI, RoleConsumer, RoleOutboxRelay, RoleReferenceWorker, RoleAll:
	default:
		errs = append(errs, fmt.Errorf("WAGERD_ROLE %q is unknown", c.Role))
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	return c, nil
}
