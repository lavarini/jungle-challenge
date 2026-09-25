# Arquitetura

Visão consolidada. O detalhe está no [desenho da solução](docs/design.md),
e cada decisão tem um ADR em [`docs/adr/`](docs/adr/README.md). A prova de cada garantia está em
[`docs/EVIDENCIAS.md`](docs/EVIDENCIAS.md), gerado a partir da execução dos testes.

## Visão

Um binário, `wagerd`, com papéis escolhidos por `WAGERD_ROLE`: `api`, `consumer`, `outbox-relay`,
`reference-worker` ou `all` ([ADR 0002](docs/adr/0002-monolito-modular-com-papeis-fx.md),
[ADR 0019](docs/adr/0019-papeis-de-worker-separados.md)). O Uber Fx compõe só os módulos do papel.
Não há estado em memória que importe à correção: qualquer número de instâncias de qualquer papel
roda contra o mesmo PostgreSQL, e o banco é o único árbitro.

```text
HTTP (provider/internal) ─┐
                          ├─> SubmitWager ──> PostgreSQL ──(commit)──> outbox_events
SQS wager-transactions ───┘      │              ▲    │                       │
                                 │              │    └─> PENDING_REFERENCE   │
                                 │   reference-worker <──(lease, despertar)  │
                                 │                                           ▼
                                 └─ inbox (mesmo commit)          outbox-relay ──> SNS wallet-events.fifo
```

Camadas: `internal/money`, `wallet`, `wagering` e `events` são domínio puro. `internal/app` tem os
casos de uso e as portas. `internal/adapters` tem Postgres, HTTP, SQS, SNS, OIDC e workers. O
`bootstrap` compõe tudo. O `archtest` impõe a regra de dependência e proíbe ponto flutuante nos
caminhos de dinheiro.

## Composição com Fx

`cmd/wagerd/main.go` carrega e valida toda a configuração (`config.Load`, com todos os erros
agregados de uma vez) antes de montar o `fx.App`: um `WAGERD_ROLE` desconhecido ou uma dependência
faltando para o papel escolhido (por exemplo `outbox-relay` sem `SNS_EVENTS_TOPIC_ARN`) nunca chega
a `fx.New`. O app recebe `fx.StopTimeout(cfg.ShutdownTimeout)` — um único orçamento de tempo para
toda a drenagem, os workers, os servidores e o fechamento do pool.

`bootstrap.Options` monta um `fx.Module` por adapter e um por papel — `api`, `consumer`,
`outbox-relay`, `reference-worker` — e inclui na composição só os módulos do papel pedido em
`WAGERD_ROLE` (`all` soma todos) ([ADR 0002](docs/adr/0002-monolito-modular-com-papeis-fx.md),
[ADR 0019](docs/adr/0019-papeis-de-worker-separados.md)). `internal/bootstrap` é o único pacote do
repositório que importa `go.uber.org/fx`. O domínio (`internal/money`, `internal/wallet`,
`internal/wagering`, `internal/events`) e os casos de uso (`internal/app`) não importam Fx nem
nenhuma biblioteca de infraestrutura — a regra é verificada em teste pelo `archtest`, junto com a
proibição de ponto flutuante nos caminhos de dinheiro.

## Fluxo de uma operação

1. **Entrada.**
   - Pela API: o token OIDC é verificado (RS256, fail-closed) e o `providerId` do corpo tem de ser
     o do token.
   - Pelo SQS: o remetente da mensagem é vinculado a um provider
     ([ADR 0016](docs/adr/0016-credenciais-do-broker-e-vinculo-do-remetente.md)).
2. **Uma transação `READ COMMITTED`.** A ordem de locks é fixa: carteira (`SELECT … FOR UPDATE`),
   a própria transação, pendências dependentes, inbox
   ([ADR 0008](docs/adr/0008-processamento-sincrono-e-ordem-de-locks.md)).
3. **Idempotência** por `(providerId, idempotencyKey)`, conferida antes e depois do lock. O
   mesmo corpo é replay. Corpo diferente é `IDEMPOTENCY_PAYLOAD_MISMATCH`. A mesma operação com
   outra chave é `IDEMPOTENCY_KEY_MISMATCH`
   ([ADR 0007](docs/adr/0007-outra-chave-para-mesma-operacao-e-conflito.md)).
4. **Decisão no domínio.**
   - Aplica, rejeita de forma definitiva (persistida, `422`) ou deixa pendente.
   - A rejeição corrigível não é persistida
     ([ADR 0009](docs/adr/0009-rejeicao-corrigivel-e-definitiva.md)).
   - Uma reversão que chega antes da referência fica `PENDING_REFERENCE` até o prazo
     ([ADR 0010](docs/adr/0010-referencias-pendentes-com-prazo.md)).
5. **Escrita**, sempre nesta ordem: transação, carteira, ledger, outbox, inbox, tudo no mesmo
   commit.
6. **Despertar e publicação.** As pendências que esperavam essa referência são despertadas na
   mesma transação (`next_attempt_at = now`), e o resolvedor as pega logo após o commit. O relay
   publica no SNS FIFO depois do commit.

## Garantias e onde são impostas

| Garantia | Onde | ADR |
|---|---|---|
| Dinheiro exato | `int64` em unidades menores; parsing canônico estrito; `archtest` proíbe float | [0006](docs/adr/0006-money-canonico-estrito.md) |
| Saldo nunca negativo; ledger coerente | domínio **e** banco: trigger de ledger (direção x tipo, cadeia `balance_before/after`, versão +1) e guarda de saldo diferida, todos O(1) | [0004](docs/adr/0004-invariantes-no-banco-com-custo-constante.md) |
| Sem lost update | lock da linha da carteira; versão avança exatamente 1 | [0008](docs/adr/0008-processamento-sincrono-e-ordem-de-locks.md) |
| Uma reversão por aposta | índice único parcial (vaga única) | [0005](docs/adr/0005-vaga-unica-de-reversao.md) |
| Efeito único com entrega repetida | idempotência persistente + inbox no mesmo commit | [0007](docs/adr/0007-outra-chave-para-mesma-operacao-e-conflito.md), [0013](docs/adr/0013-dlq-explicita-para-mensagem-invalida.md) |
| Ledger append-only | papel de runtime `wager_app` sem `UPDATE`/`DELETE`; triggers bloqueiam também o dono | [0004](docs/adr/0004-invariantes-no-banco-com-custo-constante.md) |
| Evento só após commit, nunca perdido | outbox transacional; relay com lease e fencing | [0014](docs/adr/0014-relay-da-outbox-por-cabeca-de-particao.md) |
| Ordem de eventos por carteira | só a cabeça de cada partição é reclamada; SNS FIFO com `MessageGroupId = walletId` | [0012](docs/adr/0012-eventos-de-saida-em-sns-fifo.md), [0014](docs/adr/0014-relay-da-outbox-por-cabeca-de-particao.md) |
| Mensagem inválida não bloqueia a carteira | DLQ explícita com motivo; `RETRIES_EXHAUSTED` antes do redrive nativo | [0013](docs/adr/0013-dlq-explicita-para-mensagem-invalida.md) |
| `FAILED` só por invariante | só `ErrInvariantViolation` leva a `FAILED`; infraestrutura é sempre transitória | [0011](docs/adr/0011-failed-e-violacao-de-invariante.md) |
| Autorização por rota e isolamento entre providers | OIDC fail-closed; leitura fora do escopo responde 404 | [0015](docs/adr/0015-oidc-fail-closed-e-politica-por-rota.md) |
| Privilégio mínimo na AWS | uma política IAM por papel, conferida por teste contra a tabela de ações de cada papel, sem curinga | [0016](docs/adr/0016-credenciais-do-broker-e-vinculo-do-remetente.md) |

## Falhas

| Situação | Comportamento |
|---|---|
| Queda depois do commit, antes de remover a mensagem | reentrega respondida pelo inbox; nenhum débito novo |
| Queda depois de publicar, antes de confirmar | lease expira; republicação com o mesmo `eventId`, deduplicada |
| Queda depois de reclamar uma pendência | lease expira; outra instância retoma |
| PostgreSQL indisponível | HTTP `503` com `retryable=true`; SQS devolve à fila com backoff; workers recuam |
| SNS indisponível ou mal configurado | relay recua até cerca de 5 min (teto mais jitter) sem descartar; no boot recusa tópico inexistente ou não FIFO |
| `SIGTERM` | prontidão vira 503; HTTP e workers drenam em paralelo; mensagem interrompida volta com visibilidade 0; pool fecha por último |

As janelas de queda são provadas com processos reais e failpoints compilados só com
`-tags failpoint` ([ADR 0017](docs/adr/0017-failpoints-por-build-tag.md)).

## Shutdown

Um único `drain` (`internal/bootstrap/drain.go`) orquestra o desligamento. Seu `fx.StopHook` é
registrado depois do hook do pool (que é provido antes de tudo o que o usa), e como o Fx para os
hooks na ordem inversa do registro, o `drain` roda primeiro e o pool só fecha depois que ele
retornar.

A ordem real, dentro de `drain.stop`:

1. **Prontidão vira `503`.** `SetDraining()` é a primeira chamada; a partir daí `/health/ready`
   responde não-pronto.
2. **Espera `SHUTDOWN_READINESS_DELAY`**, limitada a no máximo metade do que resta do orçamento de
   `fx.StopTimeout`, para um balanceador ver o `503` antes de a porta fechar.
3. **HTTP para de aceitar e termina o que está em curso.** O servidor chama `srv.Shutdown(ctx)`, que
   para de aceitar conexões novas e espera as requisições em andamento terminarem; se o prazo
   estourar antes disso, `srv.Close()` força o fechamento.
4. **Workers param de receber e esperam o lote**, em paralelo com o HTTP: `consumer`,
   `reference-worker` e `outbox-relay` primeiro cancelam a busca por trabalho novo e só cancelam o
   trabalho em si se o prazo expirar antes de o lote em andamento terminar.
5. **Mensagens cortadas voltam com visibilidade 0.** Se o cancelamento do trabalho interromper o
   consumidor SQS no meio do tratamento de uma mensagem, ele distingue esse corte (contexto
   cancelado) de uma falha real e devolve a mensagem com `ChangeMessageVisibility` a 0, sem gastar
   uma tentativa de reentrega.
6. **Admin fecha por último entre os componentes da aplicação.** Só depois que HTTP e todos os
   workers retornarem é que o servidor administrativo (métricas, `pprof`, `/health/ready`) para;
   ele fica de pé durante toda a drenagem, inclusive nos papéis sem HTTP público, para que a
   prontidão continue reportando `503` até o fim.
7. **Pool fecha** só então, pelo `fx.StopHook` registrado junto com a conexão.

## Observabilidade

Logs JSON com `correlationId`, `transactionId`, `walletId`, `providerId` e `messageId`, sem
payload financeiro completo nem credenciais. As métricas Prometheus e o `pprof` ficam na porta
administrativa (`ADMIN_ADDR`), que o Compose não publica. Nenhum identificador de alta
cardinalidade aparece em rótulos. O [RUNBOOK](docs/RUNBOOK.md) diz o que fazer com cada sinal.

## Interpretações adotadas

O enunciado deixa decisões em aberto; cada uma virou um ADR:

- **Normalização de `Money` antes do hash.** A entrada externa só aceita o padrão canônico
  (`25.00`, duas casas, sem sinal nem notação científica); não há forma equivalente a normalizar, e
  o hash de idempotência cobre exatamente os bytes aceitos
  ([ADR 0006](docs/adr/0006-money-canonico-estrito.md)).
- **Outra chave para a mesma operação é conflito.** Reenviar `(providerId, externalTransactionId)`
  com uma `Idempotency-Key` diferente da primeira vez não cria uma operação nova: é
  `409 IDEMPOTENCY_KEY_MISMATCH`
  ([ADR 0007](docs/adr/0007-outra-chave-para-mesma-operacao-e-conflito.md)).
- **Prazo de referência pendente.** O critério terminal é um TTL absoluto (`deadline_at`), não o
  número de tentativas de backoff, que por si só encerraria a espera bem antes de qualquer prazo
  declarado ([ADR 0010](docs/adr/0010-referencias-pendentes-com-prazo.md)).
- **Vaga única de reversão.** Cada transação referenciada aceita no máximo uma reversão
  `PROCESSED`, `REFUND` ou `ROLLBACK`, nunca as duas
  ([ADR 0005](docs/adr/0005-vaga-unica-de-reversao.md)).
- **`FAILED` só por violação de invariante.** Indisponibilidade, timeout e conflito de lock são
  sempre transitórios (retry ou DLQ); `FAILED` é reservado ao banco recusar, por constraint ou
  trigger, algo que o domínio já havia aprovado
  ([ADR 0011](docs/adr/0011-failed-e-violacao-de-invariante.md)).
- **Eventos de saída em SNS FIFO, não em fila ponto a ponto.** `WalletBalanceChanged` e os demais
  têm vários interessados (notificação, antifraude, analytics); o relay publica em um tópico com
  fan-out para filas SQS FIFO próprias de cada assinante
  ([ADR 0012](docs/adr/0012-eventos-de-saida-em-sns-fifo.md)).
- **Vínculo remetente→provedor na fila.** A entrada SQS não carrega token OIDC; o consumidor casa o
  `SenderId` da mensagem — uma credencial por provedor — com o `providerId` do envelope antes de
  processar ([ADR 0016](docs/adr/0016-credenciais-do-broker-e-vinculo-do-remetente.md)).

## Limitações conhecidas

- **Uma credencial AWS e um papel de banco por processo, não por papel.** As políticas IAM por
  papel existem e são testadas, mas o Compose local roda o papel `all` com uma só credencial
  (a união das quatro políticas), e o LocalStack não aplica IAM.
- **A ordem de eventos quebra na quarentena.** Um evento em quarentena libera o seguinte da mesma
  carteira. Isso é aceito e documentado no ADR 0014 e no RUNBOOK.
- **Contador de recebimentos do SQS.** Uma mensagem retida atrás de uma cabeça de grupo que falha
  herda recebimentos e pode ir para a DLQ como `RETRIES_EXHAUSTED` na sua primeira falha real. Ela
  vai com o motivo registrado.
- **Duas pendências que se referenciam** em carteiras diferentes e vencem ao mesmo tempo podem
  gerar deadlock no PostgreSQL. O erro é transitório e a operação é retomada.
- **Varredura da outbox.** A consulta de cabeças percorre os eventos não publicados. O custo é
  O(backlog), e LISTEN/NOTIFY é a evolução natural.
- **Healthcheck dos containers da aplicação.** A imagem é distroless e não tem `curl`, então os
  serviços `wagerd` no Compose não têm healthcheck próprio. A prontidão é verificada pelos testes e
  pelo smoke.
- **Requisição ainda não aceita pelo SO no instante do `SIGTERM`.** O teste E2E
  (`TestSIGTERMDrainsAndExitsCleanly`, `test/e2e/crash/crash_test.go`) prova que toda requisição já
  despachada antes do sinal termina com sucesso; uma conexão ainda na fila de aceitação do listener
  no instante exato do sinal não tem esse cenário coberto por teste.

## Não concluído

- Teste de indisponibilidade temporária do PostgreSQL e do SQS (enunciado §3): o comportamento —
  `503` retentável, mensagem devolvida à fila, sem efeito duplicado — está implementado (ver
  `Falhas`), mas ainda não é exercitado por um teste que pause o banco ou o broker de verdade.
- Um processo por papel, cada um com sua própria credencial AWS, não é exercitado no Compose (ver
  `Limitações conhecidas`); só o papel `all` roda ali.
- Teste de carga (opcional, ver [ADR 0003](docs/adr/0003-execucao-em-fatias-verticais.md)).
- Tracing com OpenTelemetry e dashboards (diferenciais opcionais do enunciado).
