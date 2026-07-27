# Evidência

Cada execução da matriz grava uma pasta aqui, no formato
`evidence/<cenário>/<timestamp UTC>/`, com quatro arquivos:

| Arquivo | Para que serve |
|---|---|
| `environment.md` | Máquina, kernel, CPUs, memória, limites do processo e commit medido |
| `commands.txt` | Comandos equivalentes a cada rodada, para reproduzir um cenário isolado |
| `summary.md` | Duas tabelas: o que o cliente observou e o que o servidor relatou de si mesmo |
| `metrics.json` | Os mesmos dados em JSON, para outra ferramenta consumir sem reparsear texto |

## Por que guardar isso

Um número de desempenho sem o ambiente em volta não significa nada. "4500 MB/s"
não diz em que máquina, com qual objeto, com quanta concorrência nem com qual
versão do código. A pasta de evidência guarda esse contexto para que a conclusão
continue verificável depois que a sessão terminar.

Os perfis do `pprof` de um cenário entram na mesma pasta daquele cenário, não
soltos na raiz. Assim o perfil fica ao lado do número que ele explica.

## Por que o resumo tem duas tabelas

Quem mede não deveria ser quem é medido. O cliente sabe quantas requisições
terminaram e quanto tempo cada uma levou, mas não tem como saber quanta memória o
servidor alocou nem quantas coletas de lixo ([garbage collection](https://pt.wikipedia.org/wiki/Coletor_de_lixo))
aconteceram. Essas grandezas vêm do `/metrics` do próprio servidor, lido antes e
depois de cada janela.

Na tabela do servidor, só o **total alocado** é diferença de contador. "Heap no
fim" e "goroutines no fim" são fotos de um processo que atende todos os cenários
em sequência, então carregam resíduo da rodada anterior. Para heap limpo, o
cenário `gc-buffered-vs-streamed` reinicia o servidor a cada modo.

## O que NÃO entra aqui

Arquivos grandes de carga. Os objetos sintéticos são determinísticos: quem quiser
reproduzir roda `go run ./cmd/cataloggen` e obtém exatamente os mesmos bytes. O
repositório guarda o resumo e os dados agregados que sustentam a conclusão, não os
dados brutos que dá para regerar.
