# P01: hot path HTTP

> **A pergunta deste lab:** por que carregar um arquivo inteiro na memória e
> enviá-lo em fluxo produzem comportamentos tão diferentes quando muita gente
> pede ao mesmo tempo?

O "hot path" é o caminho que o código percorre com mais frequência, aquele por
onde passa a maior parte das requisições. É onde otimizar vale a pena e onde
desperdício dói. Neste projeto o hot path é a entrega de um arquivo por HTTP.

Este projeto sobe um servidor que entrega arquivos de duas maneiras diferentes, e
mede as duas lado a lado. É um laboratório de estudo: o objetivo não é ter o
servidor mais rápido do mundo, é **entender o porquê** de uma estratégia ganhar da
outra, e aprender a provar isso com número em vez de opinião.

Se você programa mas nunca mexeu com desempenho de servidor, este README foi
escrito para você. Cada conceito aparece no momento em que faz falta.

---

## As duas estratégias

Imagine que você trabalha num restaurante e precisa levar um bolo de 16 kg da
cozinha até a mesa.

**Estratégia 1, `buffered` (tudo na bandeja):** você pega o bolo inteiro, coloca
numa bandeja e leva de uma vez. Simples. Mas se chegarem 128 pedidos ao mesmo
tempo, você precisa de 128 bandejas gigantes, e a cozinha não tem espaço.

**Estratégia 2, `streamed` (fatia por fatia):** você leva o bolo em fatias, com um
pratinho pequeno, indo e voltando. Cada viagem carrega pouco, então o pratinho é o
mesmo independente do tamanho do bolo. Você faz mais viagens, mas nunca precisa de
espaço para o bolo inteiro.

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

Repare que **a única diferença é o que passamos no último argumento**. Todo o
resto (cabeçalhos, códigos de status, suporte a download parcial) é o mesmo
`http.ServeContent` nos dois casos. Isso é proposital, e é o que torna a
comparação honesta. Se os dois caminhos tivessem implementações diferentes,
qualquer diferença de velocidade poderia vir do código, não da estratégia.

O código está em [internal/origin/server.go](internal/origin/server.go).

---

## Os conceitos, na ordem em que importam

### Memória, alocação e o coletor de lixo

Quando o programa precisa de espaço para guardar dados, ele **aloca** memória.
Em Go você não devolve essa memória à mão: um mecanismo chamado **coletor de lixo**
(garbage collector, ou GC) passa de tempos em tempos, descobre o que ninguém está
mais usando, e libera.

Esse trabalho não é de graça. Ele gasta CPU, e é CPU que poderia estar atendendo
requisições. Quanto mais lixo você produz, mais o coletor trabalha.

No modo `buffered`, cada requisição de um arquivo de 16 MiB aloca 16 MiB. Com 64
clientes simultâneos, são **mais de 1 GB de memória viva ao mesmo tempo**, e tudo
isso vira lixo assim que a resposta termina. No modo `streamed`, cada requisição
usa um bufferzinho de 32 KiB, reaproveitado. O consumo praticamente não muda com o
tamanho do arquivo.

### Latência, vazão e saturação

Três palavras que parecem sinônimos e não são:

- **Latência** é quanto tempo UMA requisição demora, medida em milissegundos.
- **Vazão** (throughput) é quantas requisições você atende POR SEGUNDO.
- **Saturação** é o quanto o sistema já está no limite.

Dá para ter vazão alta e latência péssima ao mesmo tempo: basta enfileirar todo
mundo. Por isso medimos os dois, e por isso olhamos **percentis** em vez de média.

### Por que percentil e não média

Se 99 requisições levam 1 ms e uma leva 10 segundos, a média dá 100 ms, um número
que não descreve ninguém. O **p99** ("99º percentil") responde outra pergunta:
_qual o tempo que 99% das requisições conseguiram bater?_ Ele mostra a experiência
do usuário azarado, que é justamente quem reclama.

Nos relatórios deste lab aparecem p50 (a mediana), p95 e p99.

### Carga oferecida e carga concluída

Este é o erro clássico de quem mede desempenho: contar só o que deu certo.

- **Carga oferecida** é tudo que o cliente TENTOU fazer.
- **Carga concluída** é o que realmente terminou com sucesso.

Se um servidor recebe 1000 pedidos e só responde 500, e você mede só os 500, ele
parece ótimo. As requisições lentas viraram erro e sumiram da conta. Por isso o
gerador de carga deste lab conta as duas coisas **separadamente**, e as duas
aparecem no relatório.

### User space, kernel space e syscalls

O sistema operacional divide o mundo em dois territórios. Seu programa roda no
**user space**, sem acesso direto ao hardware. Quem fala com disco e rede é o
**kernel**, no **kernel space**.

Toda vez que seu programa precisa ler um arquivo ou escrever num socket, ele faz
uma **syscall**, que é um pedido formal ao kernel. Essa travessia tem custo: o
processador troca de contexto, salva e restaura estado.

Aqui está o trade-off do lab em uma frase: **`buffered` faz menos syscalls porém
aloca muito; `streamed` aloca pouco porém faz mais syscalls.** Qual ganha depende
do tamanho do objeto e da concorrência, e é exatamente isso que vamos medir.

### Descritores de arquivo

Quando o programa abre um arquivo ou aceita uma conexão de rede, o kernel devolve
um **descritor de arquivo** (file descriptor, ou fd), que é um número identificando
aquele recurso aberto. Todo processo tem um limite de quantos pode ter.

Isso importa porque cada conexão HTTP aberta consome um fd. Um servidor com 10.000
conexões simultâneas precisa de mais de 10.000 fds, ou começa a falhar com
"too many open files". O experimento de falha controlada explora isso.

---

## O que foi medido de verdade

Tudo abaixo foi medido nesta máquina, não copiado de blog. A evidência bruta está
em [evidence/](evidence/).

### A máquina onde isto rodou

| Item                                       | Valor                               |
| ------------------------------------------ | ----------------------------------- |
| CPU                                        | Intel Core i9-13980HX (13ª geração) |
| Processadores lógicos visíveis ao processo | 32                                  |
| Memória disponível ao Linux                | 15 GiB                              |
| Sistema                                    | Ubuntu sobre WSL2                   |
| Kernel                                     | 5.15.167.4-microsoft-standard-WSL2  |
| Arquitetura                                | linux/amd64                         |
| Go                                         | 1.24.2                              |
| Limite de descritores por processo         | 1.048.576                           |

Quatro coisas dessa tabela mudam a leitura dos números, e vale entender cada uma
antes de olhar qualquer resultado.

**É um notebook, não um servidor.** Um processador móvel reduz a frequência
conforme esquenta, então uma rodada longa pode ficar mais lenta que uma curta sem
que nada no código tenha mudado. É um dos motivos para repetir cada cenário em vez
de confiar numa medição só.

**É WSL2, não Linux direto no hardware.** O kernel é uma máquina virtual leve
rodando sobre o Windows, e o sistema de arquivos e a rede passam por camadas que
não existem num servidor de verdade. Os números servem para comparar as duas
estratégias entre si, não para estimar o que a mesma máquina faria sem essa camada.

**Os 32 processadores são divididos entre o servidor e quem gera a carga.** Os dois
rodam aqui dentro e brigam pela mesma CPU. É a interferência discutida mais adiante,
e é por isso que os valores absolutos valem menos que a diferença entre os modos.

**Os 15 GiB são o que o WSL2 entrega ao Linux**, não a memória total do notebook.
Por padrão o WSL2 reserva cerca de metade da RAM do host. Esse é o teto que o modo
`buffered` está consumindo quando aloca centenas de megabytes por rajada.

Repare também no limite de descritores: mais de um milhão. É folgado demais para
esgotar por acidente, e é exatamente por isso que o experimento de falha controlada
precisa de um container com o limite reduzido de propósito para o problema aparecer.

### Alocação de memória (benchmark do Go)

```
BenchmarkServeObject/obj-4MiB.bin/buffered-32    1450501 ns/op   4262961 B/op   110 allocs/op
BenchmarkServeObject/obj-4MiB.bin/streamed-32     776514 ns/op     42897 B/op    95 allocs/op
```

Leia a coluna `B/op` (bytes alocados por operação): **4.262.961 contra 42.897**.
Cem vezes mais memória para entregar exatamente o mesmo arquivo.

E repare em `allocs/op`: 110 contra 95, praticamente igual. **Não é o número de
alocações que difere, é o tamanho delas.** Uma alocação de 4 MiB é muito mais cara
para o coletor de lixo do que quarenta alocações de 32 KiB.

### O experimento principal: 64 clientes pedindo 16 MiB

Servidor reiniciado limpo para cada modo, 8 segundos de medição:

|                          | `buffered` | `streamed` |       diferença |
| ------------------------ | ---------: | ---------: | --------------: |
| Requisições concluídas   |      5.349 |     14.697 |   **2,7× mais** |
| Concluídas por segundo   |      667,4 |    1.836,3 |   **2,7× mais** |
| Latência p50             |   86,41 ms |   28,17 ms | **3,1× melhor** |
| Latência p99             |  222,25 ms |  102,83 ms | **2,2× melhor** |
| Total alocado            |   113,8 GB |    0,70 GB |  **163× menos** |
| Heap em uso              |   849,0 MB |    11,9 MB |   **72× menos** |
| Ciclos de coleta de lixo |        128 |        357 |   _veja abaixo_ |

O `streamed` ganhou em tudo que interessa. Mas olhe a última linha com atenção,
porque ela ensina mais que as outras.

### A linha que parece contradizer o resto

O `streamed` teve **mais que o dobro de ciclos de coleta de lixo** (357 contra
128). Se coleta de lixo é ruim, como ele ganhou?

Porque **contar ciclos de GC é a métrica errada**. O coletor do Go dispara quando o
heap cresce uma certa proporção em relação ao tamanho anterior. Como o heap do
`streamed` é pequeno (12 MB), ele atinge esse gatilho com frequência, mas cada
coleta é trivial, porque há pouquíssima coisa para examinar.

O `buffered` coletou menos vezes, e cada coleta teve que lidar com centenas de
megabytes. O que dói não é a frequência, é o **volume**: 113,8 GB alocados contra
0,70 GB.

Essa é uma lição que vale além deste lab: **um número isolado engana**. Se alguém
tivesse olhado só "ciclos de GC", teria concluído exatamente o contrário da
verdade.

### O efeito da concorrência

Da matriz completa, comparando o mesmo objeto de 16 MiB em duas concorrências:

| Concorrência | `buffered` p99 | `streamed` p99 |
| -----------: | -------------: | -------------: |
|   8 clientes |       18,23 ms |        7,30 ms |
| 128 clientes |      798,68 ms |      193,40 ms |

Com 8 clientes, o `buffered` é ruim. Com 128, ele é **44 vezes pior que ele
mesmo**. A degradação não é proporcional à carga, ela acelera. É assim que
saturação se parece num gráfico: a curva não sobe, ela vira.

---

## Como rodar

Você precisa de Go 1.24 ou mais novo. Docker é opcional.

### 1. Gerar os arquivos de teste

```bash
go run ./cmd/cataloggen -dir testdata/objects
```

Isso cria arquivos de 1 KiB, 64 KiB, 1 MiB e 16 MiB. O conteúdo é
**determinístico**: gerado a partir do nome, sempre os mesmos bytes, em qualquer
máquina. Por isso eles não vão para o Git, já que regerar é mais barato que
versionar.

### 2. Subir o servidor

```bash
go run ./cmd/origin -dir testdata/objects -addr 127.0.0.1:8080 -admin-addr 127.0.0.1:8081
```

Duas portas, de propósito:

- **8080** serve os objetos (é a porta pública);
- **8081** serve as métricas e o `pprof` (é a porta administrativa).

O `pprof` mostra nomes de funções, uso de memória e goroutines do processo, e
coletar um perfil consome CPU. Deixar isso na porta pública seria entregar
informação interna a qualquer um e permitir que qualquer cliente atrapalhasse a
medição. **Em produção, a porta administrativa nunca é exposta à internet.**

### 3. Uma medição rápida

```bash
go run ./cmd/loadgen -object obj-16MiB.bin -mode buffered -concurrency 64 -duration 8s
go run ./cmd/loadgen -object obj-16MiB.bin -mode streamed -concurrency 64 -duration 8s
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

**Por que repetir?** Não é para pegar o melhor resultado. É para ver **dispersão**.
Se as três rodadas do mesmo cenário discordam muito entre si, a conclusão é frágil
e o relatório precisa dizer isso em vez de esconder atrás de uma média.

### 5. Testes e benchmarks

```bash
go test -race ./...                                          # tudo, com detector de corrida
go test ./internal/origin -bench=. -benchmem -run '^$'        # benchmarks
```

O `-race` liga o **detector de corrida de dados**, que avisa quando duas goroutines
mexem na mesma variável ao mesmo tempo sem coordenação. É um bug que não aparece em
teste normal: funciona 999 vezes e quebra na milésima, em produção, sem deixar
rastro. A suíte deste projeto passa com `-race`.

### 6. Container

```bash
docker build -t p01-origin .
docker run --rm \
  --read-only \
  -v "$PWD/testdata/objects:/data/objects:ro" \
  -p 8080:8080 \
  p01-origin
```

O [Dockerfile](Dockerfile) é multi-stage: compila num estágio com o Go completo e
copia só o binário para uma imagem `distroless`, que não tem shell nem gerenciador
de pacotes. O container roda como usuário `nonroot`, com o sistema de arquivos
somente leitura e o volume dos objetos montado `:ro`.

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
[evidence/gc-buffered-vs-streamed/](evidence/gc-buffered-vs-streamed/). Para abrir:

```bash
go tool pprof -http=:9000 evidence/gc-buffered-vs-streamed/*/heap-buffered.pprof
```

Uma regra que vale levar para a vida: **consultar um valor de `sysctl` é diferente
de recomendar mudá-lo.** Só se mexe em parâmetro de kernel dentro de ambiente
descartável, com baseline medido e hipótese escrita antes.

---

## Falha controlada: esgotando os descritores

O servidor não quebra só por estar lento. Ele quebra por acabar um recurso. Para
ver isso acontecer de propósito, num container descartável com limite baixo:

```bash
docker run --rm \
  --ulimit nofile=64:64 \
  -v "$PWD/testdata/objects:/data/objects:ro" \
  -p 8080:8080 \
  p01-origin

# Em outro terminal, mais clientes do que descritores disponíveis:
go run ./cmd/loadgen -object obj-1MiB.bin -concurrency 256 -duration 15s
```

O que observar: os erros aparecem como `accept: too many open files`, a carga
concluída despenca enquanto a oferecida continua alta, e a diferença entre as duas
é exatamente o que o servidor não conseguiu aceitar.

**A lição não é "aumente o ulimit".** É reconhecer o sintoma de exaustão de recurso
e saber distinguir de lentidão. Aumentar o limite sem entender por que está
acabando só adia o problema, e às vezes esconde um vazamento de conexões.

---

## Como interpretar isto sem exagerar a conclusão

Esta parte é a mais importante do README, e a mais fácil de pular.

**Os números de MB/s aqui são fisicamente impossíveis numa rede real.** O relatório
mostra quase 29.400 MB/s, ou seja mais de 29 GB/s. Nenhuma placa de rede comum faz isso. O
que aconteceu: cliente e servidor estão na mesma máquina, falando por `localhost`,
e os arquivos já estavam no cache de página do sistema operacional. Não houve rede
nem disco de verdade no caminho. Esses números medem **o custo de CPU e memória do
código**, e é só para isso que servem.

**O gerador de carga disputa CPU com o servidor medido.** Os dois rodam na mesma
máquina, e medir direito pede o contrário: quem gera a carga fica numa máquina
separada, para não roubar CPU de quem está sendo medido. Um gerador ocupado atrasa
as próprias requisições e registra isso como se fosse lentidão do servidor.
Fizemos assim por simplicidade de laboratório, o que significa que os números
absolutos estão contaminados pela interferência. A **comparação relativa** entre os
modos continua válida, porque os dois sofreram a mesma interferência.

**Medição local não é capacidade de produção.** Esta é a distinção que separa quem
aprendeu de quem decorou. Um resultado local não autoriza a dizer "nosso servidor
aguenta 1.836 requisições por segundo". Ele autoriza a dizer "nesta máquina, nestas
condições, com este objeto, o modo streamed atendeu 2,7 vezes mais requisições que
o buffered". A primeira frase é marketing, a segunda é engenharia.

**O resultado não foi decidido antes da medição.** Era plausível que o `buffered`
ganhasse em objetos pequenos, onde o custo de fazer várias syscalls poderia superar
o custo de uma alocação pequena. Medimos objetos de 4 KiB justamente para dar essa
chance a ele. Não ganhou. Mas a hipótese era legítima, e é assim que se faz: você
escreve a hipótese antes, e deixa o número decidir.

---

## Estrutura do projeto

```
cmd/
  origin/       servidor de origem (o que está sendo estudado)
  cataloggen/   gera os arquivos sintéticos
  loadgen/      roda UM cenário de carga
  matrix/       roda a matriz completa e grava a evidência
internal/
  catalog/      catálogo determinístico de objetos
  metrics/      medidores Prometheus
  origin/       o servidor de verdade, mais testes e benchmarks
  loadtest/     gerador de carga e formato de evidência
evidence/       resultados medidos, um diretório por execução
```

Cada arquivo `.go` tem comentários explicando **por que** as decisões foram
tomadas, não só o que o código faz. Ler o código junto com este README é parte do
estudo.

---

## Resumo da ópera

Servir um arquivo de duas formas diferentes parece detalhe de implementação. Não é.

Carregar o arquivo inteiro na memória é o código mais óbvio de escrever, e funciona
perfeitamente enquanto você testa sozinho na sua máquina. O problema só aparece
quando muita gente pede ao mesmo tempo: cada requisição passa a exigir um pedaço de
memória do tamanho do arquivo, o coletor de lixo começa a trabalhar sem parar, e a
CPU que deveria atender clientes vai para a faxina. Medimos 113,8 GB alocados contra
0,70 GB para entregar o mesmo conteúdo, e o p99 pagou a conta.

Transmitir em fluxo troca esse custo por outro: mais idas ao kernel, mais syscalls.
É um trade-off real, não uma bala de prata. Neste lab o fluxo ganhou em todos os
tamanhos testados, mas o que fica não é "streamed é melhor". O que fica é saber
**que existe um trade-off**, saber **onde ele aparece**, e saber **como provar de
que lado ele está** na sua situação.

E fica uma lição de método, que talvez valha mais que a técnica: os ciclos de
coleta de lixo apontavam para a conclusão errada. Quem tivesse olhado só aquele
número teria defendido o modo pior, com um gráfico na mão. Medir é fácil. Medir a
coisa certa, e admitir o que a medição **não** prova, é o trabalho.
