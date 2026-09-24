# 0011 — FAILED significa violação de invariante

**Status:** Aceito — 2026-09-23

## Contexto

O enunciado define `FAILED` como falha permanente de infraestrutura registrada
para auditoria e pede que a distinção entre falha transitória e permanente seja
documentada. Um estado terminal que existe só no contrato, sem nenhum fluxo
capaz de alcançá-lo, não prova a garantia.

## Decisão

- Indisponibilidade de banco ou broker, timeout e conflito de lock são
  transitórios: retry (e DLQ no SQS). Nunca viram `FAILED`.
- `FAILED` é o desfecho quando o banco recusa, por constraint ou trigger, algo
  que o domínio aprovou, num caminho assíncrono sobre transação já persistida
  (resolvedor de pendências). É um defeito, não uma condição passageira:
  repetir produziria a mesma recusa.
- O worker grava `FAILED` / `INVARIANT_VIOLATION` em transação separada,
  incrementa métrica de alerta e não tenta de novo.
- No caminho síncrono, a mesma condição resulta em `500`, rollback e métrica;
  nada é persistido.

## Alternativas

- **Remover `FAILED`** — contraria o modelo de estados do enunciado.
- **Promover a `FAILED` após N falhas de infraestrutura** — transforma uma
  indisponibilidade longa em perda de operação válida.

## Consequências

- O estado é alcançável e testável: o teste força uma violação com a role de
  migração e verifica o desfecho, a métrica e a ausência de retry.
