# 0002 — Monólito modular com papéis Fx

**Status:** Aceito — 2026-09-23. Lista de papéis substituída por [0019](0019-papeis-de-worker-separados.md).

## Contexto

O domínio tem dois contextos: **Wallet/Ledger** (saldo e lançamentos) e
**Wagering** (operações de provedores, referências, estados). O enunciado exige
que saldo, transação, ledger, inbox e outbox sejam confirmados no mesmo commit.
Ao mesmo tempo, API, consumidor e workers têm perfis de carga diferentes. Num
processo único com todos os workers, escalar o HTTP escala todos os workers
junto.

## Decisão

- Um módulo Go e um binário, `wagerd`, com os contextos separados em pacotes
  (`internal/wallet`, `internal/wagering`) e um shared kernel (`internal/money`).
- Os contextos compartilham uma transação SQL, fornecida por uma `UnitOfWork`
  na camada de aplicação.
- O processo recebe um papel — `api`, `consumer`, `workers` ou `all` — que
  seleciona quais `fx.Module` compõem a aplicação. `bootstrap` é o único pacote
  que importa Fx.
- A regra de dependência (domínio → stdlib; `app` → domínio e portas;
  adapters → `app`) é verificada por teste, não só documentada.

## Alternativas

- **Microsserviços Wallet e Wagering** — exigiria saga com compensação para
  mover saldo, e o requisito de atomicidade viraria consistência eventual.
  Complexidade sem retorno neste escopo.
- **Um processo sem papéis** — mais simples, mas acopla a escala de HTTP à dos
  workers.

## Consequências

- Se Wallet precisar virar serviço, o corte é o `app` de Wallet exposto como API
  interna idempotente com ledger próprio; os pacotes já estão separados nessa
  linha.
- Um único binário mantém o build e o Compose simples; o profile `multi` sobe
  três instâncias independentes para os testes de concorrência.
