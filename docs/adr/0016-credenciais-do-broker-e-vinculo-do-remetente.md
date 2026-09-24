# 0016 — Credenciais do broker e vínculo do remetente

**Status:** Aceito — 2026-09-23

## Contexto

O enunciado pede acesso à mensageria controlado por credenciais e políticas do
broker, preservando validações de domínio no consumidor. Na entrada SQS não há
token OIDC: confiar apenas no `providerId` do envelope deixa qualquer produtor
com acesso à fila falar em nome de qualquer provider. LocalStack Community não
impõe IAM.

## Decisão

- Uma credencial por ator: consumer, relay e produtor de cada provider.
- Políticas IAM e de fila versionadas em `deploy/iam/` e anexadas no
  provisionamento: produtor com `sqs:SendMessage` na entrada; consumer com
  `ReceiveMessage`, `DeleteMessage`, `ChangeMessageVisibility` na entrada e
  `SendMessage` na DLQ; relay com `sns:Publish` no tópico.
- O consumer lê o atributo de sistema `SenderId` (preenchido pelo SQS com a
  identidade AWS do remetente) e o valida contra um mapeamento configurado
  `SenderId -> providerId`. Divergência ou remetente desconhecido: DLQ com
  `PROVIDER_NOT_AUTHORIZED`.

## Alternativas

- **Fila por provider** — isolamento mais forte via IAM, mas muda o nome de fila
  exigido e multiplica consumidores.
- **Envelope assinado** — exige gestão de chaves por produtor; desproporcional
  ao escopo.
- **Confiar no envelope** — rejeitada: qualquer remetente com acesso à fila
  poderia se passar por outro provedor.

## Consequências

- Localmente, as políticas são declaradas mas não impostas; o teste de
  integração prova a checagem do consumer.
- Se o LocalStack não derivar `SenderId` da access key, a checagem fica coberta
  por teste unitário e a limitação é registrada no `ARCHITECTURE.md`.
