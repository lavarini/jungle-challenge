# 0019 — Papéis de worker separados

**Status:** Aceito — 2026-09-23. Substitui a lista de papéis do ADR 0002.

## Contexto

O ADR 0002 agrupava relay da outbox e resolvedor de pendências no papel
`workers`. Os dois têm cargas independentes: o relay cresce com o volume de
movimentações; o resolvedor, com operações fora de ordem. Separar os dois
papéis remove esse acoplamento.

## Decisão

Papéis: `api`, `consumer`, `outbox-relay`, `reference-worker` e `all`. Separar
custa apenas a seleção de `fx.Module` no `bootstrap`.

A demonstração multi-instância continua com três processos `all`
independentes, o mínimo exigido; a separação serve à operação, não à prova.

## Alternativas

- **Manter `workers`** — mais simples, acopla a escala de dois componentes sem
  relação.
- **Topologia de demonstração com cada papel replicado** — cerca de dez
  processos num laptop, sem ganho de prova sobre três processos `all`.

## Consequências

- Cada papel pode escalar e falhar isoladamente em produção.
