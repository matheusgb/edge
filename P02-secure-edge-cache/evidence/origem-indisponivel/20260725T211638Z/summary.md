# Resumo: origem indisponível

Objeto `stale-64KiB-1785014187.bin`, guardado com TTL de 3s e pedido de novo com a origem devolvendo 503.

| Medida | Valor |
|---|---:|
| Requisições durante a queda | 10 |
| Respostas servidas do cache vencido | 10 |
| Erros propagados ao cliente | 0 |
| Chamadas que chegaram à origem | 10 |
| Tentativas por pedido | 1.00 |
| Requisições até a entrada voltar a HIT | 2 |
| Revalidações 304 na volta | 1 |
| Corpos baixados de novo na volta | 0 |

## Roteiro

| Passo | Status | Cache | Gerado na origem em | Conteúdo antigo | Bytes |
|---|---:|---|---|---|---:|
| 1-popula-o-cache | 200 | MISS | 2026-07-25T21:16:27.534399041Z | false | 65536 |
| 2-durante-a-queda-01 | 200 | STALE | 2026-07-25T21:16:27.534399041Z | true | 65536 |
| 2-durante-a-queda-02 | 200 | STALE | 2026-07-25T21:16:27.534399041Z | true | 65536 |
| 2-durante-a-queda-03 | 200 | STALE | 2026-07-25T21:16:27.534399041Z | true | 65536 |
| 2-durante-a-queda-04 | 200 | STALE | 2026-07-25T21:16:27.534399041Z | true | 65536 |
| 2-durante-a-queda-05 | 200 | STALE | 2026-07-25T21:16:27.534399041Z | true | 65536 |
| 2-durante-a-queda-06 | 200 | STALE | 2026-07-25T21:16:27.534399041Z | true | 65536 |
| 2-durante-a-queda-07 | 200 | STALE | 2026-07-25T21:16:27.534399041Z | true | 65536 |
| 2-durante-a-queda-08 | 200 | STALE | 2026-07-25T21:16:27.534399041Z | true | 65536 |
| 2-durante-a-queda-09 | 200 | STALE | 2026-07-25T21:16:27.534399041Z | true | 65536 |
| 2-durante-a-queda-10 | 200 | STALE | 2026-07-25T21:16:27.534399041Z | true | 65536 |
| 3-origem-de-volta-01 | 200 | STALE | 2026-07-25T21:16:27.534399041Z | true | 65536 |
| 3-origem-de-volta-02 | 200 | HIT | 2026-07-25T21:16:27.534399041Z | true | 65536 |

A cópia original foi gerada em `2026-07-25T21:16:27.534399041Z`. As respostas com **conteúdo antigo = true**
carregam exatamente aquela cópia, o que prova que vieram do cache e não de uma
origem viva.

Depois da volta o conteúdo continua o mesmo, e isso está certo: o objeto é
determinístico, o ETag não mudou, a revalidação condicional devolveu 304 e o
Nginx renovou o frescor da cópia guardada em vez de baixar tudo de novo. O sinal
de recuperação é a entrada voltar a HIT com revalidação registrada na origem,
não o corpo mudar.

**O risco:** servir conteúdo vencido é uma escolha, não um bônus. Enquanto a
origem está fora, o cliente recebe uma versão que pode estar errada, e sem saber
disso. Para uma imagem, é o melhor negócio. Para um preço ou um saldo, pode ser
pior que um erro honesto. Quem decide é quem conhece o conteúdo.
