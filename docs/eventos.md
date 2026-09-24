# Eventos e mensagens

Este documento descreve os contratos de mensageria: a mensagem que entra pela fila SQS e os
eventos de integração que saem pelo tópico SNS. As decisões estão nos ADRs
[0012](adr/0012-eventos-de-saida-em-sns-fifo.md), [0013](adr/0013-dlq-explicita-para-mensagem-invalida.md) e
[0014](adr/0014-relay-da-outbox-por-cabeca-de-particao.md); a seção 4 da [desenho](design.md)
tem o fluxo completo.

## Entrada: `wager-transactions.fifo`

O produtor envia:

- `MessageGroupId = walletId`. As mensagens de uma carteira são entregues em ordem, e carteiras
  distintas são processadas em paralelo.
- `MessageDeduplicationId = messageId`. Isso é otimização de transporte. A garantia de efeito
  único vem do banco (inbox + idempotência), não da deduplicação do SQS.

```json
{
  "messageId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-24T12:00:00Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "bet-123",
    "idempotencyKey": "provider-a:bet-123",
    "playerId": "…",
    "walletId": "…",
    "roundId": "round-9",
    "gameId": "slots-1",
    "kind": "BET",
    "money": { "amount": "10.00", "currency": "BRL" },
    "referenceExternalTransactionId": "só em REFUND, ROLLBACK e, opcionalmente, WIN"
  }
}
```

- **Remetente.** O `SenderId` da mensagem (a credencial que a enviou) é mapeado para um provider
  por `SQS_SENDER_PROVIDERS`. Um `providerId` no corpo diferente do vinculado ao remetente é
  recusado como `PROVIDER_NOT_AUTHORIZED`.
- **Inbox.** A chave é `(wager-transactions-consumer, messageId)`, gravada no mesmo commit do
  efeito financeiro com o hash SHA-256 do corpo. Uma reentrega com o mesmo corpo é respondida pelo
  inbox. O mesmo `messageId` com corpo diferente vai para a DLQ como `INBOX_PAYLOAD_MISMATCH`.
- **Mesma operação por HTTP e SQS.** A idempotência é por `(providerId, idempotencyKey)`,
  independente do canal. A segunda chegada é um replay e não gera novo débito.

### Desfechos

| Situação | Ação |
|---|---|
| Processada, replay, rejeição definitiva persistida, `PENDING_REFERENCE` | commit, depois `DeleteMessage` |
| Inválida ou não autorizada | cópia na DLQ com `failureCode` e `reason`, depois `DeleteMessage` |
| Transitória (banco indisponível, `lock_timeout`, deadlock) | visibilidade com backoff `2^n s`, teto 60 s |
| Transitória no `SQS_MAX_RECEIVES`º recebimento (padrão 5) | DLQ com `RETRIES_EXHAUSTED` e o último erro como `reason` |
| Encerramento do processo durante o processamento | rollback e visibilidade 0; não conta como falha |

Os `failureCode` da DLQ são `INVALID_MESSAGE`, `PROVIDER_NOT_AUTHORIZED`, `WALLET_NOT_FOUND`,
`WALLET_MISMATCH`, `IDEMPOTENCY_PAYLOAD_MISMATCH`, `IDEMPOTENCY_KEY_MISMATCH`,
`INBOX_PAYLOAD_MISMATCH`, `INVARIANT_VIOLATION` e `RETRIES_EXHAUSTED`. O redrive nativo da fila
(`maxReceiveCount = 20`) é só rede de segurança. A deduplicação da cópia na DLQ usa o
`MessageId` do SQS, não o `messageId` escolhido pelo produtor.

## Saída: tópico `wallet-events.fifo`

Todo evento nasce na tabela `outbox_events`, no mesmo commit da mudança que ele descreve. Um evento
só existe se o commit existiu. O relay publica depois, fora da transação.

### Envelope

```json
{
  "eventId": "…",
  "eventType": "WagerTransactionProcessed",
  "aggregateId": "<transactionId ou walletId>",
  "correlationId": "…",
  "causationId": "…",
  "occurredAt": "2026-09-24T12:00:00.123Z",
  "version": 1,
  "data": { }
}
```

- `MessageGroupId = walletId`: a ordem é garantida por carteira, não entre carteiras.
- `MessageDeduplicationId = eventId`. Uma republicação dentro da janela de 5 min do FIFO é
  descartada pelo SNS. Fora dela, **o assinante deduplica por `eventId`**. A entrega é pelo menos
  uma vez, e um evento pode chegar duas vezes após uma queda entre publicar e confirmar.
- O atributo de mensagem `eventType` permite filtro por assinatura (filter policy) sem abrir o
  corpo.
- `version` começa em 1. Uma mudança incompatível cria uma nova versão, e um campo novo e opcional
  não muda a versão.

### Tipos

| `eventType` | Quando | `data` |
|---|---|---|
| `WagerTransactionProcessed` | operação aplicada | transação completa, `status=PROCESSED`, saldo e `walletVersion` resultantes |
| `WagerTransactionRejected` | rejeição definitiva persistida | transação, `status=REJECTED`, `failureCode` |
| `WagerTransactionPendingReference` | reversão chegou antes da referência | transação, `attempts`, `nextAttemptAt`, `deadlineAt` |
| `WalletBalanceChanged` | toda movimentação de saldo, inclusive a abertura com saldo inicial positivo | `direction` (`DEBIT`/`CREDIT`), `money`, `balanceBefore`, `balanceAfter`, `walletVersion` |

Uma operação processada que movimenta saldo emite dois eventos na mesma partição, nesta ordem: o
desfecho da transação e o `WalletBalanceChanged`.

### Garantias do relay

- **Ordem.** O relay só reclama a cabeça não publicada de cada carteira (`seq` mínimo), com lease.
  Dois relays nunca publicam eventos da mesma carteira fora de ordem.
- **Fencing.** A confirmação só é aplicada por quem ainda detém o `claim_id`. Um relay cujo lease
  expirou não sobrescreve o trabalho de quem assumiu.
- **Falha transitória** (SNS indisponível, tópico ou permissão ausentes): retry com backoff
  exponencial, jitter de até 20% e teto de 5 min. O evento nunca é descartado.
- **Falha permanente** (requisição malformada) após 5 tentativas: quarentena (`dead_at` e
  `last_error`). A partição segue para o próximo evento, e a métrica `outbox_dead_total` sobe (ver
  [RUNBOOK](RUNBOOK.md)).
- **Boot.** O relay recusa iniciar se o tópico não existe ou não é FIFO.
