# 0009 — Rejeição corrigível não persiste; definitiva persiste

**Status:** Aceito — 2026-09-23

## Contexto

O enunciado pede `failureCode` estável que distinga entradas corrigíveis de
resultados definitivos, e contratos HTTP distinguíveis para entrada inválida,
conflito, rejeição de negócio, pendência e indisponibilidade.

## Decisão

- **Corrigível** — o problema está na requisição: JSON inválido, `Money` fora
  do formato, `kind=OPENING`, carteira inexistente, jogador ou moeda divergente
  da carteira. Não é persistido; a chave de idempotência continua livre para a
  requisição corrigida. HTTP `400` ou `404`; no SQS, DLQ com o motivo.
- **Definitiva** — a requisição é válida e a regra de negócio recusa:
  `BET_INSUFFICIENT_FUNDS`, `REVERSAL_INSUFFICIENT_FUNDS`,
  `REVERSAL_ALREADY_APPLIED`, `REFERENCE_MISMATCH`, `REFERENCE_NOT_PROCESSED`,
  `REFERENCE_NOT_FOUND`. Persistida como `REJECTED` com evento; o replay
  devolve a mesma rejeição. HTTP `422`; no SQS, a mensagem é removida.
- `422` significa exatamente "rejeição persistida".
- Erros em `application/problem+json` (RFC 9457) com `code` e `retryable`.

## Alternativas

- **Persistir toda rejeição** — exigiria transação sem carteira válida e
  consumiria a chave com uma requisição malformada.
- **`200` com `status: REJECTED`** — esconde a rejeição de clientes que só
  olham o status HTTP.

## Consequências

- O catálogo de `failureCode` é contrato público, documentado com a classe de
  cada código.
