# 0003 — Execução em esqueleto andante e fatias verticais

**Status:** Aceito — 2026-09-23

## Contexto

Prazo apertado. Integração com Keycloak e LocalStack é onde o tempo costuma se
perder. Integridade, concorrência, idempotência e mensageria somam 70 dos 100
pontos da avaliação.

## Decisão

- **Fatia 1** — esqueleto andante: Compose completo, migrations, Fx, OIDC,
  `Money`/`Wallet`/`WagerTransaction`, `POST /wallets` e `BET` de ponta a ponta
  com ledger e outbox; harness de integração com infraestrutura real e o
  cenário 80+80/100 com três processos.
- **Fatia 2** — demais tipos, reversões e referências pendentes; consumidor SQS
  e inbox; relay da outbox; idempotência completa; leituras e reconciliação.
- **Fatia 3** — falhas e recuperação com failpoints, shutdown, observabilidade
  (`pprof` incluso), ADRs, matriz de evidência e teste de carga (`cmd/load`,
  [docs/CARGA.md](../CARGA.md)).

Cada fatia entra com o cenário obrigatório que a comprova.

## Alternativas

- **Por camadas** — nada roda ponta a ponta antes da fatia 2; surpresas de
  integração ficam para o fim.
- **Contrato primeiro** — os oito cenários como testes falhando antes do código;
  consome a fatia 1 sem sistema rodando. A ideia foi incorporada: cada fatia
  traz seu cenário.

## Consequências

- Se o prazo apertar, cortam-se diferenciais, nunca o núcleo.
- Fora do escopo, registrados como próximos passos: tracing OTel, Terraform,
  Secrets Manager, políticas IAM exercitadas.
