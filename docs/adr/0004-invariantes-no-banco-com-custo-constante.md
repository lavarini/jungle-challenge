# 0004 — Invariantes no banco com custo constante

**Status:** Aceito — 2026-09-23

## Contexto

O enunciado exige unicidade, não negatividade e imutabilidade do ledger
impostas pelo banco. Dois riscos técnicos a evitar:

- Não impor no banco a direção do lançamento conforme o `kind` permite que um
  bug ou credencial de runtime insira um crédito onde deveria haver débito,
  respeitando a aritmética.
- Impor a coerência saldo × ledger com triggers diferidos que somam o
  histórico da carteira a cada commit tem custo que cresce com o ledger.

## Decisão

Constraints declarativas cobrem unicidade, valores, formas e aritmética. Dois
triggers cobrem o resto, ambos com buscas por índice:

1. `wallet_ledger_entries BEFORE INSERT` confere o lançamento contra a transação
   (carteira, moeda, valor, direção por `kind`, oposto da referência em
   `ROLLBACK`) e contra a carteira (saldo e versão atuais, encadeamento com o
   lançamento da versão anterior).
2. Constraint trigger diferido em `wallets` exige, no commit, que toda mudança
   de saldo tenha versão incrementada em um e lançamento correspondente.

Imutabilidade: triggers contra `UPDATE`/`DELETE`/`TRUNCATE` no ledger,
congelamento de transações terminais e de snapshots da outbox, e role de
runtime sem privilégio para mutar o ledger nem desativar triggers.

## Alternativas

- **Só constraints declarativas** — deixa a direção por `kind` e o par
  saldo × ledger dependentes do código Go.
- **Reconciliação total no commit** — garantia equivalente com custo
  O(n) por operação.
- **Função `SECURITY DEFINER` única de mutação** — mais forte contra credencial
  comprometida; rejeitada pelo ADR 0001.

## Consequências

- Cada trigger tem teste que tenta violá-lo diretamente em SQL, com a role de
  runtime.
- A ordem de escrita na transação passa a ser contrato: carteira atualizada
  antes do lançamento. Documentado no repositório de persistência.
