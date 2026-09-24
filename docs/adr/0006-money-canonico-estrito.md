# 0006 — Money em int64 com entrada canônica estrita

**Status:** Aceito — 2026-09-23

## Contexto

Dinheiro não pode passar por ponto flutuante. O enunciado permite aceitar formas
equivalentes (`"25"`, `"25.0"`) desde que a normalização anterior ao hash de
idempotência seja documentada.

## Decisão

- `Money` é value object imutável: `int64` em unidades mínimas + `Currency`.
- Moedas suportadas: BRL, USD e EUR, todas com escala 2. A borda externa opera
  em BRL; as demais existem para testes de incompatibilidade.
- Entrada externa aceita somente `^(0|[1-9][0-9]*)\.[0-9]{2}$`. Vazio, sinal,
  espaços, notação científica, `NaN`, `Infinity`, zeros à esquerda e escala
  diferente de duas casas são rejeitados, sem arredondamento.
- Soma, subtração e negação verificam overflow e moeda; valores negativos só
  existem em cálculos internos (por exemplo, `difference` da reconciliação).
- Persistência: `BIGINT` + `CHAR(3)`.

## Alternativas

- **Normalizar formas equivalentes** — mais tolerante com integradores, mas
  exige regra de normalização documentada e testada antes do hash.
- **Biblioteca decimal** — escala arbitrária, dependência extra, sem ganho no
  escopo de duas casas.

## Consequências

- O hash de idempotência é canônico por construção.
- Moedas com escala 0 ou 3 exigiriam catálogo com expoente por moeda.
- Limite: ±92.233.720.368.547.758,07.
