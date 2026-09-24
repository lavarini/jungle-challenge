# 0007 — Outra chave para a mesma operação é conflito

**Status:** Aceito — 2026-09-23

## Contexto

Uma operação é identificada por `(providerId, externalTransactionId)` e chega
com uma `Idempotency-Key`. O enunciado proíbe reaplicar a operação com outra
chave e proíbe o servidor de substituir silenciosamente a chave recebida.

## Decisão

| Situação | Resultado |
|---|---|
| Mesma chave, mesmo hash | replay do resultado persistido, `idempotentReplay: true` |
| Mesma chave, hash diferente | `409 IDEMPOTENCY_PAYLOAD_MISMATCH` |
| Mesma operação externa, outra chave | `409 IDEMPOTENCY_KEY_MISMATCH`, com o `transactionId` existente |

A resposta de conflito só é dada ao provedor dono da operação; para outro
provedor, a operação não existe.

## Alternativas

- **Tratar como alias** — mais tolerante, mas aceita uma chave diferente da
  registrada como se fosse a mesma, o que se aproxima da substituição
  silenciosa que o enunciado proíbe.

## Consequências

- Integrador que regenera chaves recebe erro explícito e o ID para consulta.
- Na entrada SQS, o mesmo conflito é resultado terminal da mensagem (tratamento
  definido na seção de mensageria).
