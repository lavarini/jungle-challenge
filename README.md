# Processamento distribuído de apostas em Go

[![ci](https://github.com/lavarini/claude-challenge/actions/workflows/ci.yml/badge.svg)](https://github.com/lavarini/claude-challenge/actions/workflows/ci.yml)

Serviço de carteira e apostas com entrega at-least-once por HTTP e SQS, outbox transacional para
SNS FIFO e invariantes financeiras impostas no PostgreSQL. O enunciado está em
[`docs/DESAFIO.md`](docs/DESAFIO.md).

| Documento | Conteúdo |
|---|---|
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | visão, fluxo, garantias e onde são impostas, falhas, limitações |
| [`docs/EVIDENCIAS.md`](docs/EVIDENCIAS.md) | cada requisito com os testes que o provam, gerado pela execução |
| [`docs/adr/`](docs/adr/README.md) | 20 decisões de arquitetura |
| [`docs/openapi.yaml`](docs/openapi.yaml) | contrato HTTP |
| [`docs/eventos.md`](docs/eventos.md) | mensagem de entrada e eventos de saída |
| [`docs/RUNBOOK.md`](docs/RUNBOOK.md) | o que fazer com DLQ, quarentena, `FAILED`, divergência, pendência e backlog |
| [`docs/design.md`](docs/design.md) | desenho da solução |

## Pré-requisitos

- Go 1.27.x
- Docker com Compose v2

## Subir e testar

```sh
docker compose up --build        # Postgres, Keycloak, LocalStack, migrations e a API em :8080
make smoke                       # abre carteira, aposta e replay com tokens reais do Keycloak
docker compose --profile multi up --build   # três instâncias: :8080, :8082, :8083
docker compose --profile multi down -v
```

| Comando | O que faz |
|---|---|
| `make lint` | gofmt e go vet (inclusive código com build tags) |
| `make test-race` | testes unitários com `-race`, sem containers |
| `make test-integration` | PostgreSQL, Keycloak e LocalStack reais via testcontainers |
| `make test-e2e` | três processos `wagerd` independentes contra a mesma infraestrutura |
| `make test-crash` | queda abrupta (`os.Exit(137)` por failpoint) nas janelas de commit, publicação e claim; `SIGKILL` e reinício; `SIGTERM` |
| `make test-failpoint` | testes do pacote de failpoints com `-tags failpoint` |
| `make evidence` | roda todas as suítes com `-json` e regenera `docs/EVIDENCIAS.md` (~4 min) |

Papéis do processo: `WAGERD_ROLE=api|consumer|outbox-relay|reference-worker|all`. O Compose
roda `all` nas três instâncias.

Migrations: `wagerd migrate up` e `wagerd migrate down`, com `MIGRATE_DATABASE_URL`
apontando para a role proprietária `wager_migrator`. O `down` apaga os dados e
existe só para desenvolvimento. Variáveis em [`.env.example`](.env.example).

### Identidades locais (Keycloak, `client_credentials`)

| Client | Segredo (só desenvolvimento) | Papel |
|---|---|---|
| `provider-a` | `provider-a-dev-secret` | `wager:provider`, `provider_id=provider-a` |
| `provider-b` | `provider-b-dev-secret` | `wager:provider`, `provider_id=provider-b` |
| `wallet-internal` | `wallet-internal-dev-secret` | `wallet:internal` |

### Mensageria local

| Recurso | Uso |
|---|---|
| `wager-transactions.fifo` | entrada de operações (`MessageGroupId = walletId`, `MessageDeduplicationId = messageId`) |
| `wager-transactions-dlq.fifo` | mensagens inválidas ou não autorizadas (atributos `failureCode` e `reason`) e esgotadas |
| `wallet-events.fifo` (SNS) | eventos publicados pela outbox |
| `wallet-events-audit.fifo` | assinante de demonstração do tópico |

Produtores são identificados pela credencial: no LocalStack, a access key
`111111111111` fala por `provider-a` e `222222222222` por `provider-b`
(`SQS_SENDER_PROVIDERS`). Enviar uma aposta pela fila:

```sh
MSG_ID=$(uuidgen | tr A-Z a-z)
AWS_ACCESS_KEY_ID=111111111111 AWS_SECRET_ACCESS_KEY=test aws --endpoint-url http://localhost:4566 \
  sqs send-message --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id <walletId> --message-deduplication-id "$MSG_ID" \
  --message-body '{"messageId":"'"$MSG_ID"'","type":"WagerTransactionRequested","occurredAt":"2026-09-24T12:00:00Z","data":{...}}'
```

O envelope completo e os desfechos de cada mensagem estão em
[`docs/eventos.md`](docs/eventos.md).

### Métricas e pprof

A porta administrativa (`ADMIN_ADDR`, padrão `:9090`) serve `/metrics`, `/health/ready` e
`/debug/pprof/`. O Compose não a publica, e a imagem é distroless (sem shell nem `curl`). Para
inspecionar localmente, entre na rede do container:

```sh
docker run --rm --network "container:$(docker compose ps -q app)" curlimages/curl -s \
  localhost:9090/metrics | grep -E '^(wager|outbox|sqs)_'
```

As métricas seguem a seção 7 da spec, sem identificadores em rótulos. O
[RUNBOOK](docs/RUNBOOK.md) liga cada alerta a um procedimento.
