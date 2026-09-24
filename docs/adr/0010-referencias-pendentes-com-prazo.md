# 0010 — Referências pendentes com prazo como critério terminal

**Status:** Aceito — 2026-09-23

## Contexto

Reversões podem chegar antes da operação que referenciam. O enunciado exige
`PENDING_REFERENCE`, retry com backoff exponencial que sobreviva a reinícios,
limite de tentativas ou TTL, e rejeição com código próprio ao esgotar. Limitar
por tentativas acopla o prazo real à fórmula de backoff: oito tentativas com
teto de 5 min encerram a espera em cerca de 255 s, bem antes de um TTL
declarado de 24 h.

## Decisão

- Critério terminal único: `deadline_at = created_at + TTL` (padrão 24 h,
  configurável; testes usam segundos). Expirado: `REJECTED` /
  `REFERENCE_NOT_FOUND` com evento.
- Tentativas não encerram a espera; são consequência do backoff exponencial com
  jitter (1 s, 2 s, 4 s..., teto de 5 min). `attempts` é registrado para
  auditoria e métrica.
- Despertar: a transação que termina `PROCESSED` atualiza `next_attempt_at =
  now()` das pendências que a referenciam, no mesmo commit.
- Worker: transação curta reivindica pendências vencidas com
  `FOR UPDATE SKIP LOCKED`, gravando um lease em `next_attempt_at`; cada item é
  processado em transação própria, travando carteira e depois transação,
  reconferindo o estado e chamando o mesmo `Decide`.
- Referência existente mas pendente: continua esperando. Referência `REJECTED`
  ou `FAILED`: `REFERENCE_NOT_PROCESSED`.

## Alternativas

- **Limite por tentativas** — torna o prazo real dependente da fórmula de
  backoff, o que produz a incoerência acima.
- **Espera em memória ou `sleep`** — não sobrevive a reinício.

## Consequências

- O prazo é uma política de negócio explícita e configurável.
- O despertar torna a resolução imediata no caso comum, sem polling agressivo.
