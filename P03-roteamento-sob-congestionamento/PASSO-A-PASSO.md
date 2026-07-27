# P03 passo a passo

Este roteiro leva do zero até ver, com os próprios olhos, um servidor de borda
ficar lento sem cair, o roteador perceber isso e desviar o tráfego, e depois
voltar a usá-lo quando ele melhora.

Nada aqui exige conhecer o código. Cada passo diz o comando, o que esperar e o
que aquilo significa.

Requisitos: Docker com Compose e Go instalado. Tudo roda em `127.0.0.1`.

## Passo 1: subir o ambiente

```bash
cd P03-roteamento-sob-congestionamento
docker compose up -d --build
```

A primeira vez demora alguns minutos, porque compila os binários Go e baixa
Prometheus, Grafana, Loki, Tempo e Toxiproxy.

```bash
docker compose ps
```

Onze serviços de pé:

| Serviço | Papel |
| --- | --- |
| `origin` | a origem, que tem todos os objetos e é o recurso caro |
| `edge-a`, `edge-b`, `edge-c` | os três servidores de borda, cada um com cache próprio |
| `toxiproxy` | fica no caminho entre roteador e edges, e degrada esse caminho sob comando |
| `router` | o roteador, que decide para onde vai cada requisição |
| `prometheus`, `grafana` | métricas e painéis |
| `loki`, `promtail` | os logs dos containers, pesquisáveis |
| `tempo` | os traces |

## Passo 2: a primeira requisição

```bash
curl -sD - -o /dev/null http://127.0.0.1:9080/objects/obj-64KiB-1.bin
```

A resposta traz cabeçalhos que contam a história inteira:

```text
HTTP/1.1 200 OK
Content-Length: 65536
Etag: "45cbfd660d27f2fa"
X-Cache: MISS               <- o edge não tinha o objeto e foi buscar na origem
X-Correlation-Id: 1c84c639  <- identificador desta requisição, do início ao fim
X-Edge: edge-c              <- qual edge respondeu
X-Router-Attempts: 1        <- quantas tentativas foram necessárias
X-Router-Backend: edge-c    <- para quem o roteador mandou
X-Router-Strategy: adaptativa
```

Peça de novo o mesmo objeto:

```bash
curl -s -o /dev/null -D - http://127.0.0.1:9080/objects/obj-64KiB-1.bin | grep -E "X-Cache|X-Edge"
```

Se cair no mesmo edge, o `X-Cache` vira `HIT`. Se cair em outro, continua `MISS`,
porque **cada edge tem cache próprio**. Isso não é um defeito do laboratório: é
como uma CDN funciona, e é o que dá preço a trocar de destino no meio da carga.

## Passo 3: ver a distribuição do tráfego

```bash
for i in $(seq 1 30); do
  curl -s -o /dev/null -D - http://127.0.0.1:9080/objects/obj-1KiB-$i.bin | grep -i "^x-edge:"
done | sort | uniq -c
```

Com os três edges saudáveis, o resultado fica perto de dez para cada. A política
não tem motivo para preferir ninguém.

## Passo 4: deixar um edge lento (e só um)

Aqui está o coração do projeto. O comando abaixo adiciona 300ms de latência ao
CAMINHO entre o roteador e o edge-a. O processo do edge-a continua perfeitamente
saudável: quem ficou ruim é a rede até ele.

```bash
curl -s -X POST http://127.0.0.1:8474/proxies/edge-a/toxics \
  -d '{"name":"latencia","type":"latency","stream":"downstream","toxicity":1,
       "attributes":{"latency":300,"jitter":50}}'
```

Confirme que o edge-a continua se declarando saudável:

```bash
docker compose exec edge-a wget -qO- http://127.0.0.1:8080/healthz
# ok
```

É este o ponto: **o health check diz que está tudo bem**. Um balanceador comum
continuaria mandando um terço do tráfego para lá.

## Passo 5: ver a política reagir

Rode o mesmo laço do passo 3:

```bash
for i in $(seq 1 30); do
  curl -s -o /dev/null -D - http://127.0.0.1:9080/objects/obj-1KiB-$i.bin | grep -i "^x-edge:"
done | sort | uniq -c
```

Depois de algumas requisições, o edge-a some da lista: o tráfego passa a se
dividir entre edge-b e edge-c. Nenhuma configuração foi mudada, ninguém marcou o
edge-a como fora do ar. Ele simplesmente ficou caro.

Para ver a conta por trás disso:

```bash
curl -s http://127.0.0.1:9090/admin/state | python3 -m json.tool | head -40
```

Repare em três campos de cada destino:

- `latency_ewma_ms`: a latência média que o roteador aprendeu observando as
  respostas reais;
- `cost`: o custo estimado da próxima requisição, que é o que decide;
- `circuit_open`: se o disjuntor está aberto. Um disjuntor de software para de
  mandar tráfego para um destino que está falhando, para dar tempo dele se
  recuperar. Aqui ele não abre, porque o edge-a não está falhando, só demorando.

O `cost` do edge-a estará ordens de grandeza acima dos outros dois.

## Passo 6: comparar com o rodízio simples

Troque a política em tempo de execução e repita:

```bash
curl -s -X PUT 'http://127.0.0.1:9090/admin/strategy?name=round-robin'

for i in $(seq 1 30); do
  curl -s -o /dev/null -w "%{time_total}s " http://127.0.0.1:9080/objects/obj-1KiB-$i.bin
done; echo
```

Um terço das requisições demora perto de 300ms. Volte para a política adaptativa
e repita: as demoradas somem.

```bash
curl -s -X PUT 'http://127.0.0.1:9090/admin/strategy?name=adaptativa'
```

## Passo 7: ver o edge voltar

Desligue a degradação:

```bash
curl -s -X POST http://127.0.0.1:8474/proxies/edge-a -d '{"enabled":false}'
curl -s -X POST http://127.0.0.1:8474/proxies/edge-a/toxics/latencia -d '{"toxicity":0}'
curl -s -X POST http://127.0.0.1:8474/proxies/edge-a -d '{"enabled":true}'
```

Mantenha tráfego passando por alguns segundos e olhe a distribuição de novo:

```bash
for i in $(seq 1 60); do
  curl -s -o /dev/null http://127.0.0.1:9080/objects/obj-1KiB-$((i % 10)).bin
done
for i in $(seq 1 30); do
  curl -s -o /dev/null -D - http://127.0.0.1:9080/objects/obj-1KiB-$i.bin | grep -i "^x-edge:"
done | sort | uniq -c
```

O edge-a reaparece. O mecanismo por trás disso é o envelhecimento da informação:
quanto mais tempo um destino passa sem receber nada, menos o roteador confia no
que sabia sobre ele, até que valha a pena perguntar de novo. Sem isso, o edge-a
ficaria exilado para sempre, e isso não é hipótese: aconteceu, e a execução está
guardada em `evidence/falha-controlada/20260725T235400Z`.

## Passo 8: o backpressure

Até agora a carga foi manual e pequena. Agora peça mais do que o sistema aguenta:

```bash
go run ./cmd/matrix -only=stress -rate=1200 -duration=10s -warmup=3s -workers=200
```

Na saída, três números contam a história:

```text
oferecida 11788/s, concluída 2805/s, erro 76.20%
recusas map[concorrencia:91446]
```

O sistema não travou nem inchou: ele **recusou**. Cada recusa é um 503 com
`Retry-After`, respondido em milissegundos. Quem entrou continuou sendo atendido
dentro do prazo; quem não coube soube na hora.

Veja um 503 de perto, com a carga rodando em outro terminal:

```bash
curl -sD - -o /dev/null http://127.0.0.1:9080/objects/obj-1KiB-1.bin | head -5
```

## Passo 9: a bateria completa

```bash
go run ./cmd/matrix -rate=1200 -duration=20s -warmup=5s -workers=200
```

Nove cenários, cerca de quatro minutos: linha de base, carga sustentada, rajada,
estresse, banda estreita, latência, conexão cortada, queda e recuperação. No fim,
o caminho de um diretório novo em `evidence/matriz-de-carga/`.

Abra o `summary.md` de lá. Ele tem três tabelas: o que o cliente observou, o que
o roteador relatou e o que os edges e a origem relataram. As três juntas são o
que transforma um número em evidência.

## Passo 10: a falha controlada, com as duas políticas

```bash
go run ./cmd/failover -rate=1200 -phase=20s -warmup=3s -workers=200
```

Cerca de cinco minutos. O mesmo roteiro de degradação roda duas vezes, uma com
cada política, no mesmo processo e nos mesmos containers. É a comparação
principal do projeto.

O resultado guardado em `evidence/falha-controlada/` mostra, na fase mais severa,
o rodízio entregando 731 requisições por segundo com 37% de erro e p99 de 806ms,
contra 1200 por segundo sem erro e p99 de 6,8ms da política adaptativa.

## Passo 11: os painéis

Abra <http://127.0.0.1:3000>. O painel "P03: roteamento sob congestionamento" já
vem provisionado, sem senha.

A leitura recomendada, de cima para baixo:

1. **o que o cliente sente**: requisições por segundo por código e a
   [latência](https://pt.wikipedia.org/wiki/Lat%C3%AAncia) nos
   [percentis](https://pt.wikipedia.org/wiki/Percentil) p50, p95 e p99, ou seja,
   o valor abaixo do qual ficam 50%, 95% e 99% das requisições;
2. **a decisão do roteador**: para onde o tráfego foi, ao lado do custo estimado
   de cada destino. Este par é o coração do painel, porque mostra a decisão e o
   motivo dela juntos;
3. **retry e backpressure**: retries concedidos, negados e requisições recusadas;
4. **edges e origem**: cache hit por edge e pressão na origem;
5. **o log do roteador**, no fim da página.

Deixe uma carga rodando num terminal e assista:

```bash
go run ./cmd/matrix -only=latency -rate=1200 -duration=60s
```

A linha do edge-a no gráfico de tráfego cai; a linha dele no gráfico de custo
sobe. Os dois acontecem com poucos segundos de diferença, e essa diferença é o
tempo que a política levou para aprender.

## Passo 12: seguir uma requisição pelos três sinais

Pegue um identificador de correlação de uma requisição:

```bash
curl -s -o /dev/null -D - http://127.0.0.1:9080/objects/obj-1MiB-3.bin | grep -i correlation
```

Procure no log, no Grafana, escolhendo a fonte Loki:

```logql
{service="router"} |= "SEU_ID_AQUI"
```

E no Tempo, procurando o trace pelo atributo `correlation_id`. O trace mostra o
span do roteador, o span da tentativa e o span do edge, aninhados, com o tempo
gasto em cada etapa. É assim que se responde "por que ESTA requisição demorou",
que é a pergunta que a métrica sozinha não responde.

## Passo 13: os testes

```bash
go test -race ./...                       # unitários, sem docker
go test -bench=. ./internal/router/       # quanto custa decidir
go test -tags=integration ./test/... -v   # sobe o Compose e mede de ponta a ponta
```

O teste de integração é o que prova as propriedades que moram entre os processos:
que o prazo atravessa roteador, edge e origem; que o orçamento de retry contém a
amplificação quando tudo falha; e que as duas políticas se comportam de forma
diferente diante do mesmo edge lento.

Com o ambiente já de pé, `EDGE_SKIP_COMPOSE=1 go test -tags=integration ./test/...`
reaproveita os containers em vez de subir tudo de novo.

## Encerrando

```bash
docker compose down -v
```

O `-v` remove os volumes. Os dados de Prometheus, Loki e Tempo são de uma
execução e não devem sobreviver a ela; a evidência que importa já está em
`evidence/`, versionada.

## Se algo der errado

**A API do Toxiproxy parou de responder.** Aconteceu neste laboratório, e a
causa está documentada em `internal/toxi/toxi.go`: remover uma toxina enquanto
ela segura conexões trava o mutex da API inteira. As ferramentas do projeto
evitam esse caminho, mas se você mexeu na mão e travou, `docker compose restart
toxiproxy` resolve.

**"bind: address already in use" no gerador de carga.** É o gerador esgotando as
portas efêmeras da máquina, não o serviço falhando. Baixe a taxa ou o número de
workers. A execução guardada em `evidence/matriz-de-carga/20260725T232715Z` é
justamente uma medição contaminada por isso, mantida como registro.

**Os números não batem com os do README.** Não vão bater mesmo: dependem da
máquina, e o gerador divide CPU com os containers medidos. O que deve se repetir
é a forma do resultado, não o valor.
