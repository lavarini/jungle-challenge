# Runbook

Procedimentos para as seis situações em que o serviço precisa de uma pessoa. As métricas estão na
porta administrativa (`ADMIN_ADDR`, padrão `:9090`, `/metrics`), que o Compose não publica. Os logs
são JSON e trazem `correlationId`, `transactionId`, `walletId`, `providerId` e `messageId` onde
existirem.

Duas regras valem para tudo abaixo:

- **Nunca editar saldo ou ledger à mão.** O ledger é append-only: o papel de runtime não consegue
  alterá-lo, e os triggers bloqueiam também o dono. Toda correção é uma nova operação pela API.
- **Reenviar é seguro.** Reenviar uma operação com a mesma `idempotencyKey`, ou uma mensagem com o
  mesmo `messageId` e o mesmo corpo, nunca gera efeito financeiro duplicado.

## 1. Mensagem na DLQ

**Sinal:** `sqs_dlq_total{reason}` sobe; `wager-transactions-dlq.fifo` tem mensagens.

1. Ler a mensagem. Os atributos `failureCode` e `reason` dizem por que ela foi separada, e o corpo é
   o original.
2. Agir conforme o motivo:

| `failureCode` | Significado | Ação |
|---|---|---|
| `INVALID_MESSAGE` | JSON inválido, campo ausente, `Money` inválido, `kind=OPENING` | corrigir no produtor; reenviar com **novo** `messageId` |
| `PROVIDER_NOT_AUTHORIZED` | a credencial remetente não está vinculada ao `providerId` do corpo | conferir `SQS_SENDER_PROVIDERS`; pode ser erro de integração ou tentativa de se passar por outro provider |
| `WALLET_NOT_FOUND`, `WALLET_MISMATCH` | carteira inexistente, ou jogador/moeda divergentes | abrir ou corrigir a carteira e reenviar |
| `IDEMPOTENCY_PAYLOAD_MISMATCH`, `IDEMPOTENCY_KEY_MISMATCH` | chave reutilizada com outro corpo, ou operação já registrada com outra chave | o erro é do produtor; não reenviar como está |
| `INBOX_PAYLOAD_MISMATCH` | mesmo `messageId` com corpo diferente | o produtor reaproveitou um id; se a operação for legítima, reenviar com novo `messageId` |
| `INVARIANT_VIOLATION` | o banco recusou o efeito | seção 3 |
| `RETRIES_EXHAUSTED` | falha transitória até `SQS_MAX_RECEIVES` | ver o `reason`; com o banco ou o broker de volta, devolver a mensagem à fila de entrada |

3. Para reprocessar, enviar o corpo de volta a `wager-transactions.fifo` com o mesmo
   `MessageGroupId` (a carteira). A idempotência torna a duplicata inofensiva.

## 2. Evento em quarentena

**Sinal:** `outbox_dead_total` sobe; log `event quarantined` com o `eventId` e o erro.

O relay só coloca um evento em quarentena por erro **permanente** do SNS (requisição malformada)
depois de 5 tentativas. Tópico ausente ou falta de permissão são transitórios e nunca levam à
quarentena. A quarentena libera o próximo evento da mesma carteira, e por isso a ordem dos eventos
daquela carteira fica quebrada nesse ponto ([ADR 0014](adr/0014-relay-da-outbox-por-cabeca-de-particao.md)).

1. Achar a linha: `SELECT seq, event_id, event_type, partition_key, last_error, attempts FROM outbox_events WHERE dead_at IS NOT NULL ORDER BY seq;`
2. Corrigir a causa. O conteúdo do evento é imutável (trigger `outbox_events_guard`). Se o
   problema estiver no próprio payload, o evento não pode ser republicado como está, e a correção é
   comunicar os assinantes.
3. Com a causa fora do payload (atributo, limite de tamanho, configuração), alguém com o papel
   `wager_migrator` limpa `dead_at` e zera `attempts` naquela linha. O relay volta a tratá-la como
   a cabeça da partição. O `eventId` não muda, então os assinantes deduplicam.
4. Avisar os consumidores de `wallet-events.fifo` que aquele evento chega fora de ordem para a
   carteira.

## 3. Transação `FAILED` ou violação de invariante

**Sinal:** `reference_failed_total` ou `invariant_violations_total{path}` sobem; log `ERROR` com
`class=permanent`.

`FAILED` significa que o banco recusou uma operação pendente por quebrar uma invariante: saldo
negativo, cadeia quebrada ou direção incompatível com o tipo
([ADR 0011](adr/0011-failed-so-para-violacao-de-invariante.md)). Nunca é causado por
infraestrutura. É um bug de código ou de dados.

1. **Não** reprocessar. A operação fica `FAILED` e nada foi movimentado.
2. Coletar a transação (`GET /wagering/transactions/{id}`), o ledger da carteira
   (`GET /wallets/{walletId}/ledger`) e o log do `correlationId`.
3. Rodar a reconciliação da carteira (seção 4) para confirmar que ela está consistente.
4. Abrir incidente. O provider recebe a informação de que a operação não foi aplicada. Se ela for
   legítima, uma nova operação com nova chave segue pela API depois da correção.

## 4. Divergência de reconciliação

**Sinal:** `reconciliation_divergences_total` sobe, ou `POST /wallets/{walletId}/reconciliation`
devolve `consistent: false`.

A reconciliação compara o saldo armazenado com créditos menos débitos do ledger. Também compara o
último `balanceAfter`, a versão e a moeda de cada lançamento, tudo num mesmo snapshot
(`REPEATABLE READ`), então uma operação concorrente não produz divergência falsa.

1. Repetir. Se passar agora, abrir issue sobre a métrica: o snapshot exclui corrida.
2. Se persistir, **congelar a carteira no provider** (parar de aceitar apostas) e abrir incidente
   Sev-1. Com os triggers do banco, uma divergência significa alteração direta com papel de dono ou
   migration que quebrou uma invariante.
3. Ler `difference`, `versionMismatch`, `chainMismatch` e `currencyMismatches` na resposta. Achar o
   primeiro lançamento divergente percorrendo o ledger na ordem de `walletVersion`.

## 5. Pendência próxima do prazo

**Sinal:** `pending_references_near_deadline` > 0, ou `pending_references` crescendo.

Um `REFUND` ou `ROLLBACK` que chegou antes da referência espera até `deadline_at` (TTL
`REFERENCE_TTL`, padrão 24 h). Quando a referência é commitada, a pendência é despertada na hora.
Vencido o prazo, vira `REJECTED` com `REFERENCE_NOT_FOUND`.

1. Listar: `SELECT id, provider_id, external_id, reference_external_id, deadline_at FROM wager_transactions WHERE status = 'PENDING_REFERENCE' ORDER BY deadline_at;`
2. Perguntar ao provider se a referência foi enviada. Se ela se perdeu, precisa ser reenviada, e a
   reversão se resolve sozinha quando ela chegar.
3. Se `pending_references` cresce sem prazo próximo, conferir se algum processo roda o papel
   `reference-worker` e olhar `reference_lock_conflicts_total`.

## 6. Backlog da outbox

**Sinal:** `outbox_lag_seconds` ou `outbox_pending` crescendo; `outbox_publish_total{result="retried"}`
subindo.

1. `retried` alto: SNS indisponível ou credencial sem permissão. Ver o log do relay (o aviso de
   retry tem limite de taxa). O relay recua até 5 min e não perde nada.
2. Retries zerados e lag crescendo: nenhum relay rodando. Conferir se algum processo tem o papel
   `outbox-relay`. Se o relay não sobe, o log diz por quê: tópico inexistente ou não FIFO.
3. `outbox_claim_lost_total` subindo: leases vencendo antes do fim da publicação. Aumentar
   `WORKER_LEASE` ou investigar `outbox_publish_seconds`.
4. `outbox_bookkeeping_failures_total` subindo: o relay publica mas não consegue registrar o
   resultado no banco. O evento é republicado com o mesmo `eventId`. Investigar o banco.

## Diagnóstico com pprof

A porta administrativa serve `/debug/pprof/`. Exemplos, com a porta encaminhada do container:

```bash
go tool pprof http://localhost:9090/debug/pprof/profile?seconds=30
curl -s http://localhost:9090/debug/pprof/goroutine?debug=2 | head -100
```
