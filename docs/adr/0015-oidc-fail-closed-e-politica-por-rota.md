# 0015 — OIDC fail-closed e política por rota

**Status:** Aceito — 2026-09-23

## Contexto

Autenticação efetiva é eliminatória. A identidade autenticada deve determinar o
`providerId`; provedores só acessam as próprias transações, inclusive em
replays; operações de carteira são restritas ao serviço interno. Riscos a
evitar: ler o JWT sem verificar assinatura; configuração inválida desligar a
autenticação em silêncio; aceitar como identidade um header de provider vindo
do cliente.

## Decisão

- Keycloak com realm importado no boot; clients `client_credentials` por
  provider (`provider_id` por mapper, role `wager:provider`) e um client interno
  (`wallet:internal`); `aud = wagering-api`.
- Validação com `go-oidc`: discovery, JWKS em cache, somente `RS256`, `iss`,
  `aud`, `exp`, `nbf`, tolerância de 30 s.
- `Principal` tipado no `context`; handlers nunca leem identidade de header ou
  corpo.
- Fail-closed: sem configuração válida ou sem discovery no boot, o processo não
  sobe.
- Política por rota: interno opera carteiras e lê todas as transações; provider
  submete e lê só as próprias; `providerId` do corpo diferente do token é
  `403`; transação de outro provider por ID é `404`; path de outro provider é
  `403`.
- `/metrics` e `pprof` em porta administrativa separada.

## Alternativas

- **Escopos OAuth em vez de roles** — equivalente; roles de client mapeiam
  melhor no Keycloak e aparecem no token sem configuração extra de scopes.
- **Emissão própria de tokens** — fora do escopo pelo enunciado.

## Consequências

- Toda recusa de autenticação ou autorização é testada com tokens reais do
  Keycloak e verificada por ausência de linhas novas.
- Menor privilégio nos dois sentidos: o interno não submete apostas.
