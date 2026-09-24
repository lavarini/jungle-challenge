# 0001 — Núcleo enxuto com profundidade escolhida

**Status:** Aceito — 2026-09-23

## Contexto

O enunciado exige que unicidade, não negatividade e imutabilidade do ledger
sejam impostas pelo banco. Uma resposta de defesa máxima usa muitos triggers e
verificações diferidas que revarrem o ledger a cada commit, com SQL difícil de
revisar. A vaga pede o critério oposto: reconhecer quando uma solução simples
basta e quando uma decisão exige profundidade.

## Decisão

O banco impõe as invariantes que o enunciado exige, e somente com verificações
de custo O(1) por operação (índices únicos, `CHECK`, triggers com busca por
chave). A profundidade extra vai para os pontos que checagens simples não
cobrem sozinhas:

- direção do lançamento coerente com o `kind` da transação, imposta no banco;
- `FAILED` alcançável, com política explícita;
- política de retry de referência coerente (tentativas × TTL);
- evidência executada de cada garantia.

Cada invariante no banco tem um teste que tenta violá-la diretamente em SQL.

## Alternativas

- **Defesa máxima** — toda mutação por função `SECURITY DEFINER`, saldo
  conferido contra o ledger inteiro no commit. Mais robusta contra credencial
  comprometida; custo de manutenção e de explicação alto, e verificação O(n).
- **Meio-termo** — enxuta mais uma camada da anterior. Rejeitada por prazo.

## Consequências

- Uma credencial de runtime comprometida ainda consegue inserir movimentos que
  respeitam as regras do banco; não consegue editar, apagar nem desativar a
  proteção. Registrado como limitação conhecida.
- O SQL permanece legível numa revisão de PR.
