# Evidência

Cada execução de experimento grava uma pasta aqui, no formato
`evidence/<cenário>/<timestamp UTC>/`, com quatro arquivos:

| Arquivo          | Para que serve                                                              |
| ---------------- | ---------------------------------------------------------------------------- |
| `environment.md` | máquina, kernel, CPUs, commit, versões do Docker e do Nginx                  |
| `commands.txt`   | o comando exato que produziu aquele resultado                                |
| `summary.md`     | a tabela legível, com a conclusão e as ressalvas                             |
| `metrics.json`   | os mesmos dados em JSON, para outra ferramenta consumir sem reparsear texto  |

## Os cenários

| Cenário                | O que ele responde                                                                                              |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------- |
| `cache-stampede`        | quantas chamadas chegam à origem quando 100 clientes pedem o mesmo objeto ausente, com e sem `proxy_cache_lock` |
| `origem-indisponivel`   | o que o cliente recebe enquanto a origem está fora do ar, e quanto a CDN insiste                                |
| `hit-ratio-ttl-longo`   | hit ratio e alívio da origem com TTL de 60s                                                                     |
| `hit-ratio-ttl-curto`   | os mesmos números com TTL de 3s, para isolar o efeito do TTL                                                    |
| `testes-negativos`      | os treze vetores que a CDN precisa recusar                                                                      |

## Nomes antigos numa execução guardada

`testes-negativos/20260725T211320Z` é anterior à renomeação do projeto, de
quando a camada de cache era chamada de "borda" e as métricas tinham prefixo
`edge_`. O arquivo fica como está: evidência é registro do que aconteceu numa
data, e reescrever o passado para combinar com o vocabulário de hoje é
exatamente o que uma pasta de evidência não deve fazer.

## Por que existe uma execução com falha guardada aqui

`testes-negativos/20260725T211320Z` tem um vetor marcado como **FALHOU**, e
fica no repositório de propósito.

Naquela execução, o vetor `token-nao-chega-na-origem` mostrou que a rota de
diagnóstico da CDN estava repassando a query string para a origem, e é na
query que o token viaja. A causa era uma diferença sutil do Nginx: `proxy_pass`
com URI literal repassa a query original, com variável não repassa. As rotas
de objeto já usavam variável e estavam corretas; a rota de diagnóstico, não.

Aquela execução também é anterior a uma segunda correção, na própria
ferramenta de teste: a mensagem de erro do cliente HTTP carrega a URL
inteira, e a URL inteira carrega o token. O arquivo saiu com uma assinatura
válida dentro, e teve que ser redigido à mão antes de entrar no repositório.
Depois disso, a ferramenta passou a redigir sozinha qualquer query antes de
escrever em tela ou em arquivo. Vale registrar a ordem dos fatos: quem existe
para provar que o token não vaza foi a primeira a vazá-lo.

A execução seguinte, `20260725T211438Z`, é a mesma bateria depois da
correção. Guardar as duas é mais honesto que guardar só a verde: o teste
negativo pegou um vazamento real de credencial, que era exatamente o trabalho
dele.

## O que não entra aqui

Objetos de carga. Eles são gerados a partir do nome, de forma determinística,
e regerar é mais barato que versionar. O repositório guarda o resumo e os
números agregados que sustentam a conclusão.

Segredos, tokens e query strings. As ferramentas de experimento redigem
qualquer query antes de escrever na tela ou em arquivo, inclusive dentro de
mensagem de erro do cliente HTTP, que é por onde uma URL inteira costuma
escapar.
