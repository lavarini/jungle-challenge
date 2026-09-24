# 0005 — Vaga única de reversão por transação referenciada

**Status:** Aceito — 2026-09-23

## Contexto

O enunciado proíbe duas reversões bem-sucedidas do mesmo tipo sobre uma
referência e pede coerência entre `REFUND` e `ROLLBACK` sem devolução duplicada.
Indexar por `(referência, kind)` permite que uma `BET` receba um `REFUND` e um
`ROLLBACK`, creditando duas vezes.

## Decisão

- `REFUND` referencia apenas `BET` processada.
- `ROLLBACK` referencia `BET`, `WIN` ou `REFUND` processados e aplica o
  movimento oposto ao lançamento original.
- Cada transação referenciada admite no máximo uma reversão `PROCESSED`, de
  qualquer tipo: `UNIQUE (reference_tx_id) WHERE status = 'PROCESSED' AND kind
  IN ('REFUND','ROLLBACK')`.
- A segunda reversão é `REJECTED` com `REVERSAL_ALREADY_APPLIED`.
- Desfazer um `REFUND` é um `ROLLBACK` que referencia o `REFUND`, não a `BET`:
  `BET → REFUND → ROLLBACK(REFUND)` deixa a aposta debitada de novo.
- Reversão cujo débito excederia o saldo é `REJECTED` com
  `REVERSAL_INSUFFICIENT_FUNDS`, distinto de `BET_INSUFFICIENT_FUNDS`.

## Alternativas

- **Vaga por tipo** — permite a dupla devolução; rejeitada.
- **Reabrir a `BET` após rollback do refund** — permitiria novo refund sobre a
  mesma aposta; complica o modelo sem requisito que o peça.

## Consequências

- Testes nas duas ordens (`REFUND → ROLLBACK` e `ROLLBACK → REFUND`), e
  concorrentes, sobre a mesma `BET`.
