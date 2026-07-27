# P01 passo a passo

Este documento é a execução do projeto do começo ao fim, na ordem em que as
coisas acontecem. Cada passo tem três partes:

- **o comando**, para copiar e colar;
- **o que você vai ver**, para saber se deu certo;
- **o que roda por baixo**, uma descrição superficial do caminho no código.

O [README](README.md) explica os conceitos e apresenta os resultados medidos.
Aqui o objetivo é outro: acompanhar a máquina funcionando.

Você precisa de Go 1.26 ou mais novo. Docker só é necessário no passo 9.

---

## Passo 1: gerar os objetos de teste

```bash
go run ./cmd/cataloggen -dir testdata/objects
```

**O que você vai ver:** quatro arquivos listados com nome, tamanho e ETag.

```text
catálogo em testdata/objects
  obj-1KiB.bin           1024 bytes  "…"
  obj-64KiB.bin         65536 bytes  "…"
  obj-1MiB.bin        1048576 bytes  "…"
  obj-16MiB.bin      16777216 bytes  "…"
```

**O que roda por baixo:** [cmd/cataloggen/main.go](cmd/cataloggen/main.go) chama
`catalog.Generate`, em [internal/catalog/catalog.go](internal/catalog/catalog.go).
Para cada tamanho, ele deriva uma semente do nome do arquivo e preenche os bytes
com um gerador determinístico. O mesmo nome sempre produz os mesmos bytes, em
qualquer máquina.

Rode de novo e repare que é instantâneo: se o arquivo já existe com o tamanho
certo, ele é preservado. Regerar 16 MiB a cada execução seria desperdício.

**Por que os arquivos não estão no Git:** são grandes e regeneráveis. O
`.gitignore` ignora `testdata/objects/`.

---

## Passo 2: subir o servidor

```bash
go run ./cmd/origin -dir testdata/objects -addr 127.0.0.1:8080 -admin-addr 127.0.0.1:8081
```

**O que você vai ver:** uma linha de log em JSON.

```json
{"time":"…","level":"INFO","msg":"servidor no ar","addr":"127.0.0.1:8080","admin":"127.0.0.1:8081","mode":"streamed","objetos":4,"dir":"testdata/objects"}
```

Deixe rodando e abra outro terminal para os próximos passos.

**O que roda por baixo:** [cmd/origin/main.go](cmd/origin/main.go) carrega o
catálogo, cria os medidores Prometheus e sobe **dois** servidores HTTP:

- **8080** serve os objetos, com os timeouts de leitura e escrita configurados;
- **8081** serve `/metrics` e `/debug/pprof`.

Duas portas porque o `pprof` expõe nomes de funções, memória e goroutines, e
coletar um perfil consome CPU. Na porta pública, qualquer cliente poderia ler
informação interna e atrapalhar a medição.

No fim do arquivo há um `signal.NotifyContext`: ao receber Ctrl+C, o servidor
para de aceitar conexões novas e espera as em andamento terminarem. É o que
diferencia "encerrar" de "derrubar".

---

## Passo 3: a primeira requisição

```bash
curl -sD- -o /dev/null "http://127.0.0.1:8080/objects/obj-1KiB.bin"
```

**O que você vai ver:**

```text
HTTP/1.1 200 OK
Accept-Ranges: bytes
Content-Length: 1024
Content-Type: application/octet-stream
Etag: "9ff5c5709a83bbc88cdae5e9d648e56c"
Last-Modified: Sat, 25 Jul 2026 19:05:06 GMT
X-Origin-Mode: streamed
```

**O que roda por baixo:** o roteador de
[internal/origin/server.go](internal/origin/server.go) casa `GET /objects/{name}`
e chama `serveObject`. Ele:

1. incrementa o medidor de requisições em andamento;
2. embrulha o `ResponseWriter` num `recorder`, para saber o status e os bytes
   que de fato saíram;
3. busca o objeto no catálogo;
4. escreve o `ETag` **antes** de servir, porque é ele que o `http.ServeContent`
   compara com `If-None-Match`;
5. chama `serveWithMode`, o único ponto onde os dois modos divergem;
6. registra duração, status e bytes nas métricas.

Repare no header `X-Origin-Mode`: é o modo em uso, e o padrão é `streamed`.

---

## Passo 4: ver os dois modos lado a lado

O modo pode ser trocado por requisição, com um parâmetro na query:

```bash
curl -s -o /dev/null -w "buffered: %{time_total}s\n" \
  "http://127.0.0.1:8080/objects/obj-16MiB.bin?mode=buffered"
curl -s -o /dev/null -w "streamed: %{time_total}s\n" \
  "http://127.0.0.1:8080/objects/obj-16MiB.bin?mode=streamed"
```

**O que você vai ver:** dois tempos, algo como `buffered: 0.0106s` e
`streamed: 0.0040s`. Nesta máquina o `streamed` já ganha com **uma** requisição de
16 MiB: copiar 16 MiB para a memória custa mais que as syscalls extras.

Não tire conclusão daqui, por dois motivos. Primeiro, uma medição só não é
medição: pode ter sido o cache de página, o escalonador ou o notebook reduzindo a
frequência. Segundo, o benchmark do Go, em objetos de 4 MiB e uma requisição de
cada vez, mostra o **contrário** (o `buffered` ganha em tempo por operação). A
diferença que interessa aparece sob concorrência, e é isso que os passos seguintes
vão provocar.

**O que roda por baixo:** em `serveWithMode`, a diferença cabe em duas linhas.

```go
case ModeBuffered:
    data, err := os.ReadFile(obj.Path)          // o objeto INTEIRO na memória
    http.ServeContent(w, r, obj.Name, info.ModTime(), bytes.NewReader(data))

case ModeStreamed:
    f, err := os.Open(obj.Path)                 // só um descritor
    defer f.Close()
    http.ServeContent(w, r, obj.Name, info.ModTime(), f)
```

Todo o resto é idêntico, de propósito: o mesmo `http.ServeContent`, o mesmo
contrato HTTP, os mesmos headers. Se os dois caminhos tivessem implementações
diferentes, qualquer diferença de tempo poderia vir do código, não da estratégia.

Trocar o modo por requisição também tem um motivo de método: a matriz de carga
compara os dois **no mesmo processo**, com o mesmo cache de página do sistema
operacional, removendo diferenças de ambiente que seriam confundidas com
diferença de estratégia.

---

## Passo 5: olhar as métricas do servidor

```bash
curl -s http://127.0.0.1:8081/metrics | grep -E "^origin_http_(requests|response_bytes)"
```

**O que você vai ver:** os contadores das requisições que você já fez, separados
por modo, método e código de status.

```text
origin_http_requests_total{code="200",method="GET",mode="buffered"} 1
origin_http_requests_total{code="200",method="GET",mode="streamed"} 2
origin_http_response_bytes_total{mode="buffered"} 1.6777216e+07
```

**O que roda por baixo:** [internal/metrics/metrics.go](internal/metrics/metrics.go)
declara cada medidor com um propósito escrito ao lado. O registro é próprio, não
o global do Prometheus, para que cada teste crie o seu sem estado compartilhado.

O mesmo endpoint também expõe os coletores de runtime do Go, que são o que
conecta a pergunta do lab às causas: memória alocada, coletas de lixo, goroutines
e descritores de arquivo. Os passos seguintes leem exatamente esses números.

---

## Passo 6: uma medição de verdade

```bash
go run ./cmd/loadgen -object obj-16MiB.bin -mode buffered -concurrency 64 -duration 8s
```

**O que você vai ver:** duas blocos de números.

```text
cenário         buffered-obj-16MiB.bin-c64
janela          8.1s
carga oferecida 5306 (657.9/s)
carga concluída 5306 (657.9/s)
erros           0 (taxa 0.00%) map[200:5306]
latência        p50 90.75ms  p95 161.56ms  p99 196.26ms  max 284.55ms
-- o servidor relatou --
vazão           10525.7 MB/s
alocado         89.26 GB na janela
heap no fim     950.7 MB
coletas         100 (número isolado engana, veja o README)
goroutines      138 no fim
cancelamentos   0 (cliente desistiu no meio)
```

Rode de novo trocando `-mode buffered` por `-mode streamed` e compare a linha
`alocado`. É a comparação central do projeto.

**O que roda por baixo:** [cmd/loadgen/main.go](cmd/loadgen/main.go) monta uma
`loadtest.Config` e chama `loadtest.Run`, em
[internal/loadtest/loadtest.go](internal/loadtest/loadtest.go). O que acontece lá
dentro, em ordem:

1. **aquecimento** com um atacante descartável, cujo resultado é jogado fora: as
   primeiras requisições pagam abertura de conexão e carga do arquivo no cache do
   sistema operacional, custos que não se repetem;
2. **primeira leitura do `/metrics`** do servidor;
3. **o ataque**, com o Vegeta: `Rate` zero significa "taxa máxima limitada pelo
   número de workers", ou seja, exatamente 64 clientes em laço;
4. **segunda leitura do `/metrics`**;
5. **a diferença** entre as duas leituras vira as linhas de "o servidor relatou".

A separação entre os dois blocos é deliberada: quem mede não deveria ser quem é
medido. O cliente sabe quanto tempo cada requisição levou; só o servidor sabe
quanta memória gastou para responder.

Detalhe que muda o resultado: o Vegeta é configurado com `MaxBody(0)`, que drena
o corpo sem guardá-lo. Sem isso, o gerador alocaria uma cópia de cada objeto de
16 MiB recebido e viraria o maior alocador da máquina, bem no experimento que
mede alocação.

---

## Passo 7: a matriz completa

```bash
go run ./cmd/matrix \
  -objects obj-64KiB.bin,obj-16MiB.bin \
  -concurrency 8,128 \
  -repeats 2 \
  -duration 5s
```

**O que você vai ver:** uma linha de log por rodada, e no fim o caminho da
evidência gravada.

```text
2026/07/25 18:41 matriz: 16 rodadas, ~2m8s de execução
2026/07/25 18:41 [1/16] modo=buffered objeto=obj-64KiB.bin conc=8 rep=1
2026/07/25 18:41   concluída=23044.5/s p99=0.89ms 1452.8MB/s alocado=12.9GB erro=0.00%
…
2026/07/25 18:43 evidência em evidence/matriz-buffered-vs-streamed/20260725T214122Z
```

**O que roda por baixo:** [cmd/matrix/main.go](cmd/matrix/main.go) percorre o
produto (objetos × concorrências × modos × repetições) e chama `loadtest.Run` em
cada combinação. Três decisões de método estão codificadas ali:

- **uma variável por comparação:** os laços mantêm tudo fixo e variam um eixo de
  cada vez. Mudar duas ao mesmo tempo torna impossível atribuir a diferença;
- **repetições:** não é para pegar o melhor resultado, é para ver dispersão. Se
  as rodadas do mesmo cenário discordam muito, a conclusão é frágil;
- **pausa entre rodadas** (`-settle`): deixa o coletor de lixo terminar, as
  conexões em `TIME_WAIT` drenarem e o cache assentar. Sem ela, uma rodada herda
  a bagunça da anterior.

Se você interromper com Ctrl+C, ele grava o que já mediu em vez de perder tudo.

---

## Passo 8: ler a evidência

```bash
ls evidence/matriz-buffered-vs-streamed/*/
cat evidence/matriz-buffered-vs-streamed/*/summary.md
```

**O que você vai ver:** quatro arquivos, sempre os mesmos.

| Arquivo          | Conteúdo                                                          |
| ---------------- | ----------------------------------------------------------------- |
| `environment.md` | máquina, kernel, CPUs, commit, limites do processo                |
| `commands.txt`   | o comando equivalente a cada rodada, para reproduzir uma isolada  |
| `summary.md`     | duas tabelas: o que o cliente viu, o que o servidor relatou       |
| `metrics.json`   | os mesmos dados para outra ferramenta consumir                    |

**O que roda por baixo:** `loadtest.SaveEvidence` monta o diretório
`evidence/<cenário>/<timestamp UTC>/` e escreve os quatro arquivos. O
`environment.md` é preenchido chamando `git`, `uname`, `free` e `ulimit`; se
algum não existir na máquina, o campo vira "(indisponível)" e o experimento
continua válido, só com evidência menos completa.

Ao ler o `summary.md`, atenção a uma armadilha: na tabela do servidor, só
"alocado na janela" é diferença de contador. "Heap no fim" e "goroutines no fim"
são fotos de um processo que atendeu todos os cenários em sequência, então
carregam resíduo da rodada anterior.

---

## Passo 9: o experimento de coleta de lixo, com heap limpo

Para heap sem resíduo, o servidor precisa ser reiniciado a cada modo. É o que o
cenário `gc-buffered-vs-streamed` faz:

```bash
# Terminal 1: servidor limpo
go run ./cmd/origin -dir testdata/objects -addr 127.0.0.1:8080 -admin-addr 127.0.0.1:8081

# Terminal 2
go run ./cmd/loadgen -object obj-16MiB.bin -mode buffered -concurrency 64 -duration 8s
curl -s http://127.0.0.1:8081/debug/pprof/heap -o /tmp/heap-buffered.pprof

# Reinicie o servidor (Ctrl+C no terminal 1 e suba de novo), depois:
go run ./cmd/loadgen -object obj-16MiB.bin -mode streamed -concurrency 64 -duration 8s
curl -s http://127.0.0.1:8081/debug/pprof/heap -o /tmp/heap-streamed.pprof
```

Para abrir um perfil:

```bash
go tool pprof -http=:9000 /tmp/heap-buffered.pprof
```

**O que você vai ver:** um navegador com o gráfico de quem está segurando memória.
No modo `buffered`, o topo é `os.ReadFile`. No `streamed`, não há nada parecido.

Os perfis desta máquina estão guardados em
[evidence/gc-buffered-vs-streamed/](evidence/gc-buffered-vs-streamed/).

---

## Passo 10: a falha controlada, com container

```bash
go test -tags=integration ./test/... -v
```

**O que você vai ver:** dois testes, e no log deles os números do experimento.

```text
=== RUN   TestCargaNormalConcluiTudo
    baseline: oferecida 77689, concluída 77689, p99 1.70ms, 25.3 MB/s
--- PASS
=== RUN   TestEsgotarDescritoresSeparaOferecidaDeConcluida
    com nofile=64: oferecida 188128, concluída 8116, erros 180012 (95.7%), códigos map[0:202 200:8116 500:179810]
--- PASS
```

**O que roda por baixo:**
[test/fdlimit_integration_test.go](test/fdlimit_integration_test.go) usa o
testcontainers para:

1. gerar um catálogo num diretório temporário;
2. construir a imagem a partir do [Dockerfile](Dockerfile);
3. subir o container com `nofile=64`, ou seja, 64 descritores de arquivo;
4. esperar o `/healthz` responder;
5. jogar 256 clientes simultâneos em cima, pelo mesmo `loadtest.Run` dos passos
   anteriores;
6. derrubar tudo.

O primeiro teste é a linha de base, sem limite apertado: tudo que foi oferecido
conclui. Sem ele, o segundo não teria com o que comparar.

**A parte interessante** é o formato da falha. A expectativa escrita antes de
rodar era ver `accept: too many open files`. O que apareceu foram 179.810
respostas **500**: no modo `streamed`, quem fica sem descritor primeiro é o
`os.Open` dentro do handler, não o `accept` do socket. O servidor continua
aceitando conexões e passa a responder erro em todas.

---

## Passo 11: testes e benchmarks

```bash
go test -race ./...
go test ./internal/origin -bench=. -benchmem -run '^$'
```

**O que você vai ver:** a suíte passando com o detector de corrida, e a tabela de
benchmarks com `ns/op`, `B/op` e `allocs/op` para cada modo e tamanho.

**O que roda por baixo:** o `-race` liga o detector de [condição de
corrida](https://pt.wikipedia.org/wiki/Condi%C3%A7%C3%A3o_de_corrida), que avisa
quando duas goroutines mexem na mesma variável sem coordenação. É um bug que não
aparece em teste normal: funciona 999 vezes e quebra na milésima, em produção.

Os benchmarks estão em
[internal/origin/bench_test.go](internal/origin/bench_test.go) e medem **uma
requisição de cada vez**. É por isso que eles não contam a história toda: no
objeto de 4 MiB, o `buffered` ganha em `ns/op` e perde feio em `B/op`. A conta só
inverte sob concorrência, que é o que os passos 6 e 7 medem.

---

## Passo 12: rodar em container

```bash
docker build -t p01-origin .
docker run --rm \
  --read-only \
  -v "$PWD/testdata/objects:/data/objects:ro" \
  -p 8080:8080 \
  p01-origin
```

**O que você vai ver:** o mesmo servidor do passo 2, agora isolado.

**O que roda por baixo:** o [Dockerfile](Dockerfile) é multi-stage: compila num
estágio com o Go completo e copia só o binário para uma imagem `distroless`, que
não tem shell nem gerenciador de pacotes. O container roda como usuário
`nonroot`, com o sistema de arquivos somente leitura, e o volume dos objetos é
montado `:ro` porque o servidor só lê.

Repare que a porta administrativa (8081) **não** é publicada.

---

## Encerrando

Ctrl+C no terminal do servidor. Ele para de aceitar conexões novas, espera as
requisições em andamento e sai. Os arquivos de `testdata/objects/` podem ser
apagados à vontade: o passo 1 os reconstrói byte a byte.

A partir daqui, o [README](README.md) tem os resultados medidos, a discussão de
cada conceito e, mais importante, a lista do que estes números **não** provam.
