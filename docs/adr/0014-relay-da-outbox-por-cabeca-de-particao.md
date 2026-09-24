# 0014 — Relay da outbox por cabeça de partição

**Status:** Aceito — 2026-09-23

## Contexto

O enunciado exige múltiplos publishers, disputa por registros, backoff e
recuperação de trabalho abandonado, preservando o `eventId`. Riscos técnicos a
evitar:

- `SKIP LOCKED` por linha permite que dois publishers enviem eventos da mesma
  carteira fora de ordem.
- Manter a transação SQL aberta durante a chamada ao broker acopla locks e
  conexões à latência da AWS.
- Deixar `next_attempt_at` sem uso reduz o retry a tentativas sem backoff real.

## Decisão

- Só a cabeça não publicada de cada `partition_key` (`walletId`) é
  reivindicável.
- Claim em transação curta: `claim_id` aleatório, `claim_expires_at`,
  `attempts++`. Publicação fora de transação. Ack e reagendamento condicionados
  a `claim_id` (fencing): o ack de um publisher atrasado não tem efeito.
- Falha transitória: backoff exponencial com jitter, teto de 5 min, sem limite
  de tentativas — evento confirmado nunca é descartado.
- Falha permanente da AWS após N tentativas: `dead_at`, métrica
  `outbox_dead_total`, partição segue.
- Polling configurável (500 ms). `LISTEN/NOTIFY` fica como evolução.

## Alternativas

- **Claim por linha** — mais vazão, sem ordem por carteira.
- **Transação aberta durante o publish** — mais simples, pressiona pool e locks.

## Consequências

- Vazão por carteira é serial (um evento em voo por partição); carteiras
  diferentes publicam em paralelo.
- Após uma quarentena, a ordem daquela carteira deixa de ser garantida; a
  métrica de alerta torna o caso visível.

## Revisão — 2026-09-25

Medido com `cmd/load` contra o Compose com três instâncias (`-hot 0.2`, cerca de 500 req/s e
1000 eventos/s): a outbox não drenava, com cerca de 35 mil eventos pendentes ao fim da carga.
Três causas, cada uma medida antes de mudar:

- **Claim quadrático com estatísticas velhas.** Logo após o boot o planner estimava uma linha
  pendente e reexecutava o `DISTINCT ON` das cabeças uma vez por evento pendente (14,8 s com
  6000 pendentes), e o claim estourava o `statement_timeout` (57014). As cabeças passam a sair
  de um skip scan recursivo sobre o índice parcial existente, uma sonda por partição, avaliado
  uma só vez. A semântica da cabeça, o `SKIP LOCKED`, o lease e a rechecagem sob EvalPlanQual
  não mudaram. O custo por claim passa a ser O(partições com eventos pendentes), não do backlog,
  com o array de cabeças materializado a cada tick; os próximos passos são LISTEN/NOTIFY e um
  teto de partições por claim.
- **Publicação serial.** Um lote de 50 cabeças fazia 50 idas ao SNS em série. As cabeças são de
  partições diferentes, então passam a ser publicadas em paralelo, até
  `OUTBOX_PUBLISH_CONCURRENCY` (padrão 16). Cada partição continua com no máximo um evento em
  voo. `DB_MAX_CONNS` precisa ficar acima de `OUTBOX_PUBLISH_CONCURRENCY`, com folga para HTTP,
  consumer e resolver no papel `all` (ver `.env.example`).
- **Espera após lote parcial.** O relay só reivindicava de novo sem esperar o intervalo quando o
  lote vinha cheio. Com o atraso concentrado numa carteira quente, a partição drenava a um
  evento por intervalo por relay (5,9 eventos/s com três instâncias). O relay agora espera o
  intervalo apenas quando a reivindicação volta vazia.

Depois das três mudanças, a mesma carga não tem 57014, publica cerca de 150 eventos/s durante a
carga e 300 eventos/s depois, e a carteira quente drena a cerca de 110 eventos/s. A outbox
esvazia cerca de 3,5 min após o fim da carga, contra uma cauda estimada em mais de 25 min antes.
O teto que sobra está fora do relay: o LocalStack satura (CPU acima de 100 %) e a latência média
de publicação sobe de 11 ms (serial) para cerca de 95 ms com 48 publicações simultâneas. A vazão
por carteira continua serial por desenho: uma carteira que recebe mais eventos por segundo do que
uma ida e volta de claim, publish e ack comporta acumula atraso durante a carga.
