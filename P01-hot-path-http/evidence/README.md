# Evidência

Cada execução da matriz grava uma pasta aqui, no formato
`evidence/<cenário>/<timestamp UTC>/`, com quatro arquivos:

| Arquivo | Para que serve |
|---|---|
| `environment.md` | Máquina, kernel, CPUs, memória, limites do processo e commit medido |
| `commands.txt` | Comandos equivalentes a cada rodada, para reproduzir um cenário isolado |
| `summary.md` | Tabela legível: carga oferecida, concluída, percentis, vazão e erro |
| `metrics.json` | Os mesmos dados em JSON, para outra ferramenta consumir sem reparsear texto |

## Por que guardar isso

Um número de desempenho sem o ambiente em volta não significa nada. "4500 MB/s"
é uma afirmação vazia se ninguém sabe em que máquina, com qual objeto, com quanta
concorrência e com qual versão do código. A pasta de evidência existe para que a
conclusão continue verificável depois que a sessão terminar.

Os perfis do `pprof` de um cenário entram na mesma pasta daquele cenário, não
soltos na raiz. Assim o perfil fica ao lado do número que ele explica.

## O que NÃO entra aqui

Arquivos grandes de carga. Os objetos sintéticos são determinísticos: quem quiser
reproduzir roda `go run ./cmd/cataloggen` e obtém exatamente os mesmos bytes. O
repositório guarda o resumo e os dados agregados que sustentam a conclusão, não os
dados brutos que dá para regerar.
