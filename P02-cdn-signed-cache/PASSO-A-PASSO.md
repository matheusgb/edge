# P02 passo a passo

Este documento executa o projeto do começo ao fim, na ordem em que as coisas
acontecem. Cada passo tem três partes:

- **o comando**, para copiar e colar;
- **o que você vai ver**, para saber se deu certo;
- **o que roda por baixo**, uma descrição do caminho no código.

O [README](README.md) explica os conceitos e mostra os resultados medidos.
Aqui o objetivo é outro: acompanhar uma requisição atravessando a CDN.

Você precisa de Docker com Compose e de Go 1.26 ou mais novo.

---

## Passo 1: gerar os segredos

```bash
cp .env.example .env
```

Agora troque os três valores por segredos de verdade:

```bash
printf 'CDN_ORIGIN_SECRET=%s\n' "$(openssl rand -hex 32)"
printf 'CDN_SIGNING_KEYS=k1:%s,k2:%s\n' "$(openssl rand -hex 32)" "$(openssl rand -hex 32)"
```

**O que você vai ver:** três linhas para colar no `.env`, que está no
`.gitignore` e nunca entra no repositório.

**O que roda por baixo:** nada ainda, mas duas decisões já foram tomadas aqui.

**São duas chaves de assinatura, não uma.** Isso permite rotacionar o
segredo: a chave nova passa a assinar, a antiga continua sendo aceita até o
token mais longo em circulação expirar, e só então sai do conjunto. Sem essa
sobreposição, trocar o segredo derrubaria todos os links já entregues. Quem lê
isso é `signer.ParseKeyset`, em
[internal/signer/signer.go](internal/signer/signer.go).

**O segredo da origem não fica no arquivo do Nginx.** A configuração é um
template com `${CDN_ORIGIN_SECRET}`, preenchido pelo entrypoint da imagem
oficial na hora de subir.

---

## Passo 2: subir o ambiente

```bash
docker compose up -d --build
```

**O que você vai ver:** quatro containers subindo, nesta ordem.

```text
Container p02-cdn-origin-1             Started
Container p02-cdn-tokend-1             Started
Container p02-cdn-cdn-1                Started
Container p02-cdn-logexporter-1        Started
```

**O que roda por baixo:** o [docker-compose.yml](docker-compose.yml) define a
topologia.

| Serviço       | Papel                                            | Publicado         |
| ------------- | ------------------------------------------------ | ----------------- |
| `origin`      | serve os objetos; o recurso caro a proteger      | **não** (só admin) |
| `tokend`      | emite e valida URLs assinadas                    | `127.0.0.1:8082`  |
| `cdn`         | Nginx: proxy, cache e controles                  | `127.0.0.1:8080`  |
| `logexporter` | lê o log da CDN e vira métricas Prometheus       | `127.0.0.1:8093`  |

**A porta pública da origem não é publicada, de propósito.** Ela só existe
dentro da rede do Compose. Se estivesse acessível, um experimento poderia
medi-la direto sem perceber e concluir bobagem sobre o cache.

Confirme que tudo respondeu:

```bash
curl -s localhost:8080/healthz    # a CDN
curl -s localhost:8082/healthz    # o validador
curl -s localhost:8090/admin/counters   # os contadores da origem
```

O último devolve `{"requests":0,"bodies":0,...}`: ninguém tocou a origem
ainda.

---

## Passo 3: pedir um link assinado

```bash
curl -s "localhost:8082/sign?path=/objects/foto-64KiB-a.bin&method=GET&ttl=2m"
```

**O que você vai ver:**

```json
{
  "path": "/objects/foto-64KiB-a.bin",
  "query": "exp=1785014292&kid=k2&sig=7abafe916d5f49fe…",
  "url": "/objects/foto-64KiB-a.bin?exp=1785014292&kid=k2&sig=7abafe916d5f49fe…",
  "kid": "k2",
  "expires_at": "2026-07-25T21:18:12Z"
}
```

**O que roda por baixo:** `handleSign`, em
[internal/tokensrv/server.go](internal/tokensrv/server.go):

1. valida o caminho e o TTL pedidos (o teto é 10 minutos; um prazo curto
   limita o estrago de um link vazado);
2. **normaliza o caminho antes de assinar**, porque a CDN vai entregar o
   caminho normalizado e as duas pontas precisam falar da mesma coisa;
3. chama `keyset.Sign`, que calcula
   `HMAC-SHA256(versão + método + caminho + expiração + id da chave)`.
   [HMAC](https://pt.wikipedia.org/wiki/HMAC) é um código de autenticação que
   usa uma chave secreta para provar que uma mensagem não foi alterada;
4. registra no log o caminho e o identificador da chave, **nunca a
   assinatura**.

(O `curl` mostra `\u0026` no lugar de `&`: é o codificador JSON do Go
escapando o caractere. Quem lê o campo como JSON recebe o `&` normal.)

Guarde a URL numa variável para os próximos passos:

```bash
URL=$(curl -s "localhost:8082/sign?path=/objects/foto-64KiB-a.bin&method=GET&ttl=5m" \
  | python3 -c 'import json,sys;print(json.load(sys.stdin)["url"])')
```

Num sistema real, este endpoint fica atrás de login: quem pede o link já
provou quem é. Aqui ele é aberto, porque o objeto de estudo é o que acontece
**depois** que o link existe.

---

## Passo 4: usar o link (a requisição completa)

```bash
curl -sD- -o /dev/null "http://localhost:8080$URL"
```

**O que você vai ver:**

```text
HTTP/1.1 200 OK
Content-Length: 65536
Etag: "…"
Cache-Control: public, max-age=60
X-Cache-Status: MISS
X-Origin-Generated-At: 2026-07-25T21:14:56Z
```

`MISS` significa que a CDN não tinha o objeto e foi buscar na origem.

**O que roda por baixo:** este é o passo central do projeto. A requisição
atravessa, em ordem:

**1. O Host é conhecido?** A configuração em
[deploy/nginx/templates/cdn.conf.template](deploy/nginx/templates/cdn.conf.template)
tem um `server` padrão que responde `444` (fecha a conexão sem responder)
para qualquer Host que não seja o esperado.

**2. Método e taxa.** `limit_except GET HEAD` recusa escrita; `limit_req`
conta requisições por IP.

**3. A subrequisição de autorização.** O `auth_request /_auth` faz o Nginx
consultar o validador antes de qualquer outra coisa. Ele envia três headers,
capturados com `set` na location original:

- `X-Original-Method`: o método verdadeiro. A subrequisição do
  `auth_request` é sempre um GET, então sem isso o validador veria "GET" para
  qualquer método;
- `X-Original-URI`: a URI completa, com a query onde o token viaja;
- `X-Cdn-Path`: o caminho **já normalizado pelo Nginx**.

**4. A decisão.** `handleAuth`, em
[internal/tokensrv/server.go](internal/tokensrv/server.go), normaliza o
caminho por conta própria e **recusa se o resultado dele não bater com o do
Nginx**. Se as duas pontas discordam sobre o caminho, a requisição fica
autorizada como um caminho e servida como outro: é a forma clássica de burlar
autorização num proxy. Depois disso ele confere o HMAC em tempo constante (uma
comparação que não revela pelo tempo de execução se o valor está quase certo)
e só então a expiração. Nessa ordem, porque até a assinatura fechar, o campo
`exp` é apenas um número que qualquer um escreveu. Responde `204` ou `403`.

**5. A busca no cache.** A chave é
`"protected|$scheme|$request_method|$host|$uri"`. `$uri` é o caminho
normalizado **sem a query string**, e é por isso que o token não entra na
chave: mil usuários com mil tokens diferentes compartilham a mesma entrada.

**6. A ida à origem.** `proxy_pass http://origin_upstream$uri`, e repare que é
uma **variável**. No Nginx, `proxy_pass` com URI literal repassa a query
original junto; com variável, não repassa. É assim que o token não chega à
origem.

**7. A origem.** [internal/originsrv/server.go](internal/originsrv/server.go)
confere o header `X-Cdn-Auth`, gera o objeto a partir do nome (determinístico,
sem arquivo em disco) e responde com `Cache-Control`, `ETag` e a marca de
tempo que aparece no `X-Origin-Generated-At`.

---

## Passo 5: repetir e ver o cache funcionar

```bash
curl -sD- -o /dev/null "http://localhost:8080$URL" | grep -i x-cache
curl -s localhost:8090/admin/counters
```

**O que você vai ver:**

```text
X-Cache-Status: HIT
{"requests":1,"bodies":1,"revalidations":0,"auth_failures":0,"bytes":65536}
```

`HIT`, e a origem continua com **uma** requisição: a segunda resposta saiu do
cache sem incomodá-la.

**O que roda por baixo:** o Nginx encontrou a entrada e respondeu sozinho.
Mas repare no que aconteceu antes disso: o `auth_request` rodou **de novo**.
Ele está na fase de acesso, que vem antes da fase de conteúdo, então uma
resposta que sai do cache passa pela autorização do mesmo jeito.

É essa propriedade que sustenta o projeto inteiro. Sem ela, o primeiro
usuário autorizado abriria o objeto para o mundo.

---

## Passo 6: tentar sem o token

```bash
curl -s -o /dev/null -w "%{http_code}\n" "http://localhost:8080/objects/foto-64KiB-a.bin"
```

**O que você vai ver:** `403`. Mesmo com o objeto quentinho no cache.

**O que roda por baixo:** o validador não achou token nenhum, devolveu 403, e
o Nginx nem chegou a consultar o cache. Do lado do validador, a métrica
`token_auth_total{result="deny",reason="missing_token"}` subiu, e o log
registrou o caminho e o motivo, sem query e sem token.

---

## Passo 7: ver as métricas dos três serviços

```bash
curl -s localhost:8093/metrics | grep cdn_         # a CDN, via log
curl -s localhost:8092/metrics | grep token_auth   # as decisões de autorização
curl -s localhost:8090/metrics | grep origin_      # o que chegou à origem
```

**O que você vai ver, na CDN:**

```text
cdn_log_malformed_lines_total 0
cdn_log_token_leaks_total 0
cdn_responses_total{cache="HIT",code="200"} 1
cdn_responses_total{cache="MISS",code="200"} 1
cdn_responses_total{cache="none",code="403"} 1
```

E, do lado do validador:

```text
token_auth_total{reason="ok",result="allow"} 2
token_auth_total{reason="missing_token",result="deny"} 1
```

**O que roda por baixo:** o Nginx de código aberto não tem endpoint de
estatística de cache. Ele sabe se cada resposta foi HIT, MISS, EXPIRED ou
STALE e escreve isso no log de acesso, então o `logexporter` segue o arquivo
e transforma linha em métrica. Seguir arquivo com rotação e truncamento é
problema resolvido, e fica com a biblioteca `nxadm/tail`; o que é do projeto,
mapear linha em métrica com os rótulos certos, está em
[internal/logexport/](internal/logexport/).

Duas coisas para reparar:

**`cache="none"` não é um status de cache.** É o que o Nginx escreve quando a
requisição nem chegou a consultar o cache, como o 403 do passo anterior.
Separar isso de MISS evita inventar uma categoria fantasma no hit ratio.

**`cdn_log_token_leaks_total` deve ser zero para sempre.** O exportador
vigia o que lê: se alguém mexer no formato do log e a query voltar, esse
contador sobe. Um controle de segurança que ninguém verifica não é um
controle.

---

## Passo 8: o cache stampede

```bash
go run ./cmd/stampede
```

**O que você vai ver:**

```text
com-lock  -> 1 chamadas na origem
sem-lock  -> 100 chamadas na origem

| Variante | Lock | Chamadas na origem | p50 ms | p99 ms |
| com-lock | true |                  1 |  598.7 |  639.6 |
| sem-lock | false|                100 |  321.7 |  398.2 |
```

Um cache stampede acontece quando uma entrada quente expira e muitos clientes
pedem o mesmo objeto ao mesmo tempo: sem controle, todos batem na origem
juntos. `proxy_cache_lock` é o controle do Nginx contra isso: só uma
requisição vai à origem, e as demais esperam a resposta ficar disponível no
cache.

**O que roda por baixo:** [cmd/stampede/main.go](cmd/stampede/main.go):

1. liga uma latência artificial de 300 ms na origem, pela porta
   administrativa. Sem isso, o primeiro cliente termina antes dos outros
   chegarem e não existe stampede para conter;
2. pede um token para um objeto com nome **novo**, garantindo cache frio;
3. solta 100 goroutines que param num canal fechado, a pistola de partida,
   para que a largada seja simultânea (é o `errgroup` fazendo o trabalho);
4. lê os contadores da origem antes e depois;
5. repete tudo no caminho `/nolock/`, que aponta para os mesmos objetos com
   `proxy_cache_lock off`, só para servir de comparação.

**A leitura importante não é a primeira coluna, é a última.** Com o lock, os
clientes ficaram mais lentos: 99 deles esperaram um único cliente ir à origem
e voltar. Você troca latência do cliente por proteção da origem, e essa troca
vale a pena quando a origem satura, não sempre.

---

## Passo 9: hit ratio e o efeito do TTL

```bash
go run ./cmd/loadgen -scenario ttl-longo -objects 200 -popular 10 \
  -rate 500 -duration 20s -warmup 0 -origin-max-age 60s

go run ./cmd/loadgen -scenario ttl-curto -objects 200 -popular 10 \
  -rate 500 -duration 20s -warmup 0 -origin-max-age 3s
```

**O que você vai ver:** duas tabelas quase idênticas no hit ratio e muito
diferentes no resto.

|                       | TTL 60s | TTL 3s |
| --------------------- | ------: | -----: |
| Hit ratio             |     98% |    98% |
| Chamadas à origem     |     200 |    831 |
| das quais 304         |       0 |    631 |
| Bytes da origem       | 12,5 MiB | 12,5 MiB |

**O que roda por baixo:** [cmd/loadgen/main.go](cmd/loadgen/main.go):

1. ajusta o TTL anunciado pela origem, porque o TTL é **variável do
   experimento**, não configuração de ambiente;
2. pede um token por objeto, antes de medir, para que o tempo medido seja o
   da CDN e não o do serviço de token;
3. ataca com o Vegeta a **taxa constante**, disparando na hora marcada mesmo
   que a requisição anterior não tenha respondido. Um gerador ingênuo faria o
   oposto: ao ficar lento junto com o servidor, pediria menos e esconderia a
   lentidão. Esse viés de medição tem nome, coordinated omission;
4. distribui o tráfego de forma desigual, com 10 objetos concentrando 80%
   das requisições, porque tráfego real de conteúdo é assim;
5. lê o hit ratio das métricas da CDN e o alívio dos contadores da origem.

**A lição está na diferença entre as duas últimas linhas.** O TTL curto
custou quatro vezes mais chamadas à origem e exatamente os mesmos bytes,
porque as 631 chamadas extras foram revalidações que a origem respondeu com
304. Se a métrica de capacidade for banda, o TTL curto saiu de graça; se for
requisições por segundo, saiu caro. E o hit ratio, sozinho, não contou nada
disso.

---

## Passo 10: derrubar a origem

```bash
go run ./cmd/outage
```

**O que você vai ver:** dez respostas 200 com `X-Cache-Status: STALE`,
enquanto a origem responde 503 para tudo.

```text
| Respostas servidas do cache vencido | 10/10 |
| Erros propagados ao cliente         |     0 |
| Tentativas por pedido               |  1,00 |
```

**O que roda por baixo:** [cmd/outage/main.go](cmd/outage/main.go) executa
um roteiro fixo:

1. manda a origem anunciar TTL de 3 s, para a entrada vencer rápido;
2. busca o objeto uma vez, populando o cache;
3. devolve o TTL ao normal e liga o modo de falha da origem;
4. espera o vencimento e faz dez requisições;
5. restaura a origem e observa a recuperação.

**"Tentativas por pedido = 1,00" é o número que importa.** Uma CDN mal
configurada responde a uma origem doente tentando de novo, e de novo, e
transforma uma queda numa tempestade que impede a origem de se levantar.
Aqui, `proxy_next_upstream off` garante uma tentativa por pedido.

Uma sutileza que quase virou conclusão errada: depois da origem voltar, o
conteúdo continua "antigo". Não é falha. O objeto é determinístico, o ETag
não mudou, a revalidação devolveu 304 e o Nginx renovou o **frescor** da
cópia guardada. O sinal de recuperação é a entrada voltar a HIT com
revalidação registrada na origem, não o corpo mudar.

---

## Passo 11: os testes negativos

```bash
go run ./cmd/negative
```

**O que você vai ver:** treze linhas, uma por vetor, e um código de saída
diferente de zero se algum falhar.

```text
OK    sem-token                  esperado: 403   obtido: 403
OK    assinatura-alterada        esperado: 403   obtido: 403
OK    token-expirado             esperado: 403   obtido: 403
OK    host-desconhecido          esperado: conexão encerrada ou 4xx   obtido: erro de transporte
OK    query-nao-entra-na-chave   esperado: 200 com HIT   obtido: 200 (cache: HIT)
…
```

**O que roda por baixo:** [cmd/negative/main.go](cmd/negative/main.go) roda
um vetor por função, e **cada vetor declara o que espera antes de rodar**. Um
vetor que "passa" porque a expectativa foi ajustada depois do resultado não
prova coisa alguma.

Três merecem atenção:

- **`token-nao-chega-na-origem`** pede pela CDN um endpoint que devolve os
  headers que a origem recebeu, e confere que a URI chegou lá sem `sig=` nem
  `kid=`. Foi esse vetor que, na primeira execução, pegou um vazamento real: a
  rota de diagnóstico usava `proxy_pass` com caminho literal, e o Nginx
  repassava a query junto. As duas execuções, a vermelha e a verde, estão em
  [evidence/testes-negativos/](evidence/testes-negativos/);
- **`query-nao-entra-na-chave`** adiciona `&cachebuster=1` e espera **HIT**,
  provando que a query não fabrica entradas novas no cache;
- **`replay-dentro-da-validade`** usa o mesmo link duas vezes e espera que
  funcione. Não é defeito: é a natureza de uma URL assinada, e o controle
  contra isso é o prazo curto, não a assinatura.

A ferramenta redige qualquer query antes de escrever na tela ou em arquivo,
inclusive dentro de mensagem de erro do cliente HTTP, que é por onde uma URL
inteira costuma escapar.

---

## Passo 12: os testes

```bash
go test -race ./...                        # unitários, com detector de corrida
go test -tags=integration ./test/... -v    # o caminho completo, com containers
go test ./internal/signer -bench=. -benchmem -run '^$'
```

**O que você vai ver:** a suíte passando, e o benchmark mostrando o custo de
validar um token: cerca de 611 ns por validação.

**O que roda por baixo:** os unitários provam que cada peça faz a sua parte.
O de integração prova o que eles não conseguem, porque as propriedades
interessantes moram **entre** as peças: a chave de cache é do Nginx, a
decisão de autorização é do serviço Go, e o valor está em elas concordarem.

[test/cdn_integration_test.go](test/cdn_integration_test.go) sobe o Compose
pelo testcontainers e verifica, entre outras coisas, que dois tokens
diferentes compartilham a mesma entrada de cache e que um cache quente
**não** dispensa autorização.

---

## Encerrando

```bash
docker compose down --volumes
```

O `--volumes` apaga o cache e o log da CDN. São estado de uma execução, e
deixá-los para trás faria a próxima rodada começar com um cache quente que
ninguém pediu.

A partir daqui, o [README](README.md) tem os resultados medidos, a discussão
de cada conceito e, mais importante, a lista do que estes números **não**
provam.
