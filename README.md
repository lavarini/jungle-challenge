# Processamento distribuído de apostas em Go

> Em construção. Enunciado em [`docs/DESAFIO.md`](docs/DESAFIO.md); design em
> [`docs/design.md`](docs/design.md); decisões em
> [`docs/adr/`](docs/adr/README.md).

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
AWS_ACCESS_KEY_ID=111111111111 AWS_SECRET_ACCESS_KEY=test aws --endpoint-url http://localhost:4566 \
  sqs send-message --queue-url http://localhost:4566/000000000000/wager-transactions.fifo \
  --message-group-id <walletId> --message-deduplication-id "$(uuidgen)" \
  --message-body '{"messageId":"msg-1","type":"WagerTransactionRequested","occurredAt":"2026-09-24T12:00:00Z","data":{...}}'
```
