# 0008 — Processamento síncrono e ordem de locks

**Status:** Aceito — 2026-09-23

## Contexto

HTTP, SQS e o resolvedor de pendências precisam produzir o mesmo efeito
financeiro. O enunciado exige retomada durável de todo `PENDING` confirmado,
mas permite concluir de forma síncrona operações sem dependências. A disputa
por carteira precisa ser arbitrada pelo banco, entre processos.

## Decisão

- Um caso de uso, `SubmitWager`, com a decisão financeira em uma função pura
  de domínio (`wagering.Decide`). Transportes só montam o comando canônico.
- Uma transação `READ COMMITTED` por operação. A linha da carteira é travada
  com `SELECT ... FOR UPDATE`; a idempotência é consultada antes (caminho
  rápido, sem lock) e de novo depois do lock.
- A transação é gravada já no estado final. `PENDING` existe só em memória; o
  único não terminal persistido é `PENDING_REFERENCE`.
- Ordem de locks fixa: carteira, depois transação — no caminho síncrono e no
  worker.
- Violação de unicidade em disputa: rollback e um retry, que resolve em replay.
- Falha no commit é tratada como resultado ambíguo: `503` retentável, sem
  retry automático da mutação.
- `lock_timeout` e `statement_timeout` limitam a espera; estourados, `503`.

## Alternativas

- **Controle otimista com retry** — sob disputa na mesma carteira gera retries
  e latência imprevisível no caminho financeiro.
- **`UPDATE ... WHERE balance >= amount`** — resolve o débito, mas não
  serializa a leitura de referência e de idempotência da mesma carteira.
- **Aceite assíncrono (`PENDING` commitado e worker)** — um commit a mais e um
  worker a mais sem requisito que o exija.

## Consequências

- Carteiras diferentes não disputam lock; a mesma carteira é serial.
- O cenário 80+80/100 é determinístico: a segunda vê saldo 20 após o lock.
