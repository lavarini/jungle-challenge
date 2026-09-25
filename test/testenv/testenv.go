//go:build integration || e2e

// Package testenv starts the real infrastructure used by integration and e2e tests.
package testenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/localstack"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/sync/errgroup"
)

// TemplateDatabase is the name of the migrated database that Start creates
// (see startPostgres) and that NewDatabase clones for each test.
const TemplateDatabase = "wagering"

const (
	ProviderAID          = "provider-a"
	ProviderASecret      = "provider-a-dev-secret"
	ProviderBID          = "provider-b"
	ProviderBSecret      = "provider-b-dev-secret"
	ProviderAShortID     = "provider-a-short"
	ProviderAShortSecret = "provider-a-short-dev-secret"
	InternalID           = "wallet-internal"
	InternalSecret       = "wallet-internal-dev-secret"

	Region  = "us-east-1"
	Account = "000000000000"
)

type Postgres struct {
	SuperDSN, MigratorDSN, AppDSN string
	Host, Port                    string
}

type Keycloak struct {
	BaseURL, IssuerURL string
}

type LocalStack struct {
	Endpoint string
}

type Env struct {
	Postgres   Postgres
	Keycloak   Keycloak
	LocalStack LocalStack

	pg         testcontainers.Container
	containers []testcontainers.Container
}

// Start launches PostgreSQL, Keycloak and LocalStack in parallel.
func Start(ctx context.Context) (*Env, error) {
	env := &Env{}
	var pg, kc, ls testcontainers.Container
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { pg, err = startPostgres(gctx, env); return err })
	g.Go(func() (err error) { kc, err = startKeycloak(gctx, env); return err })
	g.Go(func() (err error) { ls, err = startLocalStack(gctx, env); return err })
	err := g.Wait()
	env.pg = pg
	for _, c := range []testcontainers.Container{pg, kc, ls} {
		if c != nil {
			env.containers = append(env.containers, c)
		}
	}
	if err != nil {
		env.Terminate(context.Background())
		return nil, err
	}
	return env, nil
}

// PostgresContainerID lets failure tests pause the database with the Docker CLI.
func (e *Env) PostgresContainerID() string { return e.pg.GetContainerID() }

func (e *Env) Terminate(ctx context.Context) {
	for _, c := range e.containers {
		_ = c.Terminate(ctx)
	}
}

func repoFile(rel string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", rel)
}

func startPostgres(ctx context.Context, env *Env) (testcontainers.Container, error) {
	c, err := tcpostgres.Run(ctx, "postgres:17.6-alpine",
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres-dev-only"),
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithInitScripts(repoFile("deploy/postgres/init.sql")),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return c, fmt.Errorf("postgres: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		return c, err
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return c, err
	}
	dsn := func(user, pass, db string) string {
		return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, port.Port(), db)
	}
	env.Postgres = Postgres{
		SuperDSN:    dsn("postgres", "postgres-dev-only", "wagering"),
		MigratorDSN: dsn("wager_migrator", "migrator-dev-only", "wagering"),
		AppDSN:      dsn("wager_app", "app-dev-only", "wagering"),
		Host:        host,
		Port:        port.Port(),
	}
	return c, nil
}

func startKeycloak(ctx context.Context, env *Env) (testcontainers.Container, error) {
	req := testcontainers.ContainerRequest{
		Image:        "quay.io/keycloak/keycloak:26.3.3",
		Cmd:          []string{"start-dev", "--import-realm"},
		Env:          map[string]string{"KC_BOOTSTRAP_ADMIN_USERNAME": "admin", "KC_BOOTSTRAP_ADMIN_PASSWORD": "admin-dev-only"},
		ExposedPorts: []string{"8080/tcp"},
		Files: []testcontainers.ContainerFile{{
			HostFilePath:      repoFile("deploy/keycloak/realm-wagering.json"),
			ContainerFilePath: "/opt/keycloak/data/import/realm-wagering.json",
			FileMode:          0o644,
		}},
		WaitingFor: wait.ForHTTP("/realms/wagering/.well-known/openid-configuration").
			WithPort("8080/tcp").WithStartupTimeout(4 * time.Minute),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		return c, fmt.Errorf("keycloak: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		return c, err
	}
	port, err := c.MappedPort(ctx, "8080/tcp")
	if err != nil {
		return c, err
	}
	base := fmt.Sprintf("http://%s:%s", host, port.Port())
	env.Keycloak = Keycloak{BaseURL: base, IssuerURL: base + "/realms/wagering"}
	return c, nil
}

func startLocalStack(ctx context.Context, env *Env) (testcontainers.Container, error) {
	c, err := localstack.Run(ctx, "localstack/localstack:4.7.0",
		testcontainers.WithEnv(map[string]string{"SERVICES": "sqs,sns", "AWS_DEFAULT_REGION": Region}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      repoFile("deploy/localstack/init/ready.d/10-resources.sh"),
			ContainerFilePath: "/etc/localstack/init/ready.d/10-resources.sh",
			FileMode:          0o755,
		}),
		testcontainers.WithWaitStrategy(wait.ForLog("wagering-resources-ready").WithStartupTimeout(3*time.Minute)),
	)
	if err != nil {
		return c, fmt.Errorf("localstack: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		return c, err
	}
	port, err := c.MappedPort(ctx, "4566/tcp")
	if err != nil {
		return c, err
	}
	env.LocalStack = LocalStack{Endpoint: fmt.Sprintf("http://%s:%s", host, port.Port())}
	return c, nil
}

// MarkAsTemplate flags the shared, migrated database so it can be used as a
// CREATE DATABASE ... TEMPLATE source. It must run after migrating and before
// any test calls NewDatabase.
func (e *Env) MarkAsTemplate(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, e.Postgres.SuperDSN)
	if err != nil {
		return fmt.Errorf("testenv: mark template: connect: %w", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `ALTER DATABASE `+TemplateDatabase+` WITH is_template = true`); err != nil {
		return fmt.Errorf("testenv: mark template: %w", err)
	}
	return nil
}

// NewDatabase clones the migrated template database into a fresh, uniquely
// named database with CREATE DATABASE ... TEMPLATE, and returns Postgres
// DSNs scoped to it -- the same shape as e.Postgres, so callers build pools
// from them exactly as they do from e.Postgres today (see test/integration's
// newStack).
//
// This exists because some integration suites claim rows globally by design
// (the outbox relay's ClaimHeads, the pending resolver's Claim): on a
// database shared across the whole run, an earlier test's leftover due rows
// make their claims and assertions order-dependent
// 
//
// t.Cleanup drops the database, WITH (FORCE) so open connections don't block
// it. Register any pool's Close as a cleanup after calling NewDatabase:
// t.Cleanup runs last-registered-first, so the pool closes before the drop.
func (e *Env) NewDatabase(ctx context.Context, t testing.TB) Postgres {
	t.Helper()
	super, err := pgx.Connect(ctx, e.Postgres.SuperDSN)
	if err != nil {
		t.Fatalf("testenv: new database: connect as superuser: %v", err)
	}
	defer super.Close(ctx)

	name := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := super.Exec(ctx, fmt.Sprintf(
		`CREATE DATABASE %s TEMPLATE %s OWNER wager_migrator STRATEGY WAL_LOG`, name, TemplateDatabase)); err != nil {
		t.Fatalf("testenv: new database: create %s: %v", name, err)
	}
	// Table-level GRANTs are part of the template's schema and are copied
	// with it, but CONNECT is a property of pg_database, not of the roles,
	// and a freshly created database does not inherit the template's datacl.
	if _, err := super.Exec(ctx, fmt.Sprintf(
		`GRANT CONNECT ON DATABASE %s TO wager_migrator, wager_app`, name)); err != nil {
		t.Fatalf("testenv: new database: grant connect on %s: %v", name, err)
	}
	t.Cleanup(func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conn, err := pgx.Connect(dropCtx, e.Postgres.SuperDSN)
		if err != nil {
			t.Logf("testenv: drop database %s: connect: %v", name, err)
			return
		}
		defer conn.Close(dropCtx)
		if _, err := conn.Exec(dropCtx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, name)); err != nil {
			t.Logf("testenv: drop database %s: %v", name, err)
		}
	})

	host, port := e.Postgres.Host, e.Postgres.Port
	dsn := func(user, pass string) string {
		return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, port, name)
	}
	return Postgres{
		SuperDSN:    dsn("postgres", "postgres-dev-only"),
		MigratorDSN: dsn("wager_migrator", "migrator-dev-only"),
		AppDSN:      dsn("wager_app", "app-dev-only"),
		Host:        host,
		Port:        port,
	}
}

func (l LocalStack) awsConfig(ctx context.Context, accessKey string) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx,
		config.WithRegion(Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, "test", "")),
	)
}

// SQS returns a client authenticated with accessKey. LocalStack treats a
// 12-digit access key as the caller's account id.
func (l LocalStack) SQS(ctx context.Context, accessKey string) (*sqs.Client, error) {
	cfg, err := l.awsConfig(ctx, accessKey)
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = aws.String(l.Endpoint) }), nil
}

func (l LocalStack) SNS(ctx context.Context, accessKey string) (*sns.Client, error) {
	cfg, err := l.awsConfig(ctx, accessKey)
	if err != nil {
		return nil, err
	}
	return sns.NewFromConfig(cfg, func(o *sns.Options) { o.BaseEndpoint = aws.String(l.Endpoint) }), nil
}

func (l LocalStack) QueueURL(ctx context.Context, name string) (string, error) {
	client, err := l.SQS(ctx, "test")
	if err != nil {
		return "", err
	}
	out, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.QueueUrl), nil
}

// Token obtains an access token with the client_credentials grant.
func (k Keycloak) Token(ctx context.Context, clientID, secret string) (string, error) {
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.IssuerURL+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		return "", errors.New("empty access token")
	}
	return body.AccessToken, nil
}
