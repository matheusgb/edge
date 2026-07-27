# P02: CDN com cache e URL assinada

> **A pergunta deste lab:** como uma CDN pode guardar UMA cópia de um arquivo e
> entregá-la a milhares de pessoas, sem entregá-la para quem não tem direito a
> ela, e sem derrubar a origem no pior momento possível?

Uma [CDN](https://pt.wikipedia.org/wiki/Rede_de_distribui%C3%A7%C3%A3o_de_conte%C3%BAdo)
(rede de entrega de conteúdo) é o conjunto de servidores que fica entre o
usuário e o sistema que produz o conteúdo. Ela guarda cópias do que já foi
pedido e responde de novo sem incomodar quem produziu. É assim que um vídeo
assistido por um milhão de pessoas não vira um milhão de leituras no servidor
original.

Este projeto constrói uma dessas, numa região só, para estudar as duas decisões
que a fazem funcionar.

O problema aparece quando o conteúdo não é público. Se cada usuário tem um
direito diferente, guardar uma cópia compartilhada parece impossível: ou você
guarda uma cópia por usuário, e o cache perde a graça, ou você compartilha e
entrega o arquivo para quem não deveria vê-lo.

Este projeto mostra que a saída é separar duas coisas que costumam andar juntas.
**O conteúdo é compartilhado. O direito de recebê-lo é verificado uma vez por
requisição.** Todo o resto do README é a consequência dessa frase.

Se você programa mas nunca configurou um cache HTTP, este texto foi escrito para
você. Cada conceito aparece no momento em que faz falta.

> **Quer só rodar e ver funcionando?** O [PASSO-A-PASSO.md](PASSO-A-PASSO.md)
> executa o projeto do início ao fim, um comando de cada vez, acompanhando uma
> requisição enquanto ela atravessa a CDN. Este README é o "por quê"; aquele é
> o "como".

---

## O dilema, com números

Imagine uma foto de 64 KiB que mil pessoas autorizadas vão baixar.

**Se a autorização fizer parte da identidade do arquivo no cache**, cada pessoa
tem um "arquivo" diferente aos olhos da CDN: mil cópias guardadas, mil idas à
origem, zero economia. O cache existe e não serve para nada.

**Se não houver autorização nenhuma**, uma cópia guardada, uma ida à origem, e a
foto disponível para qualquer um que descubra o endereço.

O que este lab faz é o meio-termo correto, e ele deu **98% de alívio na origem
com 100% das requisições autorizadas individualmente**. Como, é o que vem a
seguir.

---

## Como funciona

```text
cliente
   |  GET /objects/foto.bin?kid=k2&exp=...&sig=...
   v
+------------------------------------------+
|            Nginx (a CDN)               |
|                                          |
|  1. o Host é conhecido?                  |
|  2. subrequisição -> serviço de token    |  --->  tokend (Go)
|  3. autorizado: procura no cache         |        valida HMAC, prazo,
|  4. achou: responde sem sair daqui       |        método e caminho
|  5. não achou: busca na origem           |
+------------------------------------------+
   |  GET /objects/foto.bin   (sem a query!)
   v
origem (Go), alcançável só pela CDN
```

Uma requisição passa por quatro decisões, nesta ordem:

1. **O Host é um dos meus?** Se não, a conexão é fechada sem resposta.
2. **Este token autoriza este método neste caminho, agora?** Quem responde é um
   serviço Go, consultado pelo Nginx a cada requisição.
3. **Eu já tenho este objeto?** A busca no cache usa uma chave que **não inclui o
   token**.
4. **Se não tenho, quem vai buscar?** Se cem clientes pedirem ao mesmo tempo,
   apenas um vai à origem.

O passo 2 acontecer **sempre**, inclusive quando a resposta vai sair do cache, é
o que sustenta a segurança inteira. O passo 3 ignorar o token é o que sustenta o
desempenho.

---

## Os conceitos, na ordem em que importam

### A chave de cache

Um [cache](https://pt.wikipedia.org/wiki/Cache) funciona como um dicionário:
uma chave aponta para uma resposta guardada. **Quem escolhe a chave decide o
que é "o mesmo conteúdo".** É a decisão mais importante de uma CDN, e a mais
fácil de errar.

A chave deste projeto está em
[deploy/nginx/templates/cdn.conf.template](deploy/nginx/templates/cdn.conf.template):

```nginx
proxy_cache_key "protected|$scheme|$request_method|$host|$uri";
```

Repare no que está lá e, principalmente, no que não está:

- `$uri` é o caminho **já normalizado e sem a query string**. É por isso que o
  token não entra na chave: mil tokens diferentes apontam para a mesma entrada.
- `$host` está na chave porque o mesmo caminho em domínios diferentes é conteúdo
  diferente.
- **`$args` não está.** Se estivesse, cada token criaria uma entrada nova, e
  ainda daria para qualquer um encher o cache de lixo só variando a query.

Aqui mora um perigo real. Deixar o `$host` na chave só é seguro porque existe um
servidor padrão que fecha a conexão para qualquer Host desconhecido. Sem ele, o
cliente escolheria parte da chave, e quem escolhe a chave de um cache
compartilhado pode envenenar uma entrada que outra pessoa vai consumir
(cache poisoning).

### URL assinada: um crachá com prazo

A autorização viaja na própria URL, em três parâmetros:

```text
/objects/foto.bin?kid=k2&exp=1785014292&sig=7abafe916d5f...
                  ^      ^              ^
                  |      |              assinatura HMAC-SHA256
                  |      quando expira
                  qual chave assinou
```

A assinatura usa [HMAC](https://pt.wikipedia.org/wiki/HMAC): um código de
autenticação calculado com uma chave secreta, que prova que a URL não foi
alterada e foi emitida por quem tem o segredo. Ela cobre `versão + método +
caminho + expiração + identificador da chave`, e o código está em
[internal/signer/signer.go](internal/signer/signer.go). Cada campo está lá por
um motivo, e dá para entender cada um pela pergunta "o que aconteceria sem
ele?":

| Campo sem o qual... | ...o que quebra                                            |
| ------------------- | ---------------------------------------------------------- |
| método              | um link de leitura autorizaria escrita                     |
| caminho             | um link de um arquivo abriria todos                        |
| expiração           | um link vazado valeria para sempre                         |
| identificador da chave | trocar o segredo invalidaria todos os links de uma vez  |

**Por que assinatura e não senha?** Porque a CDN não precisa consultar banco
nenhum. Ela pergunta a um serviço que recalcula o HMAC com o segredo e compara.
A resposta sai em **611 nanossegundos** (medido, veja abaixo), e não depende de
estado compartilhado.

Duas sutilezas que o código trata e que quase todo tutorial ignora:

**A comparação é em tempo constante** (`hmac.Equal`). Um `==` comum para no
primeiro byte diferente, e essa diferença de tempo, medida muitas vezes, deixa
descobrir a assinatura byte a byte. É um ataque real, e a defesa custa uma linha.

**A assinatura é conferida antes do prazo.** Parece detalhe de ordem, não é: até
o HMAC fechar, o campo `exp` é apenas um número que qualquer um escreveu. Checar
a validade primeiro seria acreditar em dado não autenticado.

### `auth_request`: perguntar antes de servir

O Nginx tem uma diretiva que, antes de responder, faz uma requisição interna a
outro serviço e usa o **código de status** como resposta: 204 autoriza, 403
recusa.

```nginx
location /objects/ {
    auth_request /_auth;   # roda ANTES de olhar o cache
    proxy_cache cdn;
    ...
}
```

O detalhe que faz tudo funcionar: `auth_request` roda na fase de **acesso**, que
vem antes da fase de **conteúdo**. Uma resposta que sairia do cache passa por ele
do mesmo jeito. Se fosse o contrário, o primeiro usuário autorizado abriria o
objeto para o mundo, e existe um teste de integração só para provar que não é
assim ([test/cdn_integration_test.go](test/cdn_integration_test.go),
`TestCacheQuenteNaoDispensaAutorizacao`).

Uma armadilha que custou tempo: **a subrequisição do `auth_request` é sempre um
GET**, e as variáveis dela descrevem a subrequisição, não o pedido original. Se o
validador lesse `$request_method` ali, ele veria "GET" para qualquer método, e um
token de leitura autorizaria qualquer coisa que a CDN deixasse passar. Por isso
o método verdadeiro é capturado antes, com `set`, e enviado num header próprio.

### Normalização: quando as duas pontas discordam, alguém entra

Este é o ponto mais afiado do projeto.

O Nginx decide **o que servir e o que guardar** usando o caminho que ele
normalizou. O validador decide **se autoriza** usando o caminho que ele
normalizou. Se as duas normalizações divergirem em algum caso, existe uma
requisição que é autorizada como um caminho e servida como outro. É a forma
clássica de burlar autorização em proxy, e ela já apareceu em produtos grandes.

A solução aqui não é adivinhar todas as diferenças entre as regras do Nginx e as
do Go. É **exigir que as duas cheguem ao mesmo resultado**: a CDN manda também
o seu caminho normalizado, e o validador recusa quando discorda do dele. Perder
uma requisição legítima esquisita é muito mais barato que autorizar a errada.

### Cache stampede e o `proxy_cache_lock`

"Stampede" é debandada. O cenário: um objeto popular expira e, no mesmo instante,
cem clientes pedem por ele. Sem coordenação, a CDN abre cem conexões com a
origem para buscar **o mesmo arquivo**. É o pior momento possível para
multiplicar carga, porque acontece justamente quando o conteúdo está em alta.

Uma linha resolve:

```nginx
proxy_cache_lock on;
```

Um cliente vai à origem, os outros esperam a resposta dele. O experimento mediu
os dois lados, e o resultado está mais abaixo.

### Stale: servir o velho quando não há o novo

Quando a origem está fora do ar, a CDN tem duas opções: propagar o erro ou
entregar a cópia vencida que ainda tem guardada.

```nginx
proxy_cache_use_stale error timeout updating http_500 http_502 http_503 http_504;
```

Isso não é bônus, é **escolha com consequência**. O cliente recebe uma versão que
pode estar errada, e sem saber disso. Para uma imagem, é o melhor negócio do
mundo. Para um preço ou um saldo, pode ser pior que um erro honesto. Quem decide
é quem conhece o conteúdo.

### Revalidação condicional: perguntar "mudou?" em vez de baixar de novo

Quando o prazo de uma entrada vence, o conteúdo não necessariamente mudou. A
CDN então pergunta à origem: "ainda vale este ETag?". Se a resposta for **304
Not Modified**, a entrada é renovada sem transferir um byte de corpo.

O efeito disso aparece de forma muito clara na comparação de TTLs medida adiante:
o TTL curto custou **quatro vezes mais chamadas** à origem, e **exatamente os
mesmos bytes**.

---

## O que foi medido de verdade

Tudo abaixo foi medido nesta máquina, não copiado de blog. A evidência bruta está
em [evidence/](evidence/), com ambiente, comandos e JSON de cada execução.

### A máquina onde isto rodou

| Item                                       | Valor                               |
| ------------------------------------------ | ------------------------------------ |
| CPU                                        | Intel Core i9-13980HX (13ª geração) |
| Processadores lógicos visíveis ao processo | 32                                  |
| Memória disponível ao Linux                | 15 GiB                              |
| Sistema                                    | Ubuntu sobre WSL2                   |
| Kernel                                     | 5.15.167.4-microsoft-standard-WSL2  |
| Go                                         | 1.26.5                              |
| Docker                                     | 28.1.1                              |
| Nginx                                      | 1.27.5 (imagem alpine)              |

Cliente, CDN, validador e origem rodam todos aqui dentro, disputando a mesma
CPU e falando por `localhost`. Não há rede de verdade no caminho. Os números de
latência absoluta, na casa de décimos de milissegundo, medem o custo de CPU do
software, não o que aconteceria entre duas máquinas. **As comparações entre
cenários continuam válidas**, porque todos sofreram a mesma interferência.

### Cache stampede: 100 clientes, o mesmo objeto ausente

Origem com 300 ms de latência artificial, para abrir a janela em que o problema
existe. Objeto de 1 MiB nunca pedido antes.

| Variante     | `proxy_cache_lock` | Chamadas na origem | MiB da origem | p50      | p99      |
| ------------ | ------------------ | -----------------: | ------------: | -------: | -------: |
| **com lock** | ligado             |              **1** |           1,0 | 598,7 ms | 639,6 ms |
| **sem lock** | desligado          |            **100** |         100,0 | 321,7 ms | 398,2 ms |

**Cem vezes menos trabalho na origem para entregar exatamente o mesmo conteúdo.**
Do lado do cliente: 99 respostas HIT e uma MISS com lock; 100 MISS sem lock.

Agora olhe a coluna de latência, porque ela ensina mais que a primeira.

**Com o lock, os clientes ficaram mais lentos.** O p50 dobrou. Faz sentido: 99
deles esperaram um único cliente ir à origem, voltar e popular o cache, em vez de
cada um buscar por conta própria. É um **trade-off real**, não uma vitória
gratuita: você troca latência do cliente por proteção da origem.

E a troca vale a pena por um motivo que este experimento, sozinho, não mostra:
aqui a origem aguentou as 100 chamadas simultâneas. Numa origem que satura, as
100 chamadas fazem todo mundo esperar do mesmo jeito, só que com a origem
caindo junto. O lock é caro quando não é necessário e é barato quando é.

### Hit ratio e alívio da origem: o TTL decide

Mesma carga nos dois cenários: 500 req/s durante 20 s (10.000 requisições), 200
objetos de 64 KiB, com 10 deles concentrando 80% do tráfego, cache começando
vazio.

|                                | TTL de 60s | TTL de 3s  |
| ------------------------------ | ---------: | ---------: |
| Requisições                    |     10.000 |     10.000 |
| Hit ratio na CDN             |     98,00% |     98,00% |
| **Chamadas à origem**          |    **200** |    **831** |
| das quais, corpos completos    |        200 |        200 |
| das quais, revalidações (304)  |          0 |        631 |
| **Bytes servidos pela origem** | **12,5 MiB** | **12,5 MiB** |
| Alívio da origem               |     98,00% |     91,69% |
| p50 / p99 no cliente           | 0,35 / 0,65 ms | 0,34 / 0,68 ms |

Três leituras que valem mais que o número grande:

**O hit ratio não conta a história toda.** Os dois cenários mostram 98%, e um
gastou quatro vezes mais chamadas à origem que o outro. Quem escolhe uma métrica
só para reportar vai escolher a que não mudou.

**Custo em chamadas e custo em bytes são coisas diferentes.** O TTL curto
multiplicou as chamadas por quatro e não transferiu um byte a mais, porque as 631
chamadas extras foram revalidações que a origem respondeu com 304. Se a métrica
de capacidade for banda, o TTL curto foi de graça. Se for requisições por
segundo, ele custou caro.

**As 200 primeiras chamadas são o preço do cache frio.** São exatamente os 200
objetos distintos: cada um precisa ser buscado uma vez. Nenhum cache evita isso, e
qualquer relatório que mostre 100% de hit ratio está medindo com o cache já
aquecido.

### Origem indisponível: a cópia velha segura o rojão

Objeto guardado com TTL de 3 s, origem passada a responder 503, dez requisições
depois do vencimento.

| Medida                                  | Valor    |
| ---------------------------------------- | -------: |
| Respostas servidas do cache vencido     | **10/10** |
| Erros propagados ao cliente             |    **0** |
| Chamadas que chegaram à origem          |       10 |
| **Tentativas por pedido**               | **1,00** |
| Requisições até a entrada voltar a HIT  |        2 |
| Revalidações 304 depois da volta        |        1 |
| Corpos baixados de novo depois da volta |        0 |

O cliente não viu a queda: dez respostas 200, com `X-Cache-Status: STALE` e o
conteúdo idêntico ao original, o que prova que veio do cache e não de uma origem
viva.

**Tentativas por pedido = 1,00 é o número que importa aqui.** Uma CDN mal
configurada responde a uma origem doente tentando de novo, e de novo, e transforma
uma queda em uma tempestade que impede a origem de se levantar. Aqui,
`proxy_next_upstream off` garante uma tentativa por pedido.

Isso também expõe um limite honesto: a CDN não amplifica, mas também **não para
de tentar**. Cada requisição do cliente vira uma tentativa na origem, para sempre.
Um circuit breaker, que para de tentar por um tempo depois de N falhas, é o passo
seguinte, e ele é assunto do P03.

Um detalhe do experimento que quase virou conclusão errada: depois da origem
voltar, o conteúdo continuou "antigo". Não é falha. O objeto é determinístico, o
ETag não mudou, a revalidação devolveu 304 e o Nginx renovou o **frescor** da
cópia que já tinha, headers inclusive. O sinal de recuperação é a entrada voltar a
HIT com revalidação registrada na origem, não o corpo mudar. A primeira versão do
experimento procurava a coisa errada.

### Os treze vetores negativos

Teste negativo é o que prova que algo **não** acontece. Cada vetor declara o que
espera antes de rodar, e a bateria inteira falha se um só passar quando deveria
ser recusado.

| Vetor                        | O que tenta                                       | Resultado |
| ----------------------------- | -------------------------------------------------- | --------- |
| `sem-token`                  | pedir objeto protegido sem token                  | 403       |
| `assinatura-alterada`        | trocar um caractere do HMAC                       | 403       |
| `chave-desconhecida`         | apontar para um `kid` que não existe              | 403       |
| `token-de-outro-objeto`      | colar um token válido na URL de outro arquivo     | 403       |
| `token-expirado`             | usar um token de 1 s dois segundos depois         | 403       |
| `metodo-nao-permitido`       | POST com token válido de GET                      | 403       |
| `corpo-grande`               | 1 MiB de corpo contra o limite de 4 KiB           | 413       |
| `host-desconhecido`          | `Host: atacante.example`                          | conexão fechada |
| `query-nao-entra-na-chave`   | `&cachebuster=1` para criar uma entrada nova      | HIT na mesma entrada |
| `caminho-codificado`         | `/objects/sub/%2e%2e/arquivo.bin`                 | HIT na mesma entrada |
| `header-forjado`             | `X-Forwarded-Host`, `Cookie` e `Authorization` forjados | não chegam à origem |
| `token-nao-chega-na-origem`  | verificar a URI que a origem recebeu              | sem `sig=` nem `kid=` |
| `replay-dentro-da-validade`  | usar o mesmo link duas vezes                      | **funciona, e é assim mesmo** |

O último não é defeito, é a natureza de uma URL assinada: quem recebe o link
repassa o link. O controle contra isso é o **prazo curto**, não a assinatura. Uma
URL assinada é um crachá de visitante, não um crachá com foto.

### O que os testes negativos pegaram

Na primeira execução da bateria, o vetor `token-nao-chega-na-origem` falhou: a
rota de diagnóstico da CDN estava entregando a query string inteira à origem, e
é na query que o token viaja. A origem passaria a ter credencial válida em log.

A causa foi uma diferença sutil do Nginx: **`proxy_pass` com URI literal repassa
a query original; com variável, não repassa.** As rotas de objeto já usavam
variável e estavam corretas; a de diagnóstico, não.

As duas execuções, a que falhou e a que passou depois da correção, estão em
[evidence/testes-negativos/](evidence/testes-negativos/). Guardar a vermelha é
mais honesto que guardar só a verde: foi ela que pagou o próprio custo.

### Quanto custa validar um token

```
BenchmarkVerify-32    1945878    611.4 ns/op    792 B/op    20 allocs/op
```

611 nanossegundos por validação, ou cerca de 1,6 milhão por segundo por núcleo.
Para uma CDN que atende 50 mil requisições por segundo, a validação de token
custa aproximadamente 3% de um núcleo. **É barato o suficiente para ser feita a
cada requisição**, e é esse número que a extensão opcional em Lua precisaria
bater para se justificar.

---

## Como rodar

Você precisa de Docker com Compose e de Go 1.26 ou mais novo.

Esta seção é a referência dos comandos. Para a execução guiada, com o que esperar
de cada saída, veja o [PASSO-A-PASSO.md](PASSO-A-PASSO.md).

### 1. Gerar os segredos

```bash
cp .env.example .env
```

Depois troque os valores por segredos de verdade:

```bash
printf 'CDN_ORIGIN_SECRET=%s\n' "$(openssl rand -hex 32)"
printf 'CDN_SIGNING_KEYS=k1:%s,k2:%s\n' "$(openssl rand -hex 32)" "$(openssl rand -hex 32)"
```

O `.env` está no `.gitignore`. **Nenhum segredo entra no repositório**, e é por
isso que a configuração do Nginx é um template com `${CDN_ORIGIN_SECRET}`,
preenchido pelo entrypoint da imagem oficial na hora de subir.

Repare que são duas chaves de assinatura. Isso não é redundância: é o que permite
**rotacionar** o segredo. A chave nova passa a assinar, a antiga continua sendo
aceita até o token mais longo em circulação expirar, e só então sai do conjunto.
Sem sobreposição, trocar o segredo derrubaria todos os links já entregues.

### 2. Subir o ambiente

```bash
docker compose up -d --build
curl -s localhost:8080/healthz
```

Quatro containers sobem:

| Serviço       | Papel                                                   | Publicado em          |
| ------------- | -------------------------------------------------------- | ---------------------- |
| `cdn`         | Nginx: proxy, cache e controles                         | `127.0.0.1:8080`      |
| `tokend`      | Go: emite e valida URLs assinadas                       | `127.0.0.1:8082`      |
| `origin`      | Go: serve os objetos                                    | **não publicado**     |
| `logexporter` | Go: transforma o log da CDN em métricas Prometheus    | `127.0.0.1:8093`      |

**A origem não tem porta publicada de propósito.** Ela só existe dentro da rede do
Compose, alcançável pela CDN. Se estivesse publicada, um experimento poderia
medi-la direto sem perceber e concluir bobagem sobre o cache.

### 3. Pedir um link e usá-lo

```bash
# uma "aplicação" pede o link temporário
curl -s "localhost:8082/sign?path=/objects/foto-64KiB-a.bin&method=GET&ttl=2m"

# o link devolvido é usado na CDN
curl -sD- -o /dev/null "localhost:8080/objects/foto-64KiB-a.bin?kid=k2&exp=...&sig=..."
```

Peça duas vezes e olhe o header `X-Cache-Status`: `MISS` na primeira, `HIT` na
segunda. Agora tente sem a query: **403**, mesmo com o objeto em cache.

### 4. Os quatro experimentos

```bash
# 100 clientes no mesmo objeto ausente, com e sem lock
go run ./cmd/stampede

# hit ratio e alívio da origem, com o TTL como variável
go run ./cmd/loadgen -scenario hit-ratio-ttl-longo -objects 200 -popular 10 \
  -rate 500 -duration 20s -warmup 0 -origin-max-age 60s
go run ./cmd/loadgen -scenario hit-ratio-ttl-curto -objects 200 -popular 10 \
  -rate 500 -duration 20s -warmup 0 -origin-max-age 3s

# origem fora do ar e política de stale
go run ./cmd/outage

# os treze vetores negativos (sai com código 1 se algum falhar)
go run ./cmd/negative
```

Cada comando grava uma pasta em `evidence/<cenário>/<data-hora>/`.

### 5. Testes

```bash
go test -race ./...                        # unitários, com detector de corrida
go test -tags=integration ./test/... -v    # sobe o Compose e exercita o caminho inteiro
go test ./internal/signer -bench=. -benchmem -run '^$'
```

O teste de integração sobe o ambiente pelo testcontainers, roda e derruba tudo,
inclusive os volumes. Para iterar com o ambiente já de pé, use
`EDGE_SKIP_COMPOSE=1`.

### 6. Ver as métricas

```bash
curl -s localhost:8093/metrics | grep cdn_           # hit ratio, tempo na origem, vazamentos
curl -s localhost:8092/metrics | grep token_         # autorizações por resultado e motivo
curl -s localhost:8090/metrics | grep origin_        # o que chegou à origem
curl -s localhost:8090/admin/counters                # resumo em JSON
```

O `cdn_log_token_leaks_total` merece atenção: ele conta linhas do log da CDN
com marca de token, e o valor esperado é **zero para sempre**. Se alguém mexer no
formato do log e a query voltar, o número sobe e o teste de integração falha. Um
controle de segurança que ninguém verifica não é um controle.

---

## Onde isto aparece em produção

Todo provedor de CDN vende exatamente estas peças, com outros nomes: URL assinada
(signed URL), token da CDN, chave de cache customizada, request collapsing,
serve-stale-if-error. Este projeto implementa as versões honestas e mínimas
delas, para que as escolhas fiquem visíveis em vez de escondidas atrás de um
painel.

Saber que existe um botão chamado "cache key" é diferente de saber por que tirar
a query dela é a decisão que faz o cache funcionar, e por que deixar o Host nela
exige fechar a porta do Host desconhecido antes.

---

## Os limites do que foi comprovado

Esta seção é a mais importante do README, e a mais fácil de pular.

**Isto não é um WAF.** Um WAF inspeciona conteúdo, aplica regras contra injeção,
bot e abuso, e é atualizado o tempo todo. Aqui existem controles específicos de
proxy: limite de método, limite de corpo, limite de taxa, remoção de headers não
confiáveis e Host conhecido. Chamar isso de WAF seria mentira, e a chance de
alguém acreditar é justamente o problema.

**Uma URL assinada é reutilizável enquanto vale.** Quem recebe o link repassa o
link, e não há nada na assinatura que impeça isso. O controle é o prazo curto.
Amarrar o token ao IP ou a uma sessão é possível e tem outro custo, que este lab
não pagou.

**Uma região só, sem geografia.** Isto é uma CDN de uma região, não uma CDN
global. Não há presença geográfica, roteamento por proximidade, nem a rede
privada entre os servidores e a origem que um provedor real opera. O "C" de
Content Delivery Network está aqui; o "N" de rede espalhada pelo mundo, não.

**Não há Range nem entrega parcial.** A CDN apaga o header `Range`. Cachear
respostas 206 corretamente exige o módulo `slice` e uma chave que inclua o
intervalo. Recusar é melhor que cachear errado, e entrega parcial é assunto do
P01.

**O `/sign` é aberto.** Num sistema real, ele fica atrás de autenticação: quem
pede o link já precisa ter provado quem é. Aqui ele é aberto porque o alvo do
estudo é o que acontece **depois** que o link existe.

**Os números de latência não são de rede.** Tudo roda na mesma máquina, por
`localhost`, disputando a mesma CPU. Décimos de milissegundo medem custo de
software, não tempo de rede.

**Medição local não é capacidade de produção.** Este lab autoriza dizer "nesta
máquina, neste cenário, o cache absorveu 98% das requisições e o lock reduziu de
100 para 1 as chamadas à origem durante um stampede". Não autoriza dizer "nossa
CDN aguenta X requisições por segundo".

---

## Estrutura do projeto

```text
PASSO-A-PASSO.md  a execução guiada, comando a comando
cmd/
  origin/       a origem, o recurso caro que a CDN protege
  tokend/       emite e valida URLs assinadas (consultado a cada requisição)
  logexporter/  segue o log da CDN e expõe métricas Prometheus
  stampede/     experimento: 100 clientes no mesmo objeto ausente
  loadgen/      experimento: hit ratio e alívio da origem
  outage/       experimento: origem fora do ar e política de stale
  negative/     experimento: os treze vetores que devem ser recusados
internal/
  signer/       HMAC, prazo, rotação de chaves (mais benchmark)
  tokensrv/     os endpoints /sign e /auth
  originsrv/    a origem, com controles de degradação para os experimentos
  objects/      objetos sintéticos determinísticos, gerados pelo nome
  logexport/    log do Nginx -> métricas, com vigilância de vazamento
  promscrape/   leitura de /metrics para calcular diferenças entre janelas
  cdnclient/    o cliente que os experimentos usam
  evidence/     o pacote de evidência de cada execução
deploy/nginx/   a CDN: nginx.conf e o template com os controles
test/           teste de integração do caminho completo, com containers
evidence/       resultados medidos, um diretório por execução
```

### O que este projeto não escreveu à mão

O contrato do Edge pede que um mecanismo próprio só exista quando o
comportamento dele for a pergunta do projeto. A pergunta aqui é cache e
autorização da CDN, então:

| Ferramenta                       | Por que ela e não código próprio                          |
| --------------------------------- | ------------------------------------------------------------ |
| `tsenart/vegeta`                 | gera carga a taxa constante, tratando coordinated omission |
| `nxadm/tail`                     | seguir arquivo com rotação e truncamento é problema resolvido |
| `prometheus/common/expfmt`       | ler o formato de exposição tem detalhes que já estão testados |
| `prometheus/client_golang`       | métricas, registro e coletores de runtime                  |
| `golang.org/x/sync/errgroup`     | a largada simultânea dos cem clientes                      |
| `testcontainers-go` (módulo compose) | sobe, espera e derruba o ambiente do teste de integração |

O que **é** escrito à mão: a assinatura, a validação, a normalização, a chave de
cache e a leitura do log. Tudo isso é a pergunta do projeto.

Sobre o Vegeta, vale uma palavra a mais, porque a escolha é conceitual. Ele
dispara na hora marcada mesmo que a requisição anterior não tenha respondido. Um
gerador ingênuo, com N clientes em laço, faz o oposto: se o servidor fica lento,
ele naturalmente pede menos, e a lentidão some da medição. Esse viés tem nome,
coordinated omission, e é o erro mais comum em medição caseira de latência.

---

## A extensão opcional em Lua

O plano do Edge prevê validar o mesmo token em Lua, dentro do próprio proxy
(OpenResty), eliminando a subrequisição de rede.

Ela ainda não foi feita, e o motivo é o número acima: a validação custa 611 ns de
CPU, e a subrequisição local custa muito mais que isso, mas ainda assim o p99 do
lab fica abaixo de 1 ms. Trazer OpenResty significa uma segunda implementação da
regra de segurança, com risco de divergir da primeira, e duas implementações que
discordam sobre autorização é pior que uma implementação com uma chamada de rede
a mais.

A extensão só se justifica com um teste de contrato aplicando **os mesmos treze
vetores** às duas implementações. Sem isso, ela é troca de risco por
microssegundos.

---

## Resumo da ópera

O impulso natural de quem precisa proteger conteúdo é colocar a identidade do
usuário no caminho da requisição. Faça isso e o cache morre: cada pessoa passa a
ter a própria cópia, e a CDN vira um proxy caro.

A saída é separar as duas perguntas. **"Que arquivo é este?"** define o que a
CDN guarda, e a resposta não pode depender de quem está pedindo. **"Você pode
recebê-lo?"** é verificada a cada requisição, inclusive quando o arquivo já está
guardado. No Nginx isso são duas linhas: uma chave de cache sem a query string e
um `auth_request` que roda antes do cache. Medimos 98% de alívio na origem com
100% das requisições autorizadas individualmente.

O resto é sobre não se enganar. O `proxy_cache_lock` transformou 100 chamadas à
origem em 1, e cobrou o dobro de latência por isso, o que é um trade-off e não uma
vitória. O hit ratio ficou igual em dois cenários que custaram quatro vezes
diferente à origem, o que é um lembrete de que uma métrica só engana. A política
de stale escondeu uma queda completa do cliente, entregando conteúdo que podia
estar errado, o que é uma escolha e não um bônus.

E fica a lição que custou uma execução vermelha guardada no repositório: o teste
negativo que parecia burocracia foi quem descobriu que o token estava vazando
para a origem, por causa de uma diferença de uma linha entre `proxy_pass` com
caminho literal e `proxy_pass` com variável. Segurança da CDN não se lê na
configuração. Se prova com uma requisição que tenta passar.
