# P03: roteamento sob congestionamento

Um roteador HTTP em Go distribui requisições entre três servidores de borda.
Um deles fica lento, mas não cai: continua aceitando conexão, continua
respondendo 200, continua dizendo que está saudável. A pergunta do projeto é
o que o roteador deveria fazer a respeito.

Resposta curta, medida neste laboratório e detalhada mais abaixo: com rodízio
simples, o p99 do cliente foi de 6,9ms para 806ms e 37% das requisições
falharam. Com uma política que aprende com o tráfego, o p99 ficou em 6,8ms e
o erro em zero, na mesma máquina, no mesmo minuto, contra a mesma degradação.

## O problema, em uma cena

Um site de vídeo tem três servidores de borda entregando os mesmos arquivos.
Um deles está numa rede que começou a congestionar. Ele não caiu: o processo
está vivo, a porta está aberta, o health check responde `ok` em
milissegundos, porque a rota do health check não faz nada.

O balanceador continua mandando um terço do tráfego para lá. Um terço dos
usuários espera 800ms em vez de 6ms. Alguns desistem. Os que não desistem
tentam de novo, o que gera mais carga. A equipe olha o painel e vê "os três
servidores saudáveis", porque é isso que o health check diz.

O ponto do projeto é este: saudável no protocolo não é o mesmo que saudável
na prática. A diferença entre as duas coisas é onde mora quase todo incidente
de latência sem causa óbvia.

## Como funciona

```text
gerador de carga (Vegeta, modelo aberto)
              |
              v
        roteador em Go
   ( escolhe, limita, dá prazo, decide retry )
              |
     +--------+--------+
     |        |        |
  Toxiproxy Toxiproxy Toxiproxy      <- a degradação acontece AQUI
     |        |        |
  edge-a   edge-b   edge-c           <- cache próprio, independente
     \        |        /
          origem Go                  <- o recurso caro que os caches protegem
```

O Toxiproxy fica entre o roteador e cada edge. É ele quem atrasa bytes,
limita banda ou corta conexões, sempre em UM caminho, sem tocar nos outros
dois. O edge continua saudável do lado de lá: quem está doente é o caminho
até ele. Essa separação é o que torna o experimento fiel ao problema, em vez
de virar "desliguei um container e vi o que acontece".

Cada edge tem cache próprio e pequeno, como numa CDN real. É isso que dá
preço à decisão do roteador: mandar a requisição para outro edge pode
significar buscar o objeto na origem de novo.

## Os conceitos, na ordem em que importam

### Health check não vê o que interessa

Um health check responde uma pergunta binária: você está de pé? O caso que
derruba o p99 não é esse. É o destino que está de pé, aceita conexão,
responde certo, e demora.

Existem duas formas de descobrir isso. A **ativa** é perguntar de fora: um
agente faz requisições sintéticas e mede. Funciona, mas tem dois problemas: o
agente vê o caminho dele até o destino, que não é necessariamente o caminho
do tráfego real, e ele só descobre o problema no intervalo entre sondagens.

A **passiva**, usada neste projeto, aprende com o tráfego que já está
passando. Toda requisição que volta é uma amostra: quanto demorou, deu
certo? Não custa tráfego extra, mede exatamente o caminho que importa, e o
atraso da descoberta é o tempo de algumas respostas.

O preço da passiva está na seção de limites, mais abaixo: o estado é local
ao processo do roteador.

### O custo de um destino

A política adaptativa não escolhe pelo "mais rápido". Ela calcula um custo
estimado para a próxima requisição:

```text
custo = (fila + 1) x latência média x (1 + taxa de erro x penalidade)
```

Os três fatores existem porque nenhum funciona sozinho:

- **latência sozinha** reage tarde: a média só sobe depois que várias
  respostas lentas já voltaram, e o tráfego continua indo enquanto isso;
- **fila sozinha** não distingue um destino com dez requisições rápidas de
  um com dez requisições travadas;
- **erro sozinho** ignora o destino que responde certo, só que devagar, que
  é o caso central deste projeto.

Multiplicar em vez de somar evita ter que inventar uma unidade comum entre
"três requisições na fila" e "quarenta milissegundos".

### Sorteio de dois, e não o melhor

Escolher sempre o de menor custo parece a decisão óbvia, mas não é: a
informação que o roteador tem é de alguns milissegundos atrás. Se todo mundo
decidir ao mesmo tempo com a mesma informação, a rajada inteira vai para o
mesmo destino, que fica lento, e a próxima rajada vai para o concorrente que
acabou de ficar rápido. O sistema oscila sozinho, sem nenhuma falha real.

A política sorteia dois destinos e escolhe o melhor entre eles: é a técnica
conhecida como "power of two choices". Perde-se um pouco de qualidade na
escolha individual e ganha-se a quebra da sincronia. Com três destinos o
efeito é modesto; com trinta, é a diferença entre funcionar e não funcionar.

### O envelhecimento, ou por que o exílio não pode ser permanente

Esta parte não estava na primeira versão do código. A evidência mostrou que
faltava.

A conta acima tem um ponto fixo desagradável. Um destino fica caro. Por
ficar caro, para de receber tráfego. Sem tráfego, a média móvel dele nunca
mais é atualizada. Sem atualização, ele continua caro para sempre. O edge
conserta, volta a responder em 1ms, e o roteador nunca descobre.

Foi exatamente o que aconteceu em
[`evidence/falha-controlada/20260725T235400Z`](evidence/falha-controlada/20260725T235400Z/summary.md):
na fase de recuperação, com o edge-a já saudável, ele seguiu com 0% do
tráfego até o fim do experimento.

A correção é dizer a verdade sobre a informação: uma medição de dez segundos
atrás vale menos que uma de agora. O custo é dividido por um fator que
**dobra** a cada janela de silêncio. Um destino que parecia oitocentas vezes
pior volta a ser sondado depois de cerca de dez janelas, e o tempo até a
sondagem cresce com o logaritmo de quão ruim ele parecia. Quanto pior a
última notícia, mais tempo até insistir; nunca "para sempre".

Faltava uma segunda peça. Quando a sondagem finalmente acontece e volta em
1ms, misturar essa amostra a 20% numa média de 800ms quase não muda nada, e a
próxima sondagem depende de outro silêncio longo. Por isso, depois de um
silêncio longo, a amostra nova **substitui** a média em vez de ser diluída
nela: a lembrança antiga é de um destino que pode ter sido reiniciado,
corrigido ou trocado desde então.

Faltava um terceiro ajuste, que só apareceu quando os dois primeiros já
estavam no lugar. O limiar de "informação velha" começou em oito janelas de
silêncio, e a sondagem de um destino exilado acontecia a cada seis ou sete
segundos, logo abaixo desse limiar: cada resposta boa era diluída a vinte por
cento numa média de 800ms, e o destino recuperado precisaria de mais de um
minuto para voltar. Com o limiar em duas janelas, a separação fica correta
por construção: um destino em rotação normal responde a cada poucos
milissegundos e nunca cruza o limiar, e um destino que voltou do exílio
passou, necessariamente, por muito mais silêncio que isso.

Com as três peças, o edge-a voltou a receber tráfego cerca de dois segundos
depois de ficar saudável, e terminou a fase de recuperação com 34,1%.

### Prazo que atravessa processos

`context.Context` propaga cancelamento dentro de um processo. Entre
processos, ele não vai sozinho.

Sem isso, um edge que recebe uma requisição já quase vencida vai até o fim:
busca na origem, gasta conexão, CPU e banda para produzir uma resposta que
ninguém vai mais ler. Num sistema saturado, esse trabalho perdido é o que
mantém o sistema saturado.

O roteador manda o prazo restante num header, e o edge cria o próprio
contexto a partir dele. Cada etapa tem prazo próprio, e o prazo do cliente
manda em todos: uma tentativa nunca promete mais tempo do que o cliente
ainda tem.

### Orçamento de retry

Retry sem limite é o mecanismo que transforma degradação em queda. A conta é
direta: se cada requisição pode ser tentada três vezes, um sistema com falha
generalizada recebe o triplo da carga exatamente quando já não dava conta da
carga original.

O orçamento é um balde de fichas. Cada requisição do cliente deposita uma
fração de ficha; cada retry saca uma ficha inteira. Com pouca falha, o balde
enche mais rápido do que esvazia e todo retry passa. Com falha generalizada,
o balde seca em segundos e o roteador para de insistir.

A propriedade importante é que o teto é **proporcional ao tráfego**. Não
existe número mágico de "retries por segundo" para acertar, e o mesmo
serviço com dez vezes mais carga não precisa de nova configuração.

Na fase mais severa do experimento, com round-robin, o roteador pediu 4077
retries e o orçamento negou 2176 deles. Sem essa negativa, seriam 2176
requisições a mais em cima de um sistema que já estava perdendo 37% do
tráfego.

### Backpressure: recusar é uma resposta

O que um serviço faz quando chega mais trabalho do que ele termina? Sem
teto, a resposta padrão do Go é aceitar tudo: cada requisição vira uma
goroutine, cada goroutine segura um socket e um buffer, a memória cresce e a
latência de todo mundo piora junto. O sistema não recusa ninguém e não
atende ninguém.

Com teto, o excesso recebe 503 e `Retry-After` na hora. É uma resposta ruim,
e é muito melhor que a alternativa: quem entrou continua sendo atendido no
prazo, e quem foi recusado sabe disso em milissegundos em vez de descobrir
depois de um timeout de trinta segundos.

A recusa é imediata, sem fila de espera. Fila aqui só mudaria o acúmulo de
lugar, com as requisições vencendo o prazo do cliente enquanto esperam.
Trabalho que ninguém vai mais esperar é trabalho jogado fora.

### Coordinated omission, ou como medir errado sem perceber

No modelo fechado de teste de carga, N clientes ficam em laço: cada um
espera a resposta para pedir de novo. Parece razoável, mas é uma armadilha
em experimento de congestionamento. Quando o servidor fica lento, ele recebe
menos requisições, e a latência média melhora sozinha: o atraso do sistema
medido reduz a frequência das amostras e esconde justamente a cauda que se
queria medir. Esse efeito é conhecido como "coordinated omission".

Este projeto usa o modelo aberto: a taxa é informada, e o gerador dispara na
hora marcada mesmo que a resposta anterior não tenha voltado. É por isso que
as tabelas trazem **carga oferecida** e **carga concluída** em colunas
separadas. Quando as duas divergem, a diferença é o resultado.

## O que foi medido de verdade

### A máquina onde isto rodou

| Item | Valor |
| --- | --- |
| Máquina | notebook pessoal, Windows com WSL2 |
| Kernel | Linux 5.15.167.4-microsoft-standard-WSL2 |
| CPUs visíveis | 32 |
| Memória | 15 GiB |
| Docker | 28.1.1 |
| Go | 1.26.5 (containers e ferramentas de experimento) |
| Limite por container | 1 CPU, 256 MB |

O gerador de carga roda na mesma máquina que os containers medidos. Parte da
cauda é disputa por CPU entre quem mede e quem é medido. Os limites por
container existem para que "saturação" seja uma propriedade do experimento,
e não do notebook.

### A falha controlada: as duas políticas, o mesmo roteiro

Evidência completa em
[`evidence/falha-controlada/20260726T010238Z`](evidence/falha-controlada/20260726T010238Z/summary.md).

O edge-a recebe latência crescente e depois passa a ter 30% das conexões
cortadas. Edge-b e edge-c ficam saudáveis o tempo todo. Taxa oferecida de
1200 req/s, janelas de 20s, mesma semente de catálogo, mesmo processo,
mesmos containers.

| Fase | Política | Concluída/s | Erro | p99 |
| --- | --- | ---: | ---: | ---: |
| sem-falha | round-robin | 1200 | 0,00% | 6,9ms |
| sem-falha | adaptativa | 1200 | 0,00% | 6,7ms |
| edge-a com 50ms | round-robin | 1196 | 0,00% | 65,1ms |
| edge-a com 50ms | adaptativa | 1200 | 0,00% | 6,7ms |
| edge-a com 200ms | round-robin | 1187 | 0,00% | 231,4ms |
| edge-a com 200ms | adaptativa | 1187 | 0,00% | 6,8ms |
| edge-a com 800ms | round-robin | **731** | **36,62%** | **805,9ms** |
| edge-a com 800ms | adaptativa | **1200** | **0,01%** | **6,8ms** |
| conexão cortada | round-robin | 724 | 37,28% | 805,3ms |
| conexão cortada | adaptativa | 1200 | 0,00% | 6,8ms |
| recuperação | round-robin | 1200 | 0,00% | 6,8ms |
| recuperação | adaptativa | 1200 | 0,00% | 6,8ms |

Repare na fase de 50ms. O edge-a está apenas um pouco lento, ninguém recebe
erro, e mesmo assim o p99 do rodízio já é dez vezes maior. Um terço do
tráfego indo para um destino 50ms mais lento é suficiente para dominar o
percentil 99, porque o p99 é justamente sobre a minoria pior.

Na fase de 800ms, a latência do edge-a passou do prazo de uma tentativa
(800ms). Cada requisição que caiu nele virou timeout, retry e, quando o
orçamento negava, erro para o cliente. O edge-a nunca caiu. Ele só ficou
lento.

### Para onde o tráfego foi

| Fase | Política | Distribuição | Falhas no edge-a | Retry negado | Detecção |
| --- | --- | --- | ---: | ---: | ---: |
| edge-a com 50ms | round-robin | 33,3% / 33,3% / 33,3% | 0 | 0 | não detectou |
| edge-a com 50ms | adaptativa | 0,0% / 49,9% / 50,1% | 0 | 0 | 0,7s |
| edge-a com 800ms | round-robin | 30,5% / 34,7% / 34,8% | 3253 | 1351 | ver ressalva |
| edge-a com 800ms | adaptativa | 0,0% / 50,3% / 49,6% | 0 | 0 | 0,7s |
| conexão cortada | round-robin | 30,5% / 35,0% / 34,6% | 4077 | 2176 | ver ressalva |
| conexão cortada | adaptativa | 0,0% / 50,4% / 49,6% | 1 | 0 | 0,8s |
| recuperação | adaptativa | 34,1% / 34,0% / 31,9% | 0 | 0 | 0,7s |

A política adaptativa levou 0,7s para tirar o edge-a de circulação, e o fez
já na fase de 50ms, quando ainda não havia erro nenhum. Na recuperação,
devolveu o tráfego a ele em cerca de dois segundos: a série temporal
guardada no `metrics.json` mostra o edge-a saindo de 0% para 43% entre o
segundo 2 e o segundo 3 da fase.

**Ressalva sobre a coluna de detecção com round-robin.** Nas fases severas
ela mostra um número em torno de 4,2s, e ele não representa decisão nenhuma.
O cálculo olha a fatia de tentativas do edge-a em janelas de 250ms, e as
tentativas que vão para um destino de 800ms simplesmente demoram mais a
aparecer como tentativa concluída, o que derruba a fatia momentaneamente. O
número honesto para o round-robin é a distribuição da janela inteira: 30,7%,
ou seja, quase o terço de sempre.

### O que aconteceu com quem estava saudável

Deslocar toda a carga para os edges saudáveis e derrubá-los seria falha, não
vitória. Por isso o relatório mede também os que estavam bem:

| Fase | Política | HIT nos edges | Goroutines no roteador |
| --- | --- | ---: | ---: |
| edge-a com 800ms | round-robin | 17840 | 1130 |
| edge-a com 800ms | adaptativa | 20467 | 103 |
| conexão cortada | round-robin | 17818 | 1023 |
| conexão cortada | adaptativa | 20476 | 101 |

Os edges saudáveis absorveram 50% do tráfego cada um sem degradar: mesmo hit
ratio, mesma latência. E o roteador ficou com dez vezes menos goroutines,
porque não tinha centenas de requisições paradas esperando um destino lento.

### A matriz de carga

Evidência completa em
[`evidence/matriz-de-carga/20260726T010254Z`](evidence/matriz-de-carga/20260726T010254Z/summary.md).
Nove cenários, todos com a política adaptativa, taxa base 1200 req/s.

| Cenário | Oferecida/s | Concluída/s | Erro | p99 | O que ele mostrou |
| --- | ---: | ---: | ---: | ---: | --- |
| baseline | 300 | 300 | 0,00% | 6,5ms | a linha de base |
| load | 1200 | 1200 | 0,00% | 6,8ms | folga confortável |
| spike | 7073 | 4698 | 33,58% | 178,9ms | 12090 recusas: backpressure |
| stress | 11788 | 2805 | 76,20% | 271,6ms | 91446 recusas, 5934 goroutines |
| bandwidth | 600 | 600 | 0,00% | 7,0ms | edge-a com 0,1% do tráfego |
| latency | 1200 | 1200 | 0,00% | 6,8ms | edge-a com 0,0% do tráfego |
| conexão cortada | 1200 | 1200 | 0,00% | 6,9ms | 29% no edge-a, sem erro no cliente |
| outage | 1200 | 1072 | 10,67% | 7,7ms | edge fora, depois a origem |
| recovery | 1200 | 1100 | 8,29% | 6,9ms | 1791 retries negados na volta |

Três leituras valem o comentário.

**O teto do roteador é o teto do roteador.** No `stress`, o sistema recusou
91446 requisições e concluiu 2805/s com p99 de 272ms. O recurso limitante
foi o teto de concorrência configurado (256 simultâneas), não CPU, memória
ou descritores de arquivo: os edges nem chegaram perto de saturar. Isso é o
backpressure funcionando como projetado, e o número que ele produz é uma
escolha de configuração, não uma descoberta sobre o hardware.

**Banda estreita quase não aparece na latência.** No cenário `bandwidth`, o
p99 ficou em 7ms e o erro em zero. O motivo é que a política tirou o edge-a
de circulação (0,1% do tráfego) antes que a fila de bytes dele afetasse
alguém. O `max` de 519ms é o rastro das poucas sondagens que passaram por
lá, e é a única evidência, na tabela do cliente, de que existia um link
estrangulado.

**Os erros do outage e do recovery são cauda longa, não falha de
roteamento.** Os 10,67% de erro do `outage` são as requisições de objetos
pouco populares, que não estavam em cache em nenhum edge e precisavam da
origem, que estava fora do ar. Objeto popular continuou sendo servido do
cache: é o cache fazendo o trabalho dele durante uma queda da origem.

### Quanto custa decidir

```text
BenchmarkPick/round-robin-32     7.2 ns/op
BenchmarkPick/adaptativa-32     19.9 ns/op
```

A escolha acontece uma vez por tentativa, no caminho quente. 20 nanossegundos
contra 7 é um preço que não aparece em nada: a requisição mais rápida do
laboratório levou 500 microssegundos, vinte e cinco mil vezes mais. Vale a
comparação porque a intuição costuma ser a oposta, de que "a política
inteligente deve custar caro".

## Como rodar

### 1. Subir o ambiente

```bash
docker compose up -d --build
docker compose ps
```

Sobem onze containers: origem, três edges, Toxiproxy, roteador, Prometheus,
Grafana, Loki, promtail e Tempo.

| Endereço | O que é |
| --- | --- |
| http://127.0.0.1:9080 | roteador, o único endereço que o cliente conhece |
| http://127.0.0.1:9090 | administrativa do roteador: métricas, estado, política |
| http://127.0.0.1:9091 a 9093 | administrativas dos edges |
| http://127.0.0.1:9094 | administrativa da origem |
| http://127.0.0.1:8474 | API do Toxiproxy |
| http://127.0.0.1:9095 | Prometheus |
| http://127.0.0.1:3000 | Grafana, com o painel do projeto já provisionado |

### 2. Uma requisição

```bash
curl -sD - -o /dev/null http://127.0.0.1:9080/objects/obj-64KiB-1.bin
```

Os headers contam a história: `X-Router-Backend` diz qual edge respondeu,
`X-Router-Attempts` quantas tentativas foram necessárias, `X-Cache` se veio
do cache do edge, e `X-Correlation-Id` liga esta requisição ao log e ao
trace dela.

### 3. Os experimentos

```bash
# a matriz de nove cenários
go run ./cmd/matrix -rate=1200 -duration=20s -warmup=5s -workers=200

# um cenário só, para iterar rápido
go run ./cmd/matrix -only=latency -rate=1200 -duration=10s

# a falha controlada: as duas políticas, o mesmo roteiro
go run ./cmd/failover -rate=1200 -phase=20s -warmup=3s -workers=200
```

Cada execução grava um pacote em `evidence/<cenário>/<timestamp>/`.

### 4. Degradar um caminho à mão

```bash
# 300ms de latência no caminho até o edge-a
curl -s -X POST http://127.0.0.1:8474/proxies/edge-a/toxics \
  -d '{"name":"latencia","type":"latency","stream":"downstream","toxicity":1,
       "attributes":{"latency":300,"jitter":50}}'

# ver a política reagindo
watch -n1 'curl -s http://127.0.0.1:9090/admin/state | python3 -m json.tool | head -40'
```

Para limpar, prefira o comando do laboratório a remover a toxina na mão:

```bash
go run ./cmd/matrix -only=baseline -duration=5s   # limpa a rede antes de medir
```

### 5. Trocar a política em tempo de execução

```bash
curl -s -X PUT 'http://127.0.0.1:9090/admin/strategy?name=round-robin'
curl -s -X PUT 'http://127.0.0.1:9090/admin/strategy?name=adaptativa'
curl -s -X POST http://127.0.0.1:9090/admin/reset   # apaga o que ele aprendeu
```

### 6. Testes

```bash
go test -race ./...                       # unitários
go test -bench=. ./internal/router/       # o custo de decidir
go test -tags=integration ./test/... -v   # sobe o Compose inteiro e mede
```

O teste de integração sobe e derruba o ambiente sozinho. Com o ambiente já
de pé, `EDGE_SKIP_COMPOSE=1` reaproveita o que está rodando.

## Onde isto aparece em produção

| Aqui | Lá fora |
| --- | --- |
| custo por fila, latência e erro | least-request e EWMA do Envoy, `outlier detection` |
| sorteio de dois candidatos | P2C do Envoy, do Finagle e do gRPC |
| orçamento de retry | `retry budget` do Envoy e do gRPC |
| prazo propagado por header | deadline propagation do gRPC, `x-envoy-expected-rq-timeout-ms` |
| teto de concorrência com 503 | `max_concurrent_requests`, admission control, load shedding |
| disjuntor com sondagem | circuit breaker meio-aberto |
| identificador de correlação | `x-request-id`, trace context do W3C |

O que este projeto faz de diferente é implementar cada peça em algumas
dezenas de linhas legíveis, com a conta à vista, em vez de configurar um
YAML de service mesh onde as mesmas ideias já vêm prontas e invisíveis.

## Observabilidade

O Grafana sobe com um painel provisionado que coloca a decisão ao lado do
efeito dela: tráfego por destino em cima do custo estimado de cada destino,
latência do cliente ao lado da duração das tentativas, retries concedidos ao
lado dos negados.

Os alertas em `deploy/prometheus/alerts.yml` seguem uma regra: alerta liga
em **sintoma percebido pelo cliente**, não em causa suspeita. "edge-a está
lento" não é alerta, é um gráfico. "O cliente está recebendo erro" e "o
cliente está esperando demais" são alertas. As causas prováveis existem com
severidade menor, para encurtar a investigação depois que um sintoma acende.

Os três sinais se encontram no identificador de correlação: ele vai no
header da resposta, no log JSON de cada serviço e como atributo do trace. Do
painel de latência, dá para pular para o log de avisos, copiar um
identificador e achar o trace daquela requisição, que mostra em qual etapa o
tempo foi gasto.

## Os limites do que foi comprovado

**O estado de saúde é local ao processo.** Com várias réplicas de roteador,
cada uma aprende sozinha, e um edge doente é descoberto N vezes em vez de
uma. Em escala isso vira um problema: cada réplica gasta o próprio tráfego
para aprender, e réplicas com pouca carga aprendem devagar. As soluções
reais envolvem um plano de controle que compartilha o estado, e este
laboratório não tem nenhum.

**Três destinos são poucos.** O sorteio de dois candidatos entre três é
quase a escolha global. As propriedades interessantes do P2C aparecem com
dezenas de destinos.

**"Perda de pacotes" aqui é conexão cortada.** O Toxiproxy trabalha em cima
da conexão TCP, não do pacote IP. Ele não descarta pacote e não mexe em
janela de congestionamento; ele corta a conexão com uma certa probabilidade.
O efeito observado é parecido, timeout e retry, mas o mecanismo é diferente.
Por isso o cenário se chama `conexao-cortada` e não `packet loss`.

**Congestionamento de rede real não foi reproduzido.** Não há disputa por
buffer de switch, retransmissão de TCP, bufferbloat nem AQM. O que existe é
atraso e corte injetados por software.

**A capacidade medida é de um notebook.** 1200 req/s com folga e recusa a
partir de ~7000 req/s são números deste ambiente, com gerador e serviços na
mesma máquina e containers limitados a 1 CPU. Não são capacidade de
produção.

**O teto de concorrência foi escolhido, não descoberto.** O `stress` mostra
o sistema recusando a partir do teto configurado. Onde ficaria o teto real
dos edges, com CPU e memória de verdade como limitantes, este experimento
não responde.

**A política adaptativa não foi testada contra si mesma em escala.** Com
muitos roteadores decidindo em paralelo com informação levemente diferente,
existe risco de oscilação coletiva que três destinos e um processo não
revelam.

## Estrutura do projeto

```text
cmd/
  origin/      a origem: serve objetos, e sabe ficar lenta ou fora do ar
  edge/        um edge: cache pequeno, coalescência, respeita o prazo recebido
  router/      o roteador: escolha, prazo, retry com orçamento, backpressure
  matrix/      a matriz de nove cenários de carga
  failover/    a falha controlada, com as duas políticas
internal/
  router/      Backend, Strategy, RetryBudget, Limiter e o handler
  edgesrv/     o edge, com LRU e singleflight
  originsrv/   a origem, com os controles de degradação
  metrics/     os medidores Prometheus dos três serviços
  obs/         traces OTLP
  toxi/        injeção de falha de rede pelo Toxiproxy
  loadtest/    o gerador (Vegeta, modelo aberto) e a coleta das duas metades
  labctl/      os comandos administrativos que todo experimento faz
  evidence/    o pacote de evidência
  promscrape/  leitura de /metrics para calcular deltas de janela
deploy/        Toxiproxy, Prometheus, alertas, Grafana, Loki e Tempo
test/          integração, subindo o Compose inteiro
evidence/      os resultados, um diretório por execução
```

### O que este projeto não escreveu à mão

Gerador de carga (Vegeta), injeção de falha de rede (Toxiproxy), cache LRU
(hashicorp/golang-lru), coalescência de buscas
(golang.org/x/sync/singleflight), métricas (client_golang), traces
(OpenTelemetry) e orquestração do teste de integração (testcontainers).

O que foi escrito à mão é o que este projeto veio investigar: as duas
políticas de roteamento, o orçamento de retry, o limitador com backpressure
e a propagação de prazo entre processos. Um mecanismo só entra manual quando
o comportamento dele é a pergunta.

## Resumo da ópera

Um destino que fica lento sem cair é invisível para health check, e um
terço do tráfego indo para ele é suficiente para dominar o p99 de todo
mundo. Neste laboratório, com 1200 req/s e um edge a 800ms de latência, o
rodízio simples entregou 731 requisições por segundo com 37% de erro e p99
de 806ms; a política que aprende com o tráfego entregou 1200 por segundo,
sem erro, com p99 de 6,8ms.

O que fez a diferença não foi esperteza: foi olhar para fila, latência e
erro juntos, sortear entre dois candidatos em vez de eleger o melhor, e
aceitar que informação velha vale menos que informação nova. Essa última
parte só entrou no código porque a evidência mostrou o edge recuperado sendo
ignorado para sempre, um bom lembrete de que a medição não serve só para
confirmar o que já se achava. Foram três correções seguidas até a
recuperação funcionar, e todas vieram de olhar o número, não o código.

Ao redor da decisão, três mecanismos que valem tanto quanto ela: prazo que
atravessa processos, para ninguém trabalhar por uma resposta que já não tem
destino; orçamento de retry, para insistência não virar amplificação; e
recusa imediata do excesso, porque um serviço que aceita tudo não recusa
ninguém e não atende ninguém.
