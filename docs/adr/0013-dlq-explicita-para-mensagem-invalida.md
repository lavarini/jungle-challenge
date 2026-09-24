# 0013 — DLQ explícita para mensagem inválida

**Status:** Aceito — 2026-09-23 (revisado em 2026-09-24)

## Contexto

O enunciado pede que erros permanentes ou tentativas esgotadas cheguem à DLQ e
que o tratamento de mensagens inválidas seja documentado. Com o redrive nativo,
uma mensagem que nunca vai passar ocupa `maxReceiveCount` recebimentos. Numa
fila FIFO, enquanto ela não sai, as mensagens seguintes do mesmo
`MessageGroupId` — a mesma carteira — ficam bloqueadas. O redrive também não
registra o motivo.

## Decisão

- Mensagem inválida ou corrigível (JSON, campos obrigatórios, `Money`,
  `kind=OPENING`, carteira inexistente, conflito de idempotência, hash de inbox
  divergente, provider não autorizado para o remetente): o consumidor faz
  `SendMessage` à DLQ com atributos `failureCode` e `reason`, depois
  `DeleteMessage` da fila de entrada.
- Falha transitória volta à fila com backoff de visibilidade. Quando o
  `ApproximateReceiveCount` da mensagem chega a `SQS_MAX_RECEIVES` (padrão 5),
  o próprio consumidor a envia à DLQ com `failureCode=RETRIES_EXHAUSTED` e o
  último erro como `reason`.
- O redrive nativo fica como rede de segurança, com `maxReceiveCount = 20`.
  Ele precisa ficar bem acima do limite do consumidor porque, quando a cabeça
  de um grupo falha, as mensagens seguintes do grupo são devolvidas sem
  processamento e também acumulam recebimentos: com o redrive em 5, uma queda
  curta do banco levaria à DLQ, sem motivo, mensagens que nunca falharam.

## Alternativas

- **Só redrive nativo** — bloqueia a carteira por minutos e perde o motivo.

## Consequências

- Envio à DLQ e remoção não são atômicos: queda entre eles gera duplicata na
  DLQ, inofensiva e deduplicada pelo `MessageId` do SQS na janela FIFO. O
  `messageId` do envelope não serve de chave: é escolhido pelo produtor, e um
  segundo dead letter legítimo com o mesmo valor seria descartado em silêncio.
- Um long poll cancelado no encerramento pode ainda receber mensagens no
  servidor. Elas ficam invisíveis até o fim do `VisibilityTimeout` e ganham um
  recebimento a mais; o limite do consumidor e a folga do redrive absorvem
  isso.
- O consumidor precisa de `sqs:SendMessage` na DLQ, declarado na política do
  broker.
