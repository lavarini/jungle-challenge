# Architecture Decision Records

Uma decisão por arquivo: contexto, decisão, alternativas e consequências.
Um ADR aceito não é editado para mudar de ideia — um novo ADR o substitui e o
antigo passa a `Substituído por NNNN`.

| # | Decisão | Status |
|---|---|---|
| [0001](0001-nucleo-enxuto-com-profundidade-escolhida.md) | Núcleo enxuto com profundidade escolhida | Aceito |
| [0002](0002-monolito-modular-com-papeis-fx.md) | Monólito modular com papéis Fx | Aceito; papéis substituídos por 0019 |
| [0003](0003-execucao-em-fatias-verticais.md) | Execução em esqueleto andante e fatias verticais | Aceito |
| [0004](0004-invariantes-no-banco-com-custo-constante.md) | Invariantes no banco com custo constante | Aceito |
| [0005](0005-vaga-unica-de-reversao.md) | Vaga única de reversão por transação referenciada | Aceito |
| [0006](0006-money-canonico-estrito.md) | Money em int64 com entrada canônica estrita | Aceito |
| [0007](0007-outra-chave-para-mesma-operacao-e-conflito.md) | Outra chave para a mesma operação é conflito | Aceito |
| [0008](0008-processamento-sincrono-e-ordem-de-locks.md) | Processamento síncrono e ordem de locks | Aceito |
| [0009](0009-rejeicao-corrigivel-e-definitiva.md) | Rejeição corrigível não persiste; definitiva persiste | Aceito |
| [0010](0010-referencias-pendentes-com-prazo.md) | Referências pendentes com prazo como critério terminal | Aceito |
| [0011](0011-failed-e-violacao-de-invariante.md) | FAILED significa violação de invariante | Aceito |
| [0012](0012-eventos-de-saida-em-sns-fifo.md) | Eventos de saída em SNS FIFO | Aceito |
| [0013](0013-dlq-explicita-para-mensagem-invalida.md) | DLQ explícita para mensagem inválida | Aceito |
| [0014](0014-relay-da-outbox-por-cabeca-de-particao.md) | Relay da outbox por cabeça de partição | Aceito |
| [0015](0015-oidc-fail-closed-e-politica-por-rota.md) | OIDC fail-closed e política por rota | Aceito |
| [0016](0016-credenciais-do-broker-e-vinculo-do-remetente.md) | Credenciais do broker e vínculo do remetente | Aceito |
| [0017](0017-failpoints-por-build-tag.md) | Failpoints por build tag | Aceito |
| [0018](0018-evidencia-executada-e-gerada.md) | Evidência executada e gerada | Aceito |
| [0019](0019-papeis-de-worker-separados.md) | Papéis de worker separados | Aceito |
