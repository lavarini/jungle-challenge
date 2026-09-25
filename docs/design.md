# Desenho da solução

**Insumos:** enunciado ([`docs/DESAFIO.md`](DESAFIO.md)), descrição da vaga.
**Decisões:** [ADRs](adr/README.md).

## Postura

Núcleo enxuto com profundidade escolhida ([ADR 0001](adr/0001-nucleo-enxuto-com-profundidade-escolhida.md)).
Execução em esqueleto andante e fatias verticais ([ADR 0003](adr/0003-execucao-em-fatias-verticais.md)).

## 1. Arquitetura e módulos

Monólito modular, um binário com papéis Fx ([ADR 0002](adr/0002-monolito-modular-com-papeis-fx.md)).

```text
cmd/wagerd            fx.New(bootstrap.For(role))
                      role = api | consumer | outbox-relay | reference-worker | all
                      subcomando `migrate up|down`
internal/
  money/              shared kernel: Money, Currency (só stdlib)
  wallet/             contexto Wallet/Ledger: agregado Wallet, LedgerEntry, erros
  wagering/           contexto Wagering: WagerTransaction, máquina de estados, regras por kind
  app/                casos de uso e portas: OpenWallet, SubmitWager, ResolvePending,
                      Reconcile, consultas; UnitOfWork
  adapters/
    postgres/         pgx, SQL explícito, UoW, repositórios
    httpapi/          rotas, decode estrito, mapeamento de erro para HTTP
    oidc/             verificação JWT/JWKS para Principal
    sqsin/            consumidor de wager-transactions.fifo (inbox)
    outbox/           relay para SNS FIFO
  platform/           config, logger, métricas, health, pprof
  bootstrap/          único pacote que importa fx; um fx.Module por adapter e papel
migrations/
deploy/               realm Keycloak, init LocalStack
test/integration/     infraestrutura real via testcontainers
test/e2e/             três processos independentes
docs/adr/
```

### Regra de dependência

| Pacote | Pode importar |
|---|---|
| `money`, `wallet`, `wagering` | stdlib (e `money`) |
| `app` | domínio e as próprias portas |
| `adapters/*` | `app`, domínio, bibliotecas de infraestrutura |
| `bootstrap` | tudo |

Verificada por um teste de arquitetura baseado em `go list -deps`, executado em
`go test ./...`.

### Papéis

| Papel | Módulos |
|---|---|
| `api` | HTTP |
| `consumer` | consumidor SQS |
| `outbox-relay` | relay da outbox |
| `reference-worker` | resolvedor de pendências |
| `all` | todos |

Compose padrão sobe `all`; profile `multi` sobe três instâncias `all`
independentes, o mínimo que o enunciado exige
([ADR 0019](adr/0019-papeis-de-worker-separados.md)).

### Stack

Go 1.27.x (fixado em `go.mod` e Dockerfile); `net/http` com padrões de rota;
`pgx` v5 com SQL explícito; `golang-migrate`; `go-oidc`; AWS SDK v2; `slog` JSON;
Prometheus; `testcontainers-go`.

## 2. Modelo de dados e invariantes no banco

Invariantes no banco apenas com custo O(1) por operação
([ADR 0004](adr/0004-invariantes-no-banco-com-custo-constante.md)).

### Money na persistência

`amount_minor BIGINT` + `currency CHAR(3)`. Entrada externa aceita somente a
forma canônica `^(0|[1-9][0-9]*)\.[0-9]{2}$`
([ADR 0006](adr/0006-money-canonico-estrito.md)).

### Tabelas

```text
wallets             id, player_id, currency, balance_minor >= 0, version >= 1, timestamps
                    UNIQUE (player_id, currency)

wager_transactions  id, origin (INTERNAL|EXTERNAL), kind, status, wallet_id, player_id,
                    currency, amount_minor >= 0,
                    provider_id, external_id, idempotency_key, payload_hash, round_id, game_id,
                    reference_external_id, reference_tx_id (FK self),
                    failure_code, result_balance_minor, result_wallet_version,
                    attempts, next_attempt_at, deadline_at, timestamps, completed_at

wallet_ledger_entries
                    id, seq (cursor), wallet_id, transaction_id (FK), direction,
                    amount_minor > 0, currency, balance_before, balance_after >= 0,
                    wallet_version, created_at

inbox_messages      (consumer_name, message_id) PK, payload_hash, transaction_id, outcome,
                    received_at, completed_at

outbox_events       seq, event_id UNIQUE, partition_key (walletId), event_type, aggregate_id,
                    payload JSONB, occurred_at, attempts, next_attempt_at,
                    claim_id, claim_expires_at, published_at, dead_at, last_error
```

### Invariantes declarativas

| Invariante | Mecanismo |
|---|---|
| Uma carteira por jogador e moeda | `UNIQUE (player_id, currency)` |
| Saldo não negativo | `CHECK (balance_minor >= 0)` |
| Forma interna x externa | `CHECK` de origem: `OPENING` sem campos de provedor; externa com todos |
| Valor por tipo | `CHECK`: `LOSS` = 0, demais > 0; `REFUND`/`ROLLBACK` exigem referência |
| Terminalidade | `CHECK`: terminal exige `completed_at`; `REJECTED`/`FAILED` exigem `failure_code` |
| Operação externa única | `UNIQUE (provider_id, external_id)` parcial |
| Chave de idempotência única | `UNIQUE (provider_id, idempotency_key)` parcial |
| Uma abertura por carteira | `UNIQUE (wallet_id) WHERE kind = 'OPENING'` |
| Vaga única de reversão | `UNIQUE (reference_tx_id) WHERE status = 'PROCESSED' AND kind IN ('REFUND','ROLLBACK')` |
| Um lançamento por transação | `UNIQUE (wallet_id, transaction_id)` e `UNIQUE (transaction_id)` |
| Aritmética do lançamento | `CHECK` por direção: `after = before +/- amount` |
| Encadeamento | `UNIQUE (wallet_id, wallet_version)` |
| Ledger imutável | trigger contra `UPDATE`/`DELETE`/`TRUNCATE` e `REVOKE` para runtime |
| Transação terminal imutável | trigger em `wager_transactions`; identidade e snapshot congelados |
| Snapshot da outbox imutável | trigger; só colunas de entrega mudam |

### Triggers de profundidade escolhida

1. **`wallet_ledger_entries BEFORE INSERT`** — por busca de chave, confere carteira,
   moeda e valor contra a transação; direção coerente com o `kind` (`BET` débito;
   `WIN`, `REFUND`, `OPENING` crédito; `ROLLBACK` oposto do lançamento da
   referência); `balance_after` e `wallet_version` iguais aos da carteira já
   atualizada; `balance_before` igual ao `balance_after` da versão anterior, ou
   zero no primeiro lançamento.
2. **`wallets` constraint trigger `DEFERRABLE INITIALLY DEFERRED`** — no commit,
   toda mudança de saldo exige `version = old + 1` e um lançamento em
   `(wallet_id, version)` com `balance_after` igual ao saldo. Na inserção, saldo
   maior que zero exige o lançamento de abertura na versão 1; saldo zero não
   admite lançamento.

Juntos: saldo só muda com lançamento coerente; ledger só cresce com o saldo
acompanhando.

### Reversões

Vaga única por transação referenciada
([ADR 0005](adr/0005-vaga-unica-de-reversao.md)).

### Idempotência: mesma operação, outra chave

`(provider, externalTransactionId)` existente com outra `Idempotency-Key`
resulta em `409 IDEMPOTENCY_KEY_MISMATCH`, com o `transactionId` existente
([ADR 0007](adr/0007-outra-chave-para-mesma-operacao-e-conflito.md)).

### Roles

- `wager_migrator` — dono do schema e dos triggers; usado só por migrations.
- `wager_app` — runtime: `SELECT`/`INSERT`; `UPDATE` restrito por coluna
  (saldo e versão; estado e resultado; entrega da outbox; conclusão da inbox);
  sem `UPDATE`/`DELETE` no ledger, sem `TRUNCATE`, não é dono. Provado por
  teste executado com a própria role.

## 3. Fluxos e fronteiras transacionais

### SubmitWager

Um caso de uso para HTTP, SQS e o resolvedor de pendências
([ADR 0008](adr/0008-processamento-sincrono-e-ordem-de-locks.md)).

```text
borda   HTTP: token -> Principal; body.providerId != principal -> 403
        SQS : envelope válido; provider autorizado para o remetente (seção 5)
        -> Command canônico + SHA-256 do JSON canônico (chaves ordenadas)
           campos: providerId, externalTransactionId, playerId, walletId, roundId,
                   gameId, kind, money{amount,currency}, referenceExternalTransactionId?
           fora do hash: idempotencyKey, messageId, correlationId, headers

BEGIN (READ COMMITTED; lock_timeout e statement_timeout configurados)
  1. [SQS] inbox por (consumer, messageId): concluída -> desfecho gravado; hash diferente -> conflito
  2. idempotência sem lock: encontrada -> replay ou 409          (caminho rápido)
  3. SELECT wallet FOR UPDATE
  4. idempotência de novo, serializada pela carteira
  5. resolve referência (REFUND/ROLLBACK obrigatória; WIN opcional)
  6. wagering.Decide(tx, wallet, ref) -> Outcome                 (domínio puro)
  7. INSERT tx em estado final -> UPDATE wallet -> INSERT ledger -> INSERT outbox
     -> acorda pendências que esperavam esta tx -> [SQS] INSERT inbox concluída
COMMIT  (violação de unicidade -> rollback e um retry, que vira replay)
```

- `PENDING` existe só em memória; o único estado não terminal persistido é
  `PENDING_REFERENCE`, que tem retomada durável.
- Ordem de locks em todo caminho: carteira, depois transação.
- Falha no commit é resultado ambíguo: `503` retentável; o reenvio descobre o
  desfecho pela idempotência.

### Rejeição corrigível x definitiva

Corrigível não é persistido; definitivo é persistido
([ADR 0009](adr/0009-rejeicao-corrigivel-e-definitiva.md)).

| Classe | Exemplos | Persiste | HTTP |
|---|---|---|---|
| Corrigível | JSON inválido, formato de `Money`, `kind=OPENING`, carteira inexistente, jogador ou moeda divergente | não; chave livre | `400` / `404` |
| Definitiva | `BET_INSUFFICIENT_FUNDS`, `REVERSAL_INSUFFICIENT_FUNDS`, `REVERSAL_ALREADY_APPLIED`, `REFERENCE_MISMATCH`, `REFERENCE_NOT_PROCESSED`, `REFERENCE_NOT_FOUND` | sim, `REJECTED` + evento; replay devolve a rejeição | `422` |

### Referências pendentes

([ADR 0010](adr/0010-referencias-pendentes-com-prazo.md))

| Referência | Resultado |
|---|---|
| inexistente ou `PENDING_REFERENCE` | `PENDING_REFERENCE` + evento, `202` |
| `REJECTED` ou `FAILED` | `REJECTED` / `REFERENCE_NOT_PROCESSED` |
| `PROCESSED` divergente (provider, jogador, carteira, moeda, rodada, valor, tipo) | `REJECTED` / `REFERENCE_MISMATCH` |
| vaga de reversão ocupada | `REJECTED` / `REVERSAL_ALREADY_APPLIED` |

- Critério terminal único: `deadline_at` (TTL padrão 24 h, configurável).
  Expirado: `REJECTED` / `REFERENCE_NOT_FOUND` + evento.
- Backoff exponencial com jitter: 1 s, 2 s, 4 s... teto de 5 min.
- Despertar: tx que termina `PROCESSED` faz `next_attempt_at = now()` nas
  pendências que a referenciam, no mesmo commit.
- Worker: transação curta reivindica com `FOR UPDATE SKIP LOCKED` e grava lease
  em `next_attempt_at`; cada item processa em transação própria (carteira
  `FOR UPDATE`, tx `FOR UPDATE`, reconfere o estado, mesmo `Decide`).

### FAILED

Violação de invariante detectada pelo banco em caminho assíncrono
([ADR 0011](adr/0011-failed-e-violacao-de-invariante.md)). O worker grava
`FAILED` / `INVARIANT_VIOLATION` em transação separada, emite métrica de alerta
e não tenta de novo. No caminho síncrono: `500`, rollback e métrica.
Indisponibilidade de infraestrutura nunca vira `FAILED`.

### Demais fluxos

- `POST /wallets` (identidade interna): `INSERT wallet` versão 1; saldo > 0
  gera `OPENING PROCESSED`, crédito e dois eventos no mesmo commit; duplicata
  `409 WALLET_ALREADY_EXISTS`.
- Reconciliação: `REPEATABLE READ READ ONLY`; saldo armazenado x
  `SUM(créditos) - SUM(débitos)` lido como `numeric` e convertido com checagem
  de overflow; divergência na resposta, no log e em métrica; nada é alterado.
- Ledger: cursor opaco `base64(seq)`, `ORDER BY seq`, `limit <= 100`.

### Mapa HTTP

`application/problem+json` (RFC 9457) com `code` e `retryable`.

| Situação | Código |
|---|---|
| Processada agora | `201` |
| Replay | status do original (`201` vira `200`; `202`; `422`) com `idempotentReplay: true` |
| Pendente | `202` |
| Rejeição persistida | `422` |
| Entrada inválida | `400` |
| Recurso inexistente | `404` |
| Conflito de idempotência ou carteira duplicada | `409` |
| Credencial ausente ou inválida | `401` |
| Sem permissão | `403` |
| Transitório (banco, `lock_timeout`, commit ambíguo) | `503` + `Retry-After` |
| Invariante violada | `500` |

## 4. Mensageria

### Entrada: `wager-transactions.fifo`

- DLQ `wager-transactions-dlq.fifo`, redrive nativo `maxReceiveCount = 20` só como rede de segurança, visibility
  timeout 30 s, long polling 20 s, lotes de até 10.
- Contrato do produtor: `MessageGroupId = walletId`,
  `MessageDeduplicationId = messageId` do envelope. Otimizações de transporte;
  a correção vem do banco.
- Lote agrupado por `MessageGroupId`: grupos em paralelo, sequencial dentro do
  grupo. Falha transitória na cabeça de um grupo libera as seguintes do mesmo
  grupo sem processar (visibilidade 0).
- `consumer_name` lógico e estável: `wager-transactions-consumer`.

| Desfecho | Ação |
|---|---|
| Processada, replay, rejeição definitiva, `PENDING_REFERENCE` | commit, depois `DeleteMessage` |
| Inválida ou corrigível (JSON, campos, `Money`, `OPENING`, carteira inexistente, conflito de idempotência, hash de inbox divergente, provider não autorizado) | `SendMessage` à DLQ com `failureCode` e `reason`, depois `DeleteMessage` ([ADR 0013](adr/0013-dlq-explicita-para-mensagem-invalida.md)) |
| Transitória (banco, `lock_timeout`) | `ChangeMessageVisibility` com backoff por `ApproximateReceiveCount` (2^n s, teto 60 s); ao atingir `SQS_MAX_RECEIVES` (5), o consumidor envia à DLQ com `failureCode=RETRIES_EXHAUSTED` ([ADR 0013](adr/0013-dlq-explicita-para-mensagem-invalida.md)) |

Em `SIGTERM`: cancela o receive; espera mensagens em curso até prazo menor que
o `StopTimeout` do Fx; o que não terminou sofre rollback e volta à visibilidade
0. O pool fecha depois.

### Saída: outbox, SNS FIFO e assinantes

([ADR 0012](adr/0012-eventos-de-saida-em-sns-fifo.md))

- Tópico `wallet-events.fifo`; fila assinante `wallet-events-audit.fifo` com
  raw delivery (consumidor de demonstração e sonda de testes).
- `MessageGroupId = walletId`, `MessageDeduplicationId = eventId`, atributo
  `eventType` para filter policies.
- Publicação atrás da porta `EventPublisher`; se o LocalStack não suportar SNS
  FIFO, o adapter troca para SQS FIFO direto sem tocar no relay.

Relay ([ADR 0014](adr/0014-relay-da-outbox-por-cabeca-de-particao.md)):

```text
claim   (tx curta)  só a cabeça não publicada de cada partition_key, com
                    next_attempt_at <= now e sem lease vigente; SKIP LOCKED
                    -> claim_id, claim_expires_at = now + 30 s, attempts++; COMMIT
publish (sem tx)    SNS Publish, um por evento, limitado ao lease
ack     (tx curta)  published_at = now WHERE seq = $1 AND claim_id = $2   (fencing)
falha   (tx curta)  next_attempt_at = now + backoff com jitter (teto 5 min),
                    limpa lease WHERE claim_id = $2
```

- Erro transitório: retry indefinido com backoff limitado. Tópico ausente ou sem
  permissão conta como transitório; o boot do relay recusa tópico inexistente ou
  não FIFO. Erro permanente (formato da requisição) após N tentativas: `dead_at`, partição segue, métrica `outbox_dead_total`.
- Queda entre publish e ack: republicação com o mesmo `eventId`; assinantes
  deduplicam por `eventId`.
- Lag: gauge `now - occurred_at` do mais antigo não publicado. Polling de
  500 ms configurável.

### Eventos

Envelope: `eventId` (UUIDv7), `eventType`, `aggregateId`, `correlationId`,
`causationId?`, `occurredAt` (RFC 3339 UTC), `version`, `data` tipado. Um
construtor por evento define tipo e versão; payload gravado como snapshot JSONB
imutável.

| Evento | `aggregateId` | Quando |
|---|---|---|
| `WagerTransactionProcessed` | transactionId | sucesso, inclusive `LOSS` e `OPENING` |
| `WagerTransactionRejected` | transactionId | rejeição definitiva |
| `WagerTransactionPendingReference` | transactionId | entrada em espera |
| `WalletBalanceChanged` | walletId | saldo mudou: `walletId`, `transactionId`, `direction`, `money`, `balanceBefore`, `balanceAfter`, `walletVersion` |

`partition_key` é sempre `walletId`. `correlationId`: HTTP `X-Correlation-Id`
ou gerado; SQS `messageId`; worker herda o da transação. `causationId`: vazio no
HTTP; `messageId` no SQS; `transactionId` que despertou a pendência no worker.

## 5. Segurança

### IdP

Keycloak com realm importado no boot
([ADR 0015](adr/0015-oidc-fail-closed-e-politica-por-rota.md)).

| Client (`client_credentials`) | Claims e roles |
|---|---|
| `provider-a`, `provider-b` | `provider_id` fixo por mapper; role `wager:provider` |
| `wallet-internal` | role `wallet:internal`; sem `provider_id` |

Mapper de audience: `aud = wagering-api`. Segredos só no realm local e no
`.env.example`, marcados como valores de desenvolvimento.

### Validação

- `go-oidc` com discovery e JWKS em cache com rotação; somente `RS256`; confere
  `iss`, `aud`, `exp`, `nbf` com tolerância de 30 s.
- `Principal{subject, clientId, providerId, roles}` tipado no `context`.
- Fail-closed: configuração ausente ou discovery indisponível no boot impede o
  processo de subir. Não existe modo com autenticação desligada.
- Issuer único: `KC_HOSTNAME` fixo, mesmo issuer para app e testes.

### Política por rota

| Rota | Acesso | Recusa |
|---|---|---|
| `POST /wallets`, `GET /wallets/:id`, `.../ledger`, `.../reconciliation` | `wallet:internal` | `403` |
| `POST /wagering/transactions` | `wager:provider` | `body.providerId != token` -> `403 PROVIDER_MISMATCH` antes de qualquer I/O |
| `GET /wagering/transactions/:id` | provider: próprias; interno: todas | de outro provider -> `404` |
| `GET /providers/:providerId/wagering/transactions/:extId` | provider: próprio path; interno: todos | outro provider -> `403` |
| `/health/live`, `/health/ready` | públicas | |
| `/metrics`, `/debug/pprof` | porta administrativa separada, fora do mapeamento público | |

- Interno não submete apostas; provider não opera carteira.
- Índices de idempotência incluem `provider_id`: replay entre providers é
  impossível por construção.
- Toda recusa de autenticação ou autorização deixa zero linhas novas em
  `wager_transactions`, `wallet_ledger_entries` e `outbox_events` (testado).

### Broker

([ADR 0016](adr/0016-credenciais-do-broker-e-vinculo-do-remetente.md))

- Credenciais por ator: consumer, relay e produtor de cada provider.
- Políticas IAM e de fila em `deploy/iam/`: produtor só `SendMessage` na
  entrada; consumer `Receive`/`Delete`/`ChangeMessageVisibility` na entrada e
  `SendMessage` na DLQ; relay só `Publish` no tópico.
- Vínculo remetente -> provider no consumer: `SenderId` (preenchido pelo SQS,
  fora do controle do cliente) mapeado por configuração para `providerId`;
  divergência vai à DLQ com `PROVIDER_NOT_AUTHORIZED`.
- Limitação: LocalStack Community não impõe IAM. Políticas são entregues e
  anexadas; o teste prova a checagem do consumer. Se o `SenderId` não derivar
  da access key, a checagem fica coberta por teste unitário e a limitação é
  registrada.

### Banco

`wager_migrator` para migrations, `wager_app` para runtime, criadas por script
de init do Postgres. O app nunca conecta como superuser.

### Endurecimento

Body limitado a 64 KB; decode JSON estrito (sem campos desconhecidos, um objeto,
nada após o body); timeouts do `http.Server`; logs sem `Authorization`, token ou
payload financeiro completo; container `read_only`, `cap_drop: ALL`,
`no-new-privileges`.

Fora do escopo: Secrets Manager, TLS, imposição real de IAM.

## 6. Testes e evidência

### Camadas

| Camada | Comando | Infra | Cobertura |
|---|---|---|---|
| Unitário | `go test ./...`, `go test -race ./...` | nenhuma | `Money` (parsing, escala, overflow, moedas); invariantes de `Wallet`; máquina de estados; `Decide` em tabela para os cinco tipos, política de zero e reversões; construtores de eventos; mesmo hash para HTTP e SQS; mapa erro -> HTTP; verifier OIDC contra JWKS local (expirado, `aud` errado, `alg=none`, HS256); teste de arquitetura; `fx.ValidateApp` por papel |
| Integração | `go test -tags=integration ./...` | Postgres, Keycloak, LocalStack via testcontainers | cada constraint e trigger atacado em SQL com `wager_app`; migrations up, down, up; atomicidade; 50 idênticos -> um débito; 80+80/100; inbox e reentrega; DLQ inválida x transitória; pendência resolvida e expirada; `FAILED`; tokens reais ausente, inválido, expirado (client com lifespan de 2 s); isolamento entre providers; zero linhas em recusas; lifecycle Fx com `goleak` |
| E2E | `go test -tags=e2e ./test/e2e/...` | mesma infra e três binários independentes | 80+80 entre processos; 50 duplicatas entre processos misturando HTTP e SQS; `kill -9` após commit e antes do ack; `kill -9` entre publish e ack da outbox (mesmo `eventId`, ordem por carteira); restart preservando idempotência e pendências; `SIGTERM` gracioso; reconciliação final de todas as carteiras |

### Paralelismo entre carteiras

Determinístico: o teste mantém o lock da carteira A numa transação aberta e
verifica que a carteira B commita nesse intervalo.

### Failpoints

Pacote `failpoint` com pontos nomeados (`consumer.after_commit`,
`outbox.after_publish`, `resolver.after_claim`), compilado só com
`-tags failpoint`; sem a tag é no-op
([ADR 0017](adr/0017-failpoints-por-build-tag.md)). O e2e usa
`FAILPOINT=consumer.after_commit=exit` para interromper o processo na janela
exata.

### Evidência executada

([ADR 0018](adr/0018-evidencia-executada-e-gerada.md))

- CI no GitHub Actions a cada push: gofmt, vet, unit com `-race`, integração e
  e2e. Badge no README.
- `docs/EVIDENCIAS.md` gerado por `scripts/evidence.sh` a partir de
  `go test -json`: requisito do enunciado -> teste -> resultado -> duração, com
  commit e versões.

### Definição de pronto por tarefa

- código, migration, configuração e ADR atualizados quando afetados;
- teste proporcional à garantia introduzida, na camada em que ela é observável;
- comando de verificação reproduzível, executado e com saída registrada;
- nenhum caminho de produção simulado em memória nem infraestrutura inteira
  substituída por mock;
- review independente aprovado contra a spec;
- nenhuma sofisticação fora do escopo sem ADR.

### Comandos

`make test`, `make test-race`, `make vet`, `make lint`, `make test-integration`,
`make test-e2e`, `make up`, `make up-multi`, `make evidence`.

### Se sobrar tempo

Teste de carga com k6 em container (throughput, p50/p95/p99, erros, conflitos,
lag da outbox; cenários de carteiras quentes e espalhadas) e perfil `pprof`
coletado durante a carga, comentado no `ARCHITECTURE.md`.

## 7. Observabilidade

- **Logs:** `slog` JSON. Um logger por requisição ou mensagem carrega, quando
  disponíveis, `correlationId`, `causationId`, `messageId`, `transactionId`,
  `walletId`, `providerId`, `eventId`, `component`, `outcome`. Erros levam
  `code` estável e `class` (`business`, `transient`, `permanent`), com causa
  técnica sanitizada. Nunca registra `Authorization`, tokens nem payload
  financeiro completo.
- **Métricas** (Prometheus, porta administrativa). Nenhum identificador de alta
  cardinalidade (`walletId`, `providerId`, `transactionId`, `eventId`) em
  rótulos; esses ficam nos logs.

| Métrica | Tipo | Rótulos |
|---|---|---|
| `wager_transactions_total` | counter | `kind`, `status`, `source` |
| `wager_idempotent_replays_total` | counter | `source` |
| `inbox_duplicates_total` | counter | |
| `wallet_lock_conflicts_total` | counter | `reason` (`lock_timeout`, `unique_retry`) |
| `wager_processing_seconds` | histogram | `source`, `status` |
| `sqs_retries_total` | counter | |
| `sqs_dlq_total` | counter | `reason` |
| `pending_references` | gauge | |
| `outbox_pending`, `outbox_lag_seconds` | gauge | |
| `outbox_publish_total` | counter | `result` |
| `outbox_dead_total` | counter | |
| `invariant_violations_total` | counter | `path` |
| `reconciliation_divergences_total` | counter | |

- **Health:** `/health/live` responde enquanto o processo está vivo;
  `/health/ready` verifica Postgres (`ping`) e SQS (`GetQueueAttributes`) com
  timeout de 2 s e passa a `503` no início do shutdown, antes de drenar.
- Tracing OpenTelemetry fora do escopo (ADR 0003).

## 8. Entregáveis de documentação

| Artefato | Conteúdo |
|---|---|
| `README.md` | badge de CI; pré-requisitos; variáveis; subida; filas e tópico; migrations up e down; exemplos autenticados; comandos de teste |
| `ARCHITECTURE.md` | curto e consolidado: visão, fluxos, garantias, limitações e trabalho não concluído; cada decisão aponta para seu ADR |
| `docs/adr/` | uma decisão por arquivo |
| `docs/openapi.yaml` | contrato HTTP completo: rotas, esquemas, códigos, `problem+json`, `failureCode` por classe, segurança |
| `docs/eventos.md` | contrato dos eventos: envelope, tipos, versão, roteamento (atributo `eventType`), deduplicação por `eventId` |
| `docs/RUNBOOK.md` | procedimentos: mensagem na DLQ, evento em quarentena na outbox, transação `FAILED`, divergência de reconciliação, pendência próxima do prazo |
| `docs/EVIDENCIAS.md` | gerado (ADR 0018) |
| `.env.example` | valores locais, sem segredos reais |
