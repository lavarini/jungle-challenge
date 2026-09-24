# Políticas IAM por papel de processo

Uma política por papel (`WAGERD_ROLE`), com o mínimo de ações que o código de
cada papel realmente chama na SDK da AWS. Segue [ADR 0016](../../docs/adr/0016-credenciais-do-broker-e-vinculo-do-remetente.md)
e a seção 5 da spec de arquitetura.

| Arquivo | Papel | Ações | Recurso |
|---|---|---|---|
| `consumer.json` | `consumer` | `sqs:ReceiveMessage`, `sqs:DeleteMessage`, `sqs:ChangeMessageVisibility` | fila de entrada |
| | | `sqs:SendMessage` | DLQ |
| `outbox-relay.json` | `outbox-relay` | `sns:Publish`, `sns:GetTopicAttributes` | tópico de eventos |
| `api.json` | `api` | nenhuma | — |
| `reference-worker.json` | `reference-worker` | nenhuma | — |

`sns:GetTopicAttributes` está na política do relay porque `VerifyFIFOTopic`
(`internal/adapters/snsout/publisher.go`) é chamado no boot do `outbox-relay`
(`internal/bootstrap/workers.go:runOutboxRelay`) para falhar cedo se o tópico
não for FIFO, em vez de deixar cada `Publish` com `MessageGroupId` quarentenar
com `InvalidParameter`.

`api.json` e `reference-worker.json` têm `Statement: []`: nenhum código desses
dois papéis chama a SDK da AWS (`internal/bootstrap/api.go` e
`internal/adapters/refworker/worker.go` não importam `aws-sdk-go-v2/service/*`).
Um `Statement` vazio não é um documento IAM anexável de verdade — é a forma de
registrar a decisão "zero ações" e ainda ser JSON válido para o teste. Em um
ambiente real, esses papéis simplesmente não recebem política de broker
alguma.

## ARNs

Os ARNs abaixo são os do ambiente local (LocalStack, conta `000000000000`,
região `us-east-1`, vide `deploy/localstack/init/ready.d/10-resources.sh` e
`docker-compose.yml`):

- Fila de entrada: `arn:aws:sqs:us-east-1:000000000000:wager-transactions.fifo`
- DLQ: `arn:aws:sqs:us-east-1:000000000000:wager-transactions-dlq.fifo`
- Tópico de eventos: `arn:aws:sns:us-east-1:000000000000:wallet-events.fifo`

**Cada ARN é parametrizado por ambiente.** Conta, região e nomes de fila/tópico
mudam fora do local (numeração de conta AWS real, região de produção, prefixo
de nome por stack). Os arquivos aqui fixam os valores locais para que o teste
de arquitetura tenha algo concreto para comparar; um pipeline de implantação
real deve gerar estes documentos a partir de variáveis (Terraform, CDK ou
equivalente) em vez de copiar os JSONs literalmente.

## Limitações

- **LocalStack Community não impõe IAM.** As políticas aqui são declaradas e
  versionadas, mas nada as anexa ou avalia localmente — qualquer credencial
  (inclusive a única `test`/`test` que o Compose usa hoje) pode chamar
  qualquer ação em qualquer recurso. A separação real depende de uma
  credencial por papel, que o Compose local não provisiona (trade-off
  documentado no ADR 0016). O teste em `internal/archtest/iam_test.go` prova
  apenas que cada arquivo é um JSON válido e contém exatamente o conjunto de
  ações esperado — não prova imposição em runtime.
- **Sem wildcards.** Nenhuma política aqui usa `"*"` em `Action` ou em
  `Resource`; o teste de arquitetura falha se algum arquivo introduzir um.
- **Verificação de prontidão do SQS não está coberta por estas políticas.**
  `internal/bootstrap/core.go` registra um `health.Check` que chama
  `sqs:GetQueueAttributes` na fila de entrada (`platform.SQSProbe`) como parte
  de `/health/ready`. Esse probe é conectado no módulo `core`, comum a todo
  papel, mas só é de fato invocado pelo servidor HTTP do papel `api` (ou
  `all`) quando algo bate em `/health/ready`. Ou seja: com credenciais
  separadas por papel, o processo `api` precisaria de `sqs:GetQueueAttributes`
  na fila de entrada só para responder sua própria prontidão — o que
  contradiz "api: nenhuma ação AWS" listado acima. Isso está fora do escopo
  deste commit (não mexe em `internal/bootstrap/core.go`) e fica registrado
  aqui como gap conhecido para uma tarefa futura: mover esse probe para o
  papel `consumer` (que já tem a permissão) ou aceitar e documentar a ação
  extra na política do `api`.
