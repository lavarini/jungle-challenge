# Teste de carga

Gerador próprio (`cmd/load`) contra o Compose com três instâncias `all` do `wagerd`. Cobre a
seção 14 do enunciado: comando reproduzível, ambiente, metodologia, throughput, p50/p95/p99,
erros, conflitos e atraso da outbox.

## Comando reproduzível

```sh
docker compose --profile multi up --build -d --wait
make load                          | tee load-1.md      # -hot 0.2 (padrão)
make load LOAD_ARGS="-hot 0.8"     | tee load-hot.md
docker compose --profile multi down -v
```

`make load` roda `go run ./cmd/load $(LOAD_ARGS)`; `go run ./cmd/load -h` lista todas as flags.
Nesta máquina a porta 5432 do host já estava ocupada por um container de outro projeto, então o
Postgres do Compose foi remapeado para `127.0.0.1:15432` com um `docker-compose.override.yml`
local e não versionado (`ports: !override`), e as execuções abaixo usaram
`-db postgres://wager_app:app-dev-only@127.0.0.1:15432/wagering?sslmode=disable`. O padrão da
flag `-db` no binário continua apontando para a porta 5432 padrão do Compose, o que um checkout
limpo usa sem precisar de override.

## Ambiente

- Máquina: Mac mini, `x86_64`, 12 CPUs (`runtime.NumCPU()`), `darwin/amd64` (`GOOS/GOARCH`).
- Docker 29.8.0, Docker Compose v5.5.1.
- `docker compose --profile multi up --build -d --wait`: três instâncias `all` do `wagerd`
  (`app` em `:8080`, `app2` em `:8082`, `app3` em `:8083`), todas contra o mesmo PostgreSQL,
  Keycloak e LocalStack.
- Imagens (de `docker-compose.yml`): `postgres:17.6-alpine`, `quay.io/keycloak/keycloak:26.3.3`,
  `localstack/localstack:4.7.0`, `wagerd:local` (build local, `Dockerfile` do repositório).
- O gerador de carga (`cmd/load`) rodou na mesma máquina que a stack — ver Limitações.

## Metodologia

`cmd/load` obtém tokens `client_credentials` de `provider-a` e `wallet-internal` no Keycloak
(mesmo fluxo de `scripts/smoke.sh`), abre `-wallets` carteiras com `100000.00 BRL` (a primeira é
a "quente") e sobe `-concurrency` goroutines por `-duration`. Cada iteração:

1. Escolhe uma das `-api` em round-robin (contador atômico global, uma requisição por vez).
2. Com probabilidade `-dup`, reenvia a última operação do próprio worker com a mesma
   `Idempotency-Key` (exercita o caminho de replay idempotente, HTTP 200).
3. Caso contrário, gera uma nova operação `BET` de `1.00 BRL` com `externalTransactionId` novo
   (UUID) contra a carteira quente (probabilidade `-hot`) ou uma das demais carteiras escolhida
   uniformemente (probabilidade `1 - hot`).

Ao fim da janela de `-duration`, o gerador espera até 60 s a outbox drenar
(`published_at IS NULL AND dead_at IS NULL` chegar a zero), mede o atraso
`published_at - occurred_at` para os eventos ocorridos desde o início da carga
(`percentile_cont` em 0.5/0.95/0.99) e chama `POST /wallets/{id}/reconciliation` em todas as
carteiras abertas — qualquer `consistent=false` é reportado como divergência.

Duas execuções, variando só `-hot`, para isolar o efeito da contenção de escrita numa única
carteira:

| Flag | Padrão | Execução "quente" |
|---|---|---|
| `-duration` | 60s | 60s |
| `-concurrency` | 32 | 32 |
| `-wallets` | 50 | 50 |
| `-hot` | 0.2 | 0.8 |
| `-dup` | 0.1 | 0.1 |

## Resultados

### Execução padrão (`-hot 0.2`)

- Total de requisições: 26576
- Duração efetiva: 60.000492762s
- Throughput: **442.9 req/s**

| Métrica | Valor |
|---|---|
| p50 | 16.2 ms |
| p95 | 418.3 ms |
| p99 | 982.0 ms |
| max | 3262.4 ms |

| Status | Contagem |
|---|---|
| 201 novo | 23913 |
| 200 replay | 2631 |
| 409 conflito | 0 |
| 422 | 0 |
| 5xx | 0 |
| outros HTTP | 0 |
| erro de transporte | 32 |

Contagem bruta por código HTTP: `200=2631, 201=23913`.

**Outbox**: não drenou dentro do limite de 60 s (38934 eventos ainda pendentes).

| Percentil | Atraso (`occurred_at` -> `published_at`) |
|---|---|
| p50 | 75.736 s |
| p95 | 103.884 s |
| p99 | 106.544 s |

**Reconciliação**: 50/50 carteiras com `consistent=true`.

### Execução com contenção (`-hot 0.8`)

- Total de requisições: 12019
- Duração efetiva: 60.000686841s
- Throughput: **200.3 req/s**

| Métrica | Valor |
|---|---|
| p50 | 21.9 ms |
| p95 | 712.1 ms |
| p99 | 1240.0 ms |
| max | 2633.5 ms |

| Status | Contagem |
|---|---|
| 201 novo | 10832 |
| 200 replay | 1155 |
| 409 conflito | 0 |
| 422 | 0 |
| 5xx | 0 |
| outros HTTP | 0 |
| erro de transporte | 32 |

Contagem bruta por código HTTP: `200=1155, 201=10832`.

**Outbox**: não drenou dentro do limite de 60 s (39348 eventos ainda pendentes). Esta execução
começou logo após a anterior, sem esperar o backlog dela drenar por completo — ver Limitações.

| Percentil | Atraso (`occurred_at` -> `published_at`) |
|---|---|
| p50 | 23.715 s |
| p95 | 41.752 s |
| p99 | 92.137 s |

**Reconciliação**: 50/50 carteiras com `consistent=true`.

## Leitura

A contenção na carteira quente aparece claramente em latência e throughput: de 0.2 para 0.8 de
`-hot`, o p99 sobe de 982 ms para 1240 ms (e o p95 quase dobra, de 418 ms para 712 ms), enquanto o
throughput cai de 442.9 para 200.3 req/s com a mesma concorrência — as goroutines passam mais
tempo esperando a mesma linha. Ela **não** aparece como conflito HTTP: as duas execuções tiveram
zero `409`, porque a escrita da carteira usa `SELECT ... FOR UPDATE` (`internal/app/submit_wager.go`)
e serializa concorrentes por bloqueio de linha em vez de rejeitá-los — o efeito observável do lado
do cliente é latência, não status code. `409` (`IDEMPOTENCY_KEY_MISMATCH`/`IDEMPOTENCY_PAYLOAD_MISMATCH`)
também não foi exercitado porque o gerador nunca envia a mesma `Idempotency-Key` de dois workers
ao mesmo tempo — cada worker só reenvia a própria última operação. Os 32 erros de transporte de
cada execução batem exatamente com `-concurrency`: é a requisição em voo de cada worker cancelada
quando o contexto de `-duration` expira, não uma falha do serviço (nenhum `5xx` apareceu em
nenhuma execução). A outbox foi o maior gargalo: sob 442.9 req/s ela não drena nem perto do limite
de 60 s (p99 de atraso acima de 100 s), e os três `wagerd` chegaram a logar
`outbox claim failed: ... canceling statement due to statement timeout (SQLSTATE 57014)` durante
a disputa pelas linhas — a taxa de geração de eventos superou a capacidade do relay nesta máquina.

## Limitações

- **LocalStack não é SQS/SNS real**: sem a latência de rede, os limites de throughput e o
  comportamento de scaling de uma fila/tópico gerenciados; os números aqui não predizem o
  comportamento em produção na AWS.
- **Mesma máquina para gerador e sistema**: o gerador de carga, os três `wagerd`, o PostgreSQL, o
  Keycloak e o LocalStack disputam os mesmos 12 CPUs. Throughput e latência não isolam custo de
  cliente e servidor.
- **As duas execuções rodaram em sequência sem esperar a outbox drenar por completo**: a segunda
  herda parte do backlog de eventos pendentes da primeira, o que mistura o efeito nos números de
  atraso da outbox reportados para ela (provavelmente favorecendo-a, já que ela mesma gerou menos
  eventos por segundo que a primeira).
- **O gerador não exercita corrida na mesma `Idempotency-Key`**: cada worker só reenvia a própria
  última operação, nunca a de outro, então `409` por conflito de idempotência nunca aparece por
  construção — só serve para validar o caminho de replay (`200`), não o de conflito de chave.
- **O limite de 60 s de espera pela outbox é insuficiente sob esta carga**: os percentis de atraso
  reportados vêm de uma consulta feita ao fim da espera, mesmo com milhares de eventos ainda
  pendentes; eles não são um atraso "final", só o estado observado naquele instante.
