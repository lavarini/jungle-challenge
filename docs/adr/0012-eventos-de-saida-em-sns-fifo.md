# 0012 — Eventos de saída em SNS FIFO

**Status:** Aceito — 2026-09-23

## Contexto

O enunciado pede provisionar o destino dos eventos de saída e documentar seus
contratos de roteamento e consumo. Fila é ponto a ponto: cada mensagem vai
para um consumidor. Eventos como `WalletBalanceChanged` têm vários
interessados (notificação, antifraude, analytics), e a vaga cita SNS e
integrações orientadas a eventos.

## Decisão

- O relay publica no tópico SNS FIFO `wallet-events.fifo`.
- Consumidores assinam com filas SQS FIFO próprias; o repositório provisiona
  `wallet-events-audit.fifo` (raw delivery) como consumidor de demonstração e
  sonda dos testes.
- `MessageGroupId = walletId` preserva a ordem por carteira;
  `MessageDeduplicationId = eventId` absorve republicações na janela de 5 min.
- Atributo `eventType` permite filter policies por assinante: o contrato de
  roteamento é configuração declarada.
- Contrato de consumo: entrega at-least-once, ordem por carteira, deduplicação
  por `eventId` obrigatória no assinante.

## Alternativas

- **Fila SQS de saída** — ou os consumidores competem pelas mensagens, ou o
  publisher passa a conhecer e publicar em cada fila consumidora.
- **SNS padrão** — sem ordem por carteira nem deduplicação.

## Consequências

- Novo consumidor é uma assinatura, sem mudança no serviço de carteira.
- Assinantes de tópico FIFO precisam ser filas FIFO.
- Risco: suporte do LocalStack a SNS FIFO. A publicação fica atrás da porta
  `EventPublisher`; o recuo é um adapter SQS FIFO direto, registrado em novo
  ADR se acontecer.
