# Processamento distribuído de apostas em Go

> Em construção. Enunciado em [`docs/DESAFIO.md`](docs/DESAFIO.md); design em
> [`docs/design.md`](docs/design.md); decisões em
> [`docs/adr/`](docs/adr/README.md).

## Pré-requisitos

- Go 1.27.x
- Docker com Compose v2

## Comandos

| Comando | O que faz |
|---|---|
| `make lint` | gofmt e go vet |
| `make test-race` | testes unitários com `-race` |
| `make test-integration` | integração com PostgreSQL, Keycloak e LocalStack reais |
| `make test-e2e` | três processos independentes |
