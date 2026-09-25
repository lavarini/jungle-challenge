# Teste de carga

Gerador próprio (`cmd/load`) contra o Compose com três instâncias `all` do `wagerd`. Cobre a
seção 14 do enunciado: comando reproduzível, ambiente, metodologia, throughput, p50/p95/p99,
erros, conflitos e atraso da outbox.

## Comando reproduzível

Cada execução documentada parte de um stack novo, para que uma execução nunca herde o backlog da
outbox de uma anterior:

```sh
docker compose --profile multi down -v            # se já houver um stack de uma execução anterior
docker compose --profile multi up --build -d --wait
make load                          | tee load-1.md      # -hot 0.2 (padrão)
docker compose --profile multi down -v
docker compose --profile multi up --build -d --wait
make load LOAD_ARGS="-hot 0.8"     | tee load-hot.md
docker compose --profile multi down -v
```

`make load` roda `go run ./cmd/load $(LOAD_ARGS)`; `go run ./cmd/load -h` lista todas as flags.
As execuções abaixo usaram `-db` apontando para outra porta local
(`postgres://wager_app:app-dev-only@127.0.0.1:15432/wagering?sslmode=disable`). O padrão da flag
`-db` no binário continua apontando para a porta 5432 padrão do Compose, o que um checkout limpo
usa sem precisar de override.

## Ambiente

- Máquina: Mac mini, `x86_64`, 12 CPUs (`runtime.NumCPU()`), `darwin/amd64` (`GOOS/GOARCH`).
- Docker 29.8.0, Docker Compose v5.5.1.
- `docker compose --profile multi up --build -d --wait`: três instâncias `all` do `wagerd`
  (`app` em `:8080`, `app2` em `:8082`, `app3` em `:8083`), todas contra o mesmo PostgreSQL,
  Keycloak e LocalStack.
- Imagens (de `docker-compose.yml`): `postgres:17.6-alpine`, `quay.io/keycloak/keycloak:26.3.3`,
  `localstack/localstack:4.7.0`, `wagerd:local` (build local, `Dockerfile` do repositório).
- O relay da outbox inclui as três mudanças descritas em ADR 0014, seção "Revisão": claim por skip
  scan (não depende mais de estatísticas atualizadas da tabela), publicação paralela das cabeças de
  um lote (`OUTBOX_PUBLISH_CONCURRENCY=16`, o padrão) e reivindicação imediata de um novo lote
  quando o anterior não veio vazio. Isso eliminou os erros `57014` (`canceling statement due to
  statement timeout`) que apareciam na claim sob carga antes dessas mudanças.
- O gerador de carga (`cmd/load`) rodou na mesma máquina que a stack — ver Limitações.

## Metodologia

`cmd/load` obtém tokens `client_credentials` de `provider-a` e `wallet-internal` no Keycloak
(mesmo fluxo de `scripts/smoke.sh`), abre `-wallets` carteiras com `100000.00 BRL` (a primeira é
a "quente") e sobe `-concurrency` goroutines por `-duration`. Cada iteração de cada worker segue,
nesta ordem:

1. Com probabilidade `-dup`, reenvia a **própria** última operação com a mesma `Idempotency-Key`
   e o mesmo corpo — exercita o caminho de replay idempotente (HTTP 200).
2. Senão, com probabilidade `-conflict`, reaproveita a `Idempotency-Key` de uma operação recente
   de **qualquer** worker (um buffer circular compartilhado, `recentOps`, protegido por mutex) com
   um valor diferente (`2.00` em vez de `1.00`) — mesma chave, hash diferente. Isso é o que faz o
   servidor responder `409 IDEMPOTENCY_PAYLOAD_MISMATCH` sob concorrência real, não só o replay
   exato que `-dup` já cobre.
3. Caso contrário, gera uma nova operação `BET` de `1.00 BRL` com `externalTransactionId` novo
   (UUID) contra a carteira quente (probabilidade `-hot`) ou uma das demais carteiras escolhida
   uniformemente (probabilidade `1 - hot`). Só depois que essa submissão volta com `201` (já
   processada pelo servidor) é que a operação entra no buffer compartilhado, para que uma futura
   iteração de conflito de qualquer worker sempre mire uma operação que já foi de fato aceita, não
   uma ainda em voo.

A latência de cada requisição entra nos percentis gerais (`## Resultado`) não importa o caminho
que ela seguiu, então um `409` de `-conflict` ou um `200` de `-dup` contam para o p50/p95/p99
tanto quanto um `201` novo.

Ao fim da janela de `-duration`, o gerador espera até `-drain-timeout` (10 min por padrão) a
outbox global drenar (`published_at IS NULL AND dead_at IS NULL` chegar a zero via a contagem
apoiada no índice parcial), depois mede, só para os eventos ocorridos desde o início da carga
(`occurred_at >= início`): quantos existem, quantos foram publicados, quantos foram mortos (DLQ,
`dead_at` preenchido), e o atraso `published_at - occurred_at` em p50/p95/p99
(`percentile_cont`). Publicado e morto são desfechos mutuamente exclusivos; só o que sobra
(`total - publicados - mortos`) está de fato ainda em voo. `percentile_cont` ignora valores nulos,
então uma outbox com eventos ainda em voo produz percentis só sobre o que já publicou — o
relatório declara isso explicitamente e imprime publicados/mortos/total lado a lado, para que a
leitura não confunda "percentil baixo" com "outbox rápida" quando, na verdade, parte dos eventos
mais lentos ainda não tinha `published_at`, nem conte um evento morto como se ainda estivesse
pendente. Por fim chama
`POST /wallets/{id}/reconciliation` em todas as carteiras abertas — qualquer `consistent=false` é
reportado como divergência.

Duas execuções, variando só `-hot`, para isolar o efeito da contenção de escrita numa única
carteira — cada uma num stack novo (`down -v` + `up --build -d --wait`), então nenhuma herda
backlog da outra:

| Flag | Padrão | Execução "quente" |
|---|---|---|
| `-duration` | 60s | 60s |
| `-concurrency` | 32 | 32 |
| `-wallets` | 50 | 50 |
| `-hot` | 0.2 | 0.8 |
| `-dup` | 0.1 | 0.1 |
| `-conflict` | 0.02 | 0.02 |
| `-drain-timeout` | 10m | 10m |

## Resultados

### Execução padrão (`-hot 0.2`)

- Total de requisições: 33101
- Duração efetiva: 60.00048271s
- Throughput: **551.7 req/s**

| Métrica | Valor |
|---|---|
| p50 | 13.8 ms |
| p95 | 328.7 ms |
| p99 | 845.9 ms |
| max | 3755.5 ms |

| Status | Contagem |
|---|---|
| 201 novo | 29206 |
| 200 replay | 3265 |
| 409 conflito | 598 |
| 422 | 0 |
| 5xx | 0 |
| outros HTTP | 0 |
| erro de transporte | 32 |

Contagem bruta por código HTTP: `200=3265, 201=29206, 409=598`.

**Outbox**: 58416 eventos observados desde o início da carga, **58416 publicados (100%)**, mortos
(DLQ): 0 — **drenou completamente** dentro do limite de 10 min (levou cerca de 3 min 39 s depois
do fim da carga). Os percentis cobrem todos os eventos, sem censura.

| Percentil | Atraso (`occurred_at` -> `published_at`) |
|---|---|
| p50 | 91.603 s |
| p95 | 208.966 s |
| p99 | 217.193 s |

**Reconciliação**: 50/50 carteiras com `consistent=true`.

### Execução com contenção (`-hot 0.8`)

- Total de requisições: 12095
- Duração efetiva: 60.001481214s
- Throughput: **201.6 req/s**

| Métrica | Valor |
|---|---|
| p50 | 22.0 ms |
| p95 | 748.2 ms |
| p99 | 1426.0 ms |
| max | 4313.8 ms |

| Status | Contagem |
|---|---|
| 201 novo | 10729 |
| 200 replay | 1127 |
| 409 conflito | 207 |
| 422 | 0 |
| 5xx | 0 |
| outros HTTP | 0 |
| erro de transporte | 32 |

Contagem bruta por código HTTP: `200=1127, 201=10729, 409=207`.

**Outbox**: 21460 eventos observados, **21460 publicados (100%)**, mortos (DLQ): 0 — **drenou
completamente** dentro do limite de 10 min (levou cerca de 2 min 3 s depois do fim da carga, mais
rápido que a execução padrão porque gerou bem menos eventos: 21460 contra 58416).

| Percentil | Atraso (`occurred_at` -> `published_at`) |
|---|---|
| p50 | 68.565 s |
| p95 | 118.051 s |
| p99 | 121.702 s |

**Reconciliação**: 50/50 carteiras com `consistent=true`.

## Leitura

A contenção na carteira quente aparece em latência, throughput **e**, agora, em conflitos: de
`-hot 0.2` para `0.8`, o p99 de latência HTTP sobe de 846 ms para 1426 ms (p95 quase dobra, de
329 ms para 748 ms) e o throughput cai de 551.7 para 201.6 req/s com a mesma concorrência — mais
tempo esperando o `SELECT ... FOR UPDATE` da mesma linha (`internal/app/submit_wager.go`). Ela
**não** aparece como conflito de idempotência por contenção de escrita em si: o `409` observado
(598 e 207 nas duas execuções, ambos próximos dos `~2%` de `-conflict`) vem inteiramente do
mecanismo dedicado do gerador — reenviar a `Idempotency-Key` de uma operação recente com um valor
diferente — não de duas requisições novas colidindo por acaso. Sem esse mecanismo (com só `-dup`),
o `409` fica em zero mesmo sob 80% de tráfego na carteira quente, porque o lock de linha serializa
concorrentes em vez de rejeitá-los.

Os 32 erros de transporte de cada execução batem exatamente com `-concurrency`: é a requisição em
voo de cada worker cancelada quando o contexto de `-duration` expira, não uma falha do serviço
(nenhum `5xx` em nenhuma execução).

A outbox agora drena completamente dentro da janela de 10 min nas duas execuções — o relay
corrigido (ver Ambiente e ADR 0014, seção "Revisão") elimina os
`57014` e drena o backlog de uma carga de 60 s em poucos minutos. Antes dessas mudanças, sob uma
carga parecida (`-hot 0.2`, ~500 req/s), a outbox não drenava (~35 mil eventos pendentes ao fim da
carga) e, com o atraso concentrado na carteira quente, a partição só avançava a cerca de 5,9
eventos/s (um evento por intervalo de polling por relay), o que projetava uma cauda estimada acima
de 25 min. O atraso ainda é alto em termos absolutos: p50 de 91.6 s e p99 de 217.2 s na execução
padrão (551.7 req/s de ingestão), contra p50 de 68.6 s e p99 de 121.7 s na execução quente
(201.6 req/s) — o atraso escala com a taxa de ingestão de eventos, não com `-hot`: a execução
quente tem p99 de latência HTTP pior, mas atraso de outbox melhor, porque gera bem menos eventos
por segundo (menor throughput HTTP). O teto remanescente não é mais o claim da outbox no
PostgreSQL; é o throughput de publicação no LocalStack SNS, que satura (CPU acima de 100%) por
volta de 150–300 publicações/s nesta máquina enquanto a carga chega a ingerir cerca de 1000
eventos/s (ADR 0014, seção "Revisão").

## Limitações

- **LocalStack não é SQS/SNS real**: sem a latência de rede, os limites de throughput e o
  comportamento de scaling de uma fila/tópico gerenciados. O teto de publicação observado
  (~150–300 publishes/s) é um limite do LocalStack local, não da AWS; os números de atraso da
  outbox aqui não predizem o comportamento em produção.
- **Mesma máquina para gerador e sistema**: o gerador de carga, os três `wagerd`, o PostgreSQL, o
  Keycloak e o LocalStack disputam os mesmos 12 CPUs. Throughput e latência não isolam custo de
  cliente e servidor.
- **O `409` é inteiramente do mecanismo dedicado (`-conflict`), não de uma corrida orgânica**: o
  gerador reaproveita deliberadamente uma `Idempotency-Key` recente com outro valor; ele não faz
  duas requisições novas colidirem por acaso na mesma chave. Isso exercita o caminho de
  `IDEMPOTENCY_PAYLOAD_MISMATCH` sob concorrência real (o alvo pode ter sido gerado por outro
  worker), mas não mede a taxa "natural" de conflito de um provedor real reenviando por conta
  própria.
- **O atraso da outbox reportado é por evento publicado, não por carteira**: a carteira quente
  tem um único evento em voo por vez (design serial por partição, ADR 0014), então sob carga
  sustentada ela pode acumular fila própria mesmo com o relay saudável; este teste não separa o
  atraso da carteira quente do atraso médio.
