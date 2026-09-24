# 0018 — Evidência executada e gerada

**Status:** Aceito — 2026-09-23

## Contexto

Evidência apenas declarada não é verificável pelo avaliador; toda afirmação de
teste precisa vir de uma execução. Documentação pode afirmar garantias que o
código não tem. O avaliador precisa ver que a suíte passa sem reproduzir o
ambiente.

## Decisão

- GitHub Actions a cada push: gofmt, vet, testes unitários com `-race`,
  integração e e2e com testcontainers. Badge no README.
- `docs/EVIDENCIAS.md` é gerado por `scripts/evidence.sh` a partir de
  `go test -json`: matriz requisito do enunciado -> teste -> resultado ->
  duração, com commit e versões. Não é editado à mão.
- O mapeamento requisito -> teste vive num arquivo versionado lido pelo script;
  requisito sem teste aparece como lacuna na matriz.

## Alternativas

- **Matriz escrita à mão** — diverge do código com o tempo.
- **Só instruções de execução** — transfere ao avaliador o custo de provar.

## Consequências

- Lacunas ficam visíveis em vez de escondidas.
- Depende de testcontainers no runner do Actions (Docker disponível no
  `ubuntu-latest`).
