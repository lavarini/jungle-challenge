# Políticas IAM por papel de processo

Uma política por papel (`WAGERD_ROLE`), com o mínimo de ações que o código de
cada papel realmente chama na SDK da AWS. Segue [ADR 0016](../../docs/adr/0016-credenciais-do-broker-e-vinculo-do-remetente.md)
e a seção 5 da spec de arquitetura.

| Arquivo | Papel | Ações | Recurso |
|---|---|---|---|
| `consumer.json` | `consumer` | `sqs:ReceiveMessage`, `sqs:DeleteMessage`, `sqs:ChangeMessageVisibility` | fila de entrada |
| | | `sqs:SendMessage` | DLQ |
| `outbox-relay.json` | `outbox-relay` | `sns:Publish`, `sns:GetTopicAttributes` | tópico de eventos |
| `api.json` | `api` | `sqs:GetQueueAttributes` (prontidão) | fila de entrada |
| `reference-worker.json` | `reference-worker` | `sqs:GetQueueAttributes` (prontidão) | fila de entrada |
| todos | todos | `sqs:GetQueueAttributes` (prontidão) | fila de entrada |

`sns:GetTopicAttributes` está na política do relay porque `VerifyFIFOTopic`
(`internal/adapters/snsout/publisher.go`) é chamado no boot do `outbox-relay`
(`internal/bootstrap/workers.go:runOutboxRelay`) para falhar cedo se o tópico
não for FIFO, em vez de deixar cada `Publish` com `MessageGroupId` quarentenar
com `InvalidParameter`.

`api.json` e `reference-worker.json` só têm a declaração `ReadinessProbe`: fora
da prontidão, nenhum código desses dois papéis chama a SDK da AWS.

## Papel `all`

O Compose roda `WAGERD_ROLE=all`, que junta os quatro papéis num processo. A credencial desse
processo precisa da união das quatro políticas. Em produção, a separação vem de rodar cada papel
com a própria credencial.

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
  que cada arquivo é JSON válido e contém exatamente o conjunto de ações da
  tabela esperada por papel. A tabela foi levantada das chamadas à SDK de cada
  papel, e o teste não a deriva do código. Ele também não prova imposição em
  runtime.
- **Sem wildcards.** Nenhuma política aqui usa `"*"` em `Action` ou em
  `Resource`; o teste de arquitetura falha se algum arquivo introduzir um.
- **Prontidão.** Todo papel responde `/health/ready`, que verifica o Postgres e
  a fila de entrada com `sqs:GetQueueAttributes` (seção 7 da spec). Por isso as
  quatro políticas têm a declaração `ReadinessProbe`, restrita a essa ação e a
  essa fila. É a única ação AWS de `api` e `reference-worker`.
