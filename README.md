# Processamento distribuído de apostas em Go

[![ci](https://github.com/lavarini/claude-challenge/actions/workflows/ci.yml/badge.svg)](https://github.com/lavarini/claude-challenge/actions/workflows/ci.yml)

Serviço de carteira e apostas com entrega at-least-once por HTTP e SQS, outbox transacional para
SNS FIFO e invariantes financeiras impostas no PostgreSQL. O enunciado está em
[`docs/DESAFIO.md`](docs/DESAFIO.md).

| Documento | Conteúdo |
|---|---|
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | visão, fluxo, garantias e onde são impostas, falhas, limitações |
| [`docs/EVIDENCIAS.md`](docs/EVIDENCIAS.md) | cada requisito com os testes que o provam, gerado pela execução |
| [`docs/adr/`](docs/adr/README.md) | 19 decisões de arquitetura |
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

## Exemplos de chamadas

Executados contra `docker compose up --build`, com tokens reais do Keycloak local. Corpos e
campos completos estão em [`docs/openapi.yaml`](docs/openapi.yaml).

```sh
KC=http://localhost:8081/realms/wagering
tok() { curl -fsS -X POST "$KC/protocol/openid-connect/token" -d grant_type=client_credentials \
  -d client_id="$1" -d client_secret="$2" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'; }
INTERNAL=$(tok wallet-internal wallet-internal-dev-secret)
PROVIDER=$(tok provider-a provider-a-dev-secret)
PLAYER=$(uuidgen | tr 'A-Z' 'a-z')
```

Abrir carteira (`201`; guarda o `id` em `WALLET` para os exemplos seguintes):

```sh
WALLET=$(curl -sS -X POST localhost:8080/wallets -H "Authorization: Bearer $INTERNAL" -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER\",\"initialBalance\":{\"amount\":\"1000.00\",\"currency\":\"BRL\"}}" \
  | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
```

Consultar a carteira (`200`):

```sh
curl -sS localhost:8080/wallets/$WALLET -H "Authorization: Bearer $INTERNAL"
```

Enviar uma aposta com chave de idempotência (`201` na primeira vez, `idempotentReplay: false`;
guarda o `transactionId` em `TXID`):

```sh
EXT="tx-$(uuidgen | tr 'A-Z' 'a-z')"
TXID=$(curl -sS -X POST localhost:8080/wagering/transactions -H "Authorization: Bearer $PROVIDER" \
  -H "Content-Type: application/json" -H "Idempotency-Key: provider-a:$EXT" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$EXT\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}" \
  | sed -n 's/.*"transactionId":"\([^"]*\)".*/\1/p')
```

O mesmo comando, repetido com a mesma `Idempotency-Key` e o mesmo corpo (replay, `200`, mesmo
`transactionId`, `idempotentReplay: true`, sem debitar de novo):

```sh
curl -sS -X POST localhost:8080/wagering/transactions -H "Authorization: Bearer $PROVIDER" \
  -H "Content-Type: application/json" -H "Idempotency-Key: provider-a:$EXT" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$EXT\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"
```

Consultar a transação por id (`200`):

```sh
curl -sS localhost:8080/wagering/transactions/$TXID -H "Authorization: Bearer $PROVIDER"
```

Consultar pelo id externo do provedor (`200`):

```sh
curl -sS "localhost:8080/providers/provider-a/wagering/transactions/$EXT" -H "Authorization: Bearer $PROVIDER"
```

Ledger paginado por cursor (`200`):

```sh
curl -sS "localhost:8080/wallets/$WALLET/ledger?limit=10" -H "Authorization: Bearer $INTERNAL"
```

Reconciliação (`200`, `consistent: true`, sem alterar o saldo):

```sh
curl -sS -X POST "localhost:8080/wallets/$WALLET/reconciliation" -H "Authorization: Bearer $INTERNAL"
```

Provedor tentando abrir carteira, operação restrita ao papel `internal` (`403`):

```sh
curl -sS -X POST localhost:8080/wallets -H "Authorization: Bearer $PROVIDER" -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER\",\"initialBalance\":{\"amount\":\"1000.00\",\"currency\":\"BRL\"}}"
```

## Testes: dependências e build tags

| Comando | Exige Docker | O que roda |
|---|---|---|
| `go test ./...` | não | unitários |
| `go test -race ./...` | não | unitários com `-race` |
| `go test -race -tags=integration -count=1 -timeout=15m ./test/integration/...` | sim | PostgreSQL, Keycloak e LocalStack reais via testcontainers, subidos pelo próprio teste |
| `go test -tags=e2e -count=1 -timeout=20m ./test/e2e/` | sim | três processos `wagerd` compilados pelo teste, contra a mesma infraestrutura |
| `go test -tags=e2e -count=1 -timeout=20m ./test/e2e/crash/...` | sim | quedas abruptas, `SIGKILL`, `SIGTERM` |
| `go test -race -tags=failpoint ./internal/platform/failpoint/` | não | testes do próprio pacote de failpoints |
| `docker compose --profile multi up --build` | sim | três instâncias manuais, para inspeção interativa |

Os comandos correspondem um a um aos alvos do `Makefile` (`make test`, `make test-race`,
`make test-integration`, `make test-e2e`, `make test-crash`, `make test-failpoint`, `make up-multi`).

Não rode duas suítes que exigem Docker ao mesmo tempo: os testes de integração e e2e sobem seus
próprios containers via testcontainers e disputam os mesmos recursos.
