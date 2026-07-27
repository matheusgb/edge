# P01: hot path HTTP

> **A pergunta deste lab:** por que carregar um arquivo inteiro na memória e
> enviá-lo em fluxo produzem comportamentos tão diferentes quando muita gente
> pede ao mesmo tempo?

O "hot path" é o caminho que o código percorre com mais frequência, por onde
passa a maior parte das requisições. É onde otimizar vale a pena e onde
desperdício dói. Neste projeto o hot path é a entrega de um arquivo por HTTP.

Este projeto sobe um servidor que entrega arquivos de duas maneiras diferentes,
e mede as duas lado a lado. É um laboratório de estudo: o objetivo não é ter o
servidor mais rápido do mundo, é **entender o porquê** de uma estratégia ganhar
da outra, e aprender a provar isso com número em vez de opinião.

Se você programa mas nunca mexeu com desempenho de servidor, este README foi
escrito para você. Cada conceito aparece no momento em que faz falta.

> **Quer só rodar e ver funcionando?** O [PASSO-A-PASSO.md](PASSO-A-PASSO.md)
> executa o projeto do início ao fim, um comando de cada vez, dizendo o que
> esperar de cada saída e o que está acontecendo no código naquele momento.
> Este README é o "por quê", aquele é o "como".

---

## As duas estratégias

Imagine que você trabalha num restaurante e precisa levar um bolo de 16 kg da
cozinha até a mesa.

**Estratégia 1, `buffered` (tudo na bandeja):** você pega o bolo inteiro,
coloca numa bandeja e leva de uma vez. Simples. Mas se chegarem 128 pedidos ao
mesmo tempo, você precisa de 128 bandejas gigantes, e a cozinha não tem espaço.

**Estratégia 2, `streamed` (fatia por fatia):** você leva o bolo em fatias,
com um pratinho pequeno, indo e voltando. Cada viagem carrega pouco, então o
pratinho é o mesmo independente do tamanho do bolo. Você faz mais viagens, mas
nunca precisa de espaço para o bolo inteiro.

No código, isso é literalmente:

```go
// buffered: lê o arquivo INTEIRO para a memória, depois responde
data, _ := os.ReadFile(caminho)                        // 16 MiB na memória
http.ServeContent(w, r, nome, modtime, bytes.NewReader(data))

// streamed: abre o arquivo e deixa o Go copiar em pedaços
f, _ := os.Open(caminho)                               // só um descritor
defer f.Close()
http.ServeContent(w, r, nome, modtime, f)
```

A única diferença é o que passamos no último argumento. Todo o resto
(cabeçalhos, códigos de status, suporte a download parcial) é o mesmo
`http.ServeContent` nos dois casos. Isso é proposital: é o que torna a
comparação honesta. Se os dois caminhos tivessem implementações diferentes,
qualquer diferença de velocidade poderia vir do código, não da estratégia.

O código está em [internal/origin/server.go](internal/origin/server.go).

---

## Os conceitos, na ordem em que importam

### Memória, alocação e o coletor de lixo

Go aloca memória dinamicamente e usa um [coletor de
lixo](https://pt.wikipedia.org/wiki/Coletor_de_lixo) (garbage collector) para
liberar o que não está mais em uso. Esse trabalho consome CPU: quanto mais
lixo o programa produz, mais CPU o coletor gasta, e menos sobra para atender
requisições.

No modo `buffered`, cada requisição de um arquivo de 16 MiB aloca 16 MiB. Com
64 clientes simultâneos, isso são mais de 1 GB de memória viva ao mesmo tempo,
e tudo vira lixo assim que a resposta termina. No modo `streamed`, cada
requisição usa um buffer de 32 KiB, reaproveitado. O consumo praticamente não
muda com o tamanho do arquivo.

### Latência, vazão e saturação

Três palavras que parecem sinônimos e não são:

- **Latência** é quanto tempo UMA requisição demora, medida em milissegundos.
- **Vazão** (throughput) é quantas requisições você atende POR SEGUNDO.
- **Saturação** é o quanto o sistema já está no limite.

Dá para ter vazão alta e latência péssima ao mesmo tempo: basta enfileirar
todo mundo. Por isso medimos os dois, e por isso olhamos **percentis** em vez
de média.

### Por que percentil e não média

Média esconde os casos ruins. Se 99 requisições levam 1 ms e uma leva 10
segundos, a média dá 100 ms, um número que não descreve ninguém. O
[percentil](https://pt.wikipedia.org/wiki/Percentil) responde outra pergunta:
qual o tempo que 99% das requisições conseguiram bater? O p99 mostra a
experiência do usuário azarado, que é justamente quem reclama.

Nos relatórios deste lab aparecem p50 (a mediana), p95 e p99.

### Carga oferecida e carga concluída

Este é o erro clássico de quem mede desempenho: contar só o que deu certo.

- **Carga oferecida** é tudo que o cliente TENTOU fazer.
- **Carga concluída** é o que realmente terminou com sucesso.

Se um servidor recebe 1000 pedidos e só responde 500, e você mede só os 500,
ele parece ótimo. As requisições lentas viraram erro e sumiram da conta. Por
isso o gerador de carga deste lab conta as duas coisas **separadamente**, e as
duas aparecem no relatório.

### User space, kernel space e syscalls

O sistema operacional separa o programa, rodando em user space, sem acesso
direto ao hardware, do kernel, que fala com disco e rede em kernel space.
Toda leitura de arquivo ou escrita em socket passa por uma [chamada de
sistema](https://pt.wikipedia.org/wiki/Chamada_de_sistema) (syscall), um
pedido formal ao kernel que tem custo: o processador troca de contexto, salva
e restaura estado.

O trade-off do lab em uma frase: **`buffered` faz menos syscalls porém aloca
muito; `streamed` aloca pouco porém faz mais syscalls.** Qual ganha depende do
tamanho do objeto e da concorrência, e é exatamente isso que vamos medir.

### Descritores de arquivo

Quando o programa abre um arquivo ou aceita uma conexão de rede, o kernel
devolve um descritor de arquivo (file descriptor, ou fd), um número que
identifica aquele recurso aberto. Todo processo tem um limite de quantos pode
ter.

Isso importa porque cada conexão HTTP aberta consome um fd. Um servidor com
10.000 conexões simultâneas precisa de mais de 10.000 fds, ou começa a falhar
com "too many open files". O experimento de falha controlada explora isso.

---

## O que foi medido de verdade

Tudo abaixo foi medido nesta máquina, não copiado de blog. A evidência bruta
está em [evidence/](evidence/).

### A máquina onde isto rodou

| Item                                       | Valor                               |
| ------------------------------------------ | ------------------------------------ |
| CPU                                        | Intel Core i9-13980HX (13ª geração) |
| Processadores lógicos visíveis ao processo | 32                                  |
| Memória disponível ao Linux                | 15 GiB                              |
| Sistema                                    | Ubuntu sobre WSL2                   |
| Kernel                                     | 5.15.167.4-microsoft-standard-WSL2  |
| Arquitetura                                | linux/amd64                         |
| Go                                         | 1.26.5                              |
| Limite de descritores por processo         | 1.048.576                           |

Quatro coisas dessa tabela mudam a leitura dos números.

**É um notebook, não um servidor.** Um processador móvel reduz a frequência
conforme esquenta, então uma rodada longa pode ficar mais lenta que uma curta
sem que nada no código tenha mudado. É um dos motivos para repetir cada
cenário em vez de confiar numa medição só.

**É WSL2, não Linux direto no hardware.** O kernel é uma máquina virtual leve
rodando sobre o Windows, e o sistema de arquivos e a rede passam por camadas
que não existem num servidor de verdade. Os números servem para comparar as
duas estratégias entre si, não para estimar o que a mesma máquina faria sem
essa camada.

**Os 32 processadores são divididos entre o servidor e quem gera a carga.** Os
dois rodam aqui dentro e brigam pela mesma CPU. É a interferência discutida
mais adiante, e é por isso que os valores absolutos valem menos que a
diferença entre os modos.

**Os 15 GiB são o que o WSL2 entrega ao Linux**, não a memória total do
notebook. Por padrão o WSL2 reserva cerca de metade da RAM do host. Esse é o
teto que o modo `buffered` está consumindo quando aloca centenas de megabytes
por rajada.

O limite de descritores, mais de um milhão, é folgado demais para esgotar por
acidente. Por isso o experimento de falha controlada precisa de um container
com o limite reduzido de propósito, para o problema aparecer.

### Alocação de memória (benchmark do Go)

```
BenchmarkServeObject/obj-4MiB.bin/buffered-32    1389318 ns/op   4261391 B/op   105 allocs/op
BenchmarkServeObject/obj-4MiB.bin/streamed-32    2043682 ns/op     40971 B/op    91 allocs/op
```

Leia a coluna `B/op` (bytes alocados por operação): **4.261.391 contra
40.971**. Cem vezes mais memória para entregar exatamente o mesmo arquivo.

Repare em `allocs/op`: 105 contra 91, praticamente igual. **Não é o número de
alocações que difere, é o tamanho delas.** Uma alocação de 4 MiB é muito mais
cara para o coletor de lixo do que cento e vinte alocações de 32 KiB.

Neste benchmark, o `buffered` foi MAIS RÁPIDO por operação (1,39 ms contra
2,04 ms). Não é contradição com o resto do README: é a diferença entre medir
uma requisição de cada vez e medir muitas ao mesmo tempo. Com uma requisição
isolada, sobra memória, o coletor de lixo quase não trabalha e a alocação
única de 4 MiB sai barata; o `streamed` paga as syscalls e perde. É sob
concorrência que a conta vira, e é por isso que o lab não se contenta com
benchmark.

### O experimento principal: 64 clientes pedindo 16 MiB

Servidor reiniciado limpo para cada modo, 8 segundos de medição. Os números de
latência e de carga vêm do cliente; os de memória vêm do `/metrics` do próprio
servidor, lido antes e depois da janela.

|                          | `buffered` | `streamed` |       diferença |
| ------------------------ | ---------: | ---------: | --------------: |
| Requisições concluídas   |      5.306 |     11.434 |   **2,2× mais** |
| Concluídas por segundo   |      657,9 |    1.425,1 |   **2,2× mais** |
| Latência p50             |   90,75 ms |   42,06 ms | **2,2× melhor** |
| Latência p99             |  196,26 ms |  106,85 ms | **1,8× melhor** |
| Total alocado na janela  |   89,26 GB |    0,42 GB |  **213× menos** |
| Heap em uso no fim       |   950,7 MB |     9,9 MB |   **96× menos** |
| Ciclos de coleta de lixo |        100 |        148 |   _veja abaixo_ |

O `streamed` ganhou em tudo que interessa. Mas a última linha merece atenção,
porque ela ensina mais que as outras.

Uma conta que fecha e vale a pena conferir: o `buffered` alocou 89,26 GB em 8
segundos enquanto entregava 10,5 GB/s de conteúdo. Os dois números são
praticamente o mesmo, e não é coincidência: **cada requisição aloca uma cópia
inteira do objeto**. É a definição de "carregar o arquivo na memória", vista
de fora, em bytes.

### A linha que parece contradizer o resto

O `streamed` teve **quase 50% mais ciclos de coleta de lixo** (148 contra
100). Se coleta de lixo é ruim, como ele ganhou?

Porque **contar ciclos de GC é a métrica errada**. O coletor do Go dispara
quando o heap cresce uma certa proporção em relação ao tamanho anterior. Como
o heap do `streamed` é pequeno (9,9 MB), ele atinge esse gatilho com
frequência, mas cada coleta é trivial, porque há pouquíssima coisa para
examinar.

O `buffered` coletou menos vezes, e cada coleta teve que lidar com quase um
gigabyte. O que dói não é a frequência, é o **volume**: 89,26 GB alocados
contra 0,42 GB.

Essa é uma lição que vale além deste lab: **um número isolado engana**. Quem
tivesse olhado só "ciclos de GC" teria concluído exatamente o contrário da
verdade.

### O efeito da concorrência

Da matriz completa, comparando o mesmo objeto de 16 MiB em duas concorrências:

| Concorrência | `buffered` p99 | `streamed` p99 |
| -----------: | -------------: | -------------: |
|   8 clientes |       18,94 ms |        6,89 ms |
| 128 clientes |      807,77 ms |      191,23 ms |

Com 8 clientes, o `buffered` é ruim. Com 128, ele é **43 vezes pior que ele
mesmo**. A degradação não é proporcional à carga, ela acelera. É assim que
saturação se parece num gráfico: a curva não sobe, ela vira.

### Uma coluna que não dá para ler ingenuamente

O resumo da matriz traz também "heap no fim" e "goroutines no fim" de cada
cenário, e esses dois números merecem uma ressalva.

Eles são **gauges de um processo que atende todos os cenários em sequência**.
O heap medido no fim de uma rodada carrega lixo que a rodada anterior deixou e
o coletor ainda não recolheu; as goroutines incluem conexões ociosas de
rodadas passadas, que só somem quando o `IdleTimeout` vence. Numa das linhas o
`streamed` aparece com 1,2 GB de heap, herança direta do `buffered` que rodou
antes.

A medida confiável por cenário é a **diferença** de um contador monotônico,
como o total alocado, e não a foto de um gauge. Para o heap limpo, o
experimento de coleta de lixo reinicia o servidor a cada modo. Fica como
lembrete: uma tabela bonita pode conter duas colunas medidas com rigores
diferentes.

---

## Como rodar

Você precisa de Go 1.26 ou mais novo. Docker é necessário para o teste de
integração da falha controlada.

Esta seção é a referência dos comandos. Para a execução guiada, com o que
esperar de cada saída, veja o [PASSO-A-PASSO.md](PASSO-A-PASSO.md).

### 1. Gerar os arquivos de teste

```bash
go run ./cmd/cataloggen -dir testdata/objects
```

Isso cria arquivos de 1 KiB, 64 KiB, 1 MiB e 16 MiB. O conteúdo é
**determinístico**: gerado a partir do nome, sempre os mesmos bytes, em
qualquer máquina. Por isso eles não vão para o Git, já que regerar é mais
barato que versionar.

### 2. Subir o servidor

```bash
go run ./cmd/origin -dir testdata/objects -addr 127.0.0.1:8080 -admin-addr 127.0.0.1:8081
```

Duas portas, de propósito:

- **8080** serve os objetos (é a porta pública);
- **8081** serve as métricas e o `pprof` (é a porta administrativa).

O `pprof` mostra nomes de funções, uso de memória e goroutines do processo, e
coletar um perfil consome CPU. Deixar isso na porta pública seria entregar
informação interna a qualquer um e permitir que qualquer cliente atrapalhasse
a medição. **Em produção, a porta administrativa nunca é exposta à
internet.**

### 3. Uma medição rápida

```bash
go run ./cmd/loadgen -object obj-16MiB.bin -mode buffered -concurrency 64 -duration 8s
go run ./cmd/loadgen -object obj-16MiB.bin -mode streamed -concurrency 64 -duration 8s
```

A saída tem duas partes. A de cima é o que o **cliente** observou: carga
oferecida, carga concluída, percentis. A de baixo é o que o **servidor**
relatou de si mesmo, lido do `/metrics` da porta administrativa antes e depois
da janela: bytes entregues, memória alocada, coletas de lixo, goroutines.

Essa separação não é enfeite. Quem mede não deveria ser quem é medido, e o
cliente não tem como saber quanta memória o servidor gastou para responder.
Para bytes entregues, o número do servidor é mais confiável que o do cliente,
porque o gerador drena o corpo sem guardá-lo (veja a nota sobre o Vegeta mais
abaixo).

Por padrão o gerador roda em **modelo fechado**: N clientes em laço, cada um
esperando a resposta antes de pedir de novo. É o que a pergunta do lab exige,
já que ela é sobre N downloads simultâneos. Para atacar a uma taxa fixa, use
`-rate`:

```bash
go run ./cmd/loadgen -object obj-1MiB.bin -rate 2000 -concurrency 256 -duration 10s
```

### 4. A matriz completa (o experimento de verdade)

```bash
go run ./cmd/matrix \
  -objects obj-64KiB.bin,obj-16MiB.bin \
  -concurrency 8,128 \
  -repeats 3 \
  -duration 10s
```

A matriz roda cada combinação de (modo × objeto × concorrência) várias vezes e
grava tudo em `evidence/<cenário>/<data-hora>/`.

**Por que repetir?** Não é para pegar o melhor resultado. É para ver
**dispersão**. Se as três rodadas do mesmo cenário discordam muito entre si, a
conclusão é frágil e o relatório precisa dizer isso em vez de esconder atrás
de uma média.

### 5. Testes e benchmarks

```bash
go test -race ./...                                          # tudo, com detector de corrida
go test ./internal/origin -bench=. -benchmem -run '^$'        # benchmarks
go test -tags=integration ./test/... -v                       # a falha controlada, em container
```

O `-race` liga o detector de corrida de dados, que avisa quando duas
goroutines mexem na mesma variável ao mesmo tempo sem coordenação. É um bug
que não aparece em teste normal: funciona 999 vezes e quebra na milésima, em
produção, sem deixar rastro. A suíte deste projeto passa com `-race`.

### 6. Container

```bash
docker build -t p01-origin .
docker run --rm \
  --read-only \
  -v "$PWD/testdata/objects:/data/objects:ro" \
  -p 8080:8080 \
  p01-origin
```

O [Dockerfile](Dockerfile) é multi-stage: compila num estágio com o Go
completo e copia só o binário para uma imagem `distroless`, que não tem shell
nem gerenciador de pacotes. O container roda como usuário `nonroot`, com o
sistema de arquivos somente leitura e o volume dos objetos montado `:ro`.

---

## Diagnóstico: como investigar por conta própria

Medir diz _o quê_. Diagnosticar diz _por quê_. Com o servidor sob carga:

```bash
# Onde a CPU está sendo gasta (30 segundos de amostragem)
go tool pprof -http=:9000 http://127.0.0.1:8081/debug/pprof/profile?seconds=30

# Quem está segurando memória agora
go tool pprof -http=:9000 http://127.0.0.1:8081/debug/pprof/heap

# Quantas goroutines existem e onde estão paradas
curl "http://127.0.0.1:8081/debug/pprof/goroutine?debug=1" | head -40

# Conexões TCP e seus estados
ss -tan | awk '{print $1}' | sort | uniq -c

# Limite e uso de descritores de arquivo do processo
PID=$(pgrep -f 'origin -dir')
grep 'open files' /proc/$PID/limits
ls /proc/$PID/fd | wc -l

# Resumo de syscalls, que mostra o custo da travessia para o kernel
strace -c -p $PID   # Ctrl+C após alguns segundos
```

Os perfis de heap deste lab estão guardados em
[evidence/gc-buffered-vs-streamed/](evidence/gc-buffered-vs-streamed/). Para
abrir:

```bash
go tool pprof -http=:9000 evidence/gc-buffered-vs-streamed/*/heap-buffered.pprof
```

Uma regra que vale levar para a vida: **consultar um valor de `sysctl` é
diferente de recomendar mudá-lo.** Só se mexe em parâmetro de kernel dentro de
ambiente descartável, com baseline medido e hipótese escrita antes.

---

## Falha controlada: esgotando os descritores

O servidor não quebra só por estar lento. Ele quebra por acabar um recurso. E
o recurso mais fácil de acabar num servidor HTTP é o descritor de arquivo,
porque cada conexão aberta consome um.

Nesta máquina o limite é de mais de um milhão, folgado demais para esgotar
por acidente. Por isso o experimento precisa de um container descartável com
o limite baixado de propósito. Ele é um teste automatizado, em
[test/fdlimit_integration_test.go](test/fdlimit_integration_test.go):

```bash
go test -tags=integration ./test/... -v
```

O teste constrói a imagem, sobe o servidor com `nofile=64`, joga 256 clientes
simultâneos em cima e derruba tudo no fim. Quem faz esse trabalho é o
testcontainers; o teste só descreve o que espera.

O que foi medido, com 256 clientes e 64 descritores:

| Medida             |     Valor |
| ------------------ | --------: |
| Carga oferecida    |   188.128 |
| Carga concluída    |     8.116 |
| Erros              |   180.012 |
| Taxa de erro       |     95,7% |

A linha de base, com o mesmo servidor sem o limite apertado: **77.689
oferecidas, 77.689 concluídas, zero erro**. É a comparação que dá sentido ao
número.

Uma surpresa útil: a falha **não** apareceu como `accept: too many open
files`, que era a expectativa escrita antes de rodar. A maior parte veio como
**500** do próprio servidor (179.810 respostas), e só 202 como erro de
transporte. O motivo é que o modo `streamed` abre o arquivo por requisição:
quando não há descritor sobrando, quem falha primeiro é o `os.Open` dentro do
handler, não o `accept` do socket. O servidor continua aceitando conexões e
passa a responder erro em todas.

Isso muda a lição de operação. Um alerta que procurasse "too many open files"
nos logs do sistema não veria nada; o sintoma visível é uma explosão de 500
com a carga concluída despencando. **A exaustão de recurso não escolhe o
lugar mais óbvio para se manifestar.**

**A lição também não é "aumente o ulimit".** É reconhecer o sintoma de
exaustão de recurso e saber distinguir de lentidão. Aumentar o limite sem
entender por que está acabando só adia o problema, e às vezes esconde um
vazamento de conexões.

---

## Como interpretar isto sem exagerar a conclusão

Esta parte é a mais importante do README, e a mais fácil de pular.

**Os números de MB/s aqui são fisicamente impossíveis numa rede real.** O
relatório mostra quase 29.700 MB/s, ou seja mais de 29 GB/s. Nenhuma placa de
rede comum faz isso. O que aconteceu: cliente e servidor estão na mesma
máquina, falando por `localhost`, e os arquivos já estavam no cache de página
do sistema operacional. Não houve rede nem disco de verdade no caminho. Esses
números medem **o custo de CPU e memória do código**, e é só para isso que
servem.

**O gerador de carga disputa CPU com o servidor medido.** Os dois rodam na
mesma máquina, e medir direito pede o contrário: quem gera a carga fica numa
máquina separada, para não roubar CPU de quem está sendo medido. Um gerador
ocupado atrasa as próprias requisições e registra isso como se fosse lentidão
do servidor. Fizemos assim por simplicidade de laboratório, o que significa
que os números absolutos estão contaminados pela interferência. A
**comparação relativa** entre os modos continua válida, porque os dois
sofreram a mesma interferência.

**Medição local não é capacidade de produção.** Esta é a distinção que separa
quem aprendeu de quem decorou. Um resultado local não autoriza a dizer "nosso
servidor aguenta 1.425 requisições por segundo". Ele autoriza a dizer "nesta
máquina, nestas condições, com este objeto, o modo streamed atendeu 2,2 vezes
mais requisições que o buffered". A primeira frase é marketing, a segunda é
engenharia.

**O resultado não foi decidido antes da medição, e o `buffered` chegou a
ganhar.** Era plausível que ele levasse vantagem quando o custo das syscalls
superasse o de uma alocação, e foi o que aconteceu no benchmark de 4 MiB, com
UMA requisição de cada vez: 1,39 ms contra 2,04 ms. Sob concorrência, no
mesmo objeto, a conta inverteu e ele passou a perder por mais de duas vezes.

Isso é o oposto de um detalhe. Um relatório que citasse só o benchmark
defenderia a estratégia errada, com número real na mão. A hipótese era
legítima, a medição isolada confirmou, e só a medição sob carga mostrou o que
importa. É por isso que o lab não se contenta com uma medição só.

---

## Estrutura do projeto

```
PASSO-A-PASSO.md  a execução guiada, comando a comando
cmd/
  origin/       servidor de origem (o que está sendo estudado)
  cataloggen/   gera os arquivos sintéticos
  loadgen/      roda UM cenário de carga
  matrix/       roda a matriz completa e grava a evidência
internal/
  catalog/      catálogo determinístico de objetos
  metrics/      medidores Prometheus
  origin/       o servidor de verdade, mais testes e benchmarks
  loadtest/     cenário de carga e formato de evidência
  promscrape/   leitura do /metrics do servidor, para medir o que o cliente não vê
test/           a falha controlada dos descritores, em container
evidence/       resultados medidos, um diretório por execução
```

Cada arquivo `.go` tem comentários explicando **por que** as decisões foram
tomadas, não só o que o código faz. Ler o código junto com este README é
parte do estudo.

### O que este projeto não escreveu à mão

A pergunta do lab é sobre o servidor. Tudo que é ferramenta em volta vem de
biblioteca consolidada, porque reimplementar ferramenta rouba espaço da
pergunta:

| Ferramenta                 | Por que ela e não código próprio                         |
| --------------------------- | ---------------------------------------------------------- |
| `tsenart/vegeta`           | gera a carga, agrega percentis e trata o ritmo do ataque |
| `prometheus/common/expfmt` | lê o formato de exposição do `/metrics`                  |
| `prometheus/client_golang` | métricas do servidor e coletores de runtime              |
| `testcontainers-go`        | sobe, espera e derruba o container da falha controlada   |

Sobre o Vegeta, duas escolhas de configuração merecem explicação, porque as
duas mudam o que o número significa.

**`Rate` zero** significa "taxa máxima, limitada pelo número de workers". É o
modelo fechado, e é deliberado: a pergunta do P01 é o que acontece com N
downloads simultâneos. Quando a pergunta for outra, "a que taxa o servidor
satura", basta informar uma taxa e o Vegeta passa a disparar na hora marcada
mesmo que a requisição anterior não tenha respondido. Isso evita o
coordinated omission, o viés de um gerador que, ao ficar lento junto com o
servidor, pede menos e esconde a própria lentidão da medição.

**`MaxBody(0)`** faz o Vegeta drenar o corpo sem guardá-lo. Sem isso, o
cliente alocaria uma cópia de cada objeto de 16 MiB que recebe, e o gerador
viraria o maior alocador da máquina: exatamente o efeito que este lab quer
observar no servidor. Em troca, os bytes entregues passam a vir do contador
do servidor, que é a fonte correta para essa informação de qualquer forma.

---

## Resumo da ópera

Servir um arquivo de duas formas diferentes parece detalhe de implementação.
Não é.

Carregar o arquivo inteiro na memória é o código mais óbvio de escrever, e
funciona perfeitamente enquanto você testa sozinho na sua máquina. O problema
só aparece quando muita gente pede ao mesmo tempo: cada requisição passa a
exigir um pedaço de memória do tamanho do arquivo, o coletor de lixo começa a
trabalhar sem parar, e a CPU que deveria atender clientes vai para a faxina.
Medimos 89,26 GB alocados contra 0,42 GB para entregar o mesmo conteúdo, e o
p99 pagou a conta.

Transmitir em fluxo troca esse custo por outro: mais idas ao kernel, mais
syscalls. É um trade-off real, não uma bala de prata. Neste lab o fluxo
ganhou em todos os tamanhos testados, mas o que fica não é "streamed é
melhor". O que fica é saber **que existe um trade-off**, saber **onde ele
aparece**, e saber **como provar de que lado ele está** na sua situação.

E fica uma lição de método, que talvez valha mais que a técnica: os ciclos de
coleta de lixo apontavam para a conclusão errada. Quem tivesse olhado só
aquele número teria defendido o modo pior, com um gráfico na mão. Medir é
fácil. Medir a coisa certa, e admitir o que a medição **não** prova, é o
trabalho.
