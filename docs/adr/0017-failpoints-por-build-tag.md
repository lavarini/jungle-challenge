# 0017 — Failpoints por build tag

**Status:** Aceito — 2026-09-23

## Contexto

O enunciado exige demonstrar recuperação em janelas específicas: depois do
commit e antes da remoção da mensagem; entre publicação e confirmação na
outbox; depois de confirmar uma pendência. Acertar essas janelas com `sleep` ou
matando processos em momento aleatório produz testes instáveis.

## Decisão

- Pacote `failpoint` com pontos nomeados chamados no código de produção
  (`consumer.after_commit`, `outbox.after_publish`, `resolver.after_claim`).
- A implementação ativa só é compilada com `-tags failpoint`, lida de
  `FAILPOINT=<nome>=<ação>` (`exit`, `panic`, `sleep:<d>`). Sem a tag, as
  chamadas são no-op.
- O e2e compila o binário com a tag e interrompe o processo na janela exata.

## Alternativas

- **Matar em tempo aleatório** — não garante atingir a janela.
- **Hooks sempre compilados, ligados por variável** — deixa injeção de falha no
  binário de produção.

## Consequências

- Cada janela de falha do enunciado tem um teste determinístico.
- O binário entregue não contém injeção de falha.
