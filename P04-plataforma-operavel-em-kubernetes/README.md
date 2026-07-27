# P04: plataforma operável em Kubernetes

## O problema, em linguagem direta

Um serviço rápido não é a mesma coisa que um serviço operável. P01, P02 e P03
já mostraram como fazer uma origem e uma CDN em Go que respondem rápido. Este
projeto pega uma versão mínima dessas duas peças e responde a uma pergunta
diferente: outra pessoa, sem ter escrito uma linha desse código, consegue
implantar, observar, escalar e recuperar esse serviço com segurança?

Isso significa ter probes que fazem sentido, um HPA ([Horizontal Pod
Autoscaler](https://kubernetes.io/docs/tasks/run-application/horizontal-pod-autoscale/))
que reage de verdade, uma NetworkPolicy que bloqueia tráfego de verdade (não só
declara a intenção), um rollout que não manda tráfego para uma versão
quebrada, e um jeito reproduzível de destruir tudo de novo.

## Funcionamento macro

```text
loadgen (dentro do cluster, rotulado role=experiments)
        |
        v
   Service edge  --(NetworkPolicy libera só isto)-->  Service origin
        |                                                    |
   Deployment edge (cache em memória, TTL curto)      Deployment origin
        |                                                    |
        +---------------- Prometheus/Grafana ----------------+
                    (namespace observability, via Helm)
```

Um cluster [kind](https://kind.sigs.k8s.io/) local fornece os nós Kubernetes.
O CNI padrão do kind (kindnet) **não aplica NetworkPolicy**, então o bootstrap
troca por Calico antes de qualquer coisa (`infra/kind/bootstrap.sh`). O
Terraform entra depois, organizado por responsabilidade:

```text
infra/terraform/
  bootstrap-observability/   namespace, kube-prometheus-stack, ingress-nginx, metrics-server
  edge-platform/              Deployments, Services, ConfigMap, HPA, PDB, NetworkPolicy, RBAC
  environments/local/         compõe os dois módulos acima contra o cluster kind
```

O bootstrap do kind é deliberadamente um comando separado do Terraform: um
cluster local é uma ferramenta de teste, não infraestrutura de nuvem, e o
Terraform só deve assumir que um cluster com o contexto certo já existe.

## O conceito principal

Uma plataforma operável não é a soma de ferramentas (Kubernetes + Terraform +
Prometheus). É a garantia de que cada mecanismo de segurança e recuperação
realmente funciona quando testado, não apenas quando declarado. Por isso este
projeto não aceita "a NetworkPolicy existe" como resultado: ele testa se ela
bloqueia (`evidence/restricao-de-rede/`). Não aceita "o HPA está configurado"
como resultado: testa se ele reage e mede o atraso (`evidence/escala/`). E não
aceita "o rollout tem readiness probe" como resultado: testa se uma versão
quebrada de verdade fica sem tráfego (`evidence/rollout-invalido/`).

## A utilidade em produção

Cada peça daqui tem um paralelo direto em produção:

- probes distintas para início, prontidão e vida do processo evitam que um pod
  lento no boot seja matado antes da hora, ou que um pod travado continue
  recebendo tráfego;
- HPA com comportamento de subida e descida explícito evita tanto
  subdimensionamento sob pico quanto oscilação (flapping) na volta à calma;
- PodDisruptionBudget e espalhamento entre nós reduzem o impacto de
  manutenção e falha de um nó;
- NetworkPolicy com negação padrão limita o raio de um comprometimento a
  exatamente o tráfego que foi liberado;
- RBAC restrito e contas de serviço sem token automontado reduzem o que um pod
  comprometido consegue fazer contra a API do cluster.

## A preparação e os comandos originais

Ver `PASSO-A-PASSO.md` para a execução guiada, passo a passo, incluindo a
instalação das ferramentas que faltarem. Resumo dos comandos principais:

```bash
# ferramentas (uma vez): kubectl, kind, terraform, tflint, helm

# testes e build do módulo Go
go test -race ./...
go vet ./...

# cluster kind + Calico + imagens
infra/kind/bootstrap.sh up

# Terraform
cd infra/terraform/environments/local
terraform fmt -check -recursive
terraform init
terraform validate
terraform test
tflint --recursive ../../
terraform plan -out=edge.tfplan
terraform apply edge.tfplan

# experimentos (a partir da raiz do projeto)
infra/experiments/escala.sh 3
infra/experiments/pod-encerrado.sh 3
infra/experiments/rollout-invalido.sh 3
infra/experiments/restricao-de-rede.sh 3
infra/experiments/destruicao.sh 3

# destruição final
cd infra/terraform/environments/local && terraform destroy
infra/kind/bootstrap.sh down
```

## O caminho feliz

Com o cluster de pé e o Terraform aplicado, uma requisição a
`http://edge.edge.svc.cluster.local:8080/object/<nome>` retorna um objeto
sintético determinístico (o mesmo nome sempre produz o mesmo conteúdo e o
mesmo ETag). É servida do cache em memória da borda quando o TTL de 5s ainda é
válido, ou buscada na origem e cacheada quando não.

`http://.../work?n=<N>` sempre passa direto pela borda até a origem (nunca é
cacheado, de propósito: existe para gerar CPU medível a cada chamada) e
devolve um checksum depois de N iterações de hash.

Os testes de integração (`test/plataforma_integration_test.go`) exercitam
exatamente esse caminho contra processos HTTP reais, não contra mocks.

## A falha controlada

Cinco falhas foram provocadas de propósito, cada uma com três repetições
medidas em `evidence/`:

1. **Escala**: carga em degraus de 20 a 350 rps. O HPA da origem reagiu de 2
   para 8 réplicas em cerca de 136s numa das rodadas (primeira réplica nova em
   76s), sem nenhum erro do cliente em nenhuma repetição.
2. **Pod encerrado**: um pod da origem removido durante carga estável de 40
   rps. Pod substituto pronto em 5,0s nas três rodadas, sem erro do cliente
   correlacionado à remoção (post-mortem completo em
   `postmortem/falha-controlada.md`).
3. **Rollout inválido**: uma imagem que nunca passa na readiness
   (`ORIGIN_FAIL_READY=true`) foi implantada. `kubectl rollout status` travou
   em todas as rodadas (código de saída diferente de 0), o IP de nenhum pod da
   versão ruim chegou a aparecer nos endpoints do Service, e 0 de 3.600
   requisições do cliente falharam em cada rodada.
4. **Restrição de rede**: cinco casos por rodada (tráfego autorizado e não
   autorizado, direto na origem e via borda, na porta pública e na
   administrativa). Os cinco se comportaram exatamente como esperado nas três
   rodadas, confirmando que é o Calico bloqueando de verdade, não só uma
   policy declarada (o CNI padrão do kind não teria bloqueado nada).
5. **Destruição**: `terraform destroy` seguido de confirmação de que os
   namespaces `edge` e `observability` desapareceram e de que os 3 nós do
   cluster kind continuam de pé (a destruição é escopo do Terraform, não do
   kind).

## O resultado medido

Números com evidência correspondente em `evidence/`:

- HPA: de 2 para 8 réplicas da origem sob rampa de 20 a 350 rps, primeira
  réplica nova em 76s, pico em cerca de 136s (`evidence/escala/`).
- Recuperação de pod: 5,0s até um pod substituto ficar pronto, em três
  rodadas (`evidence/pod-encerrado/`).
- Rollout inválido: 0 requisições com erro em 3.600 por rodada, em três
  rodadas (`evidence/rollout-invalido/`).
- NetworkPolicy: 5 de 5 casos corretos (2 permitidos, 3 bloqueados), em três
  rodadas (`evidence/restricao-de-rede/`).
- Destruição: `terraform destroy` com código 0 e namespaces confirmadamente
  removidos, em três rodadas, cluster kind sobrevivendo às três
  (`evidence/destruicao/`).
- Custo de CPU do handler `/work`: ~0,79ms para 100.000 iterações de hash,
  medido via `go test -bench=Work ./internal/originsrv/...`.

## O limite do que foi comprovado

- O cluster tem 32 CPUs lógicas disponíveis no host; nenhuma rodada chegou
  perto de saturar uma réplica individual. Então o "atraso do HPA" medido
  aqui é sobre reação a um degrau de carga, não sobre comportamento sob
  saturação real.
- "3 zonas" no modelo de capacidade (`evidence/modelo-de-capacidade.md`) é uma
  analogia com os 3 nós do kind, não zonas de disponibilidade de um provedor
  real: o cluster inteiro roda no mesmo host físico.
- O modelo de capacidade projeta 1.422 réplicas para sustentar a premissa de
  50 mil rps. Isso não foi criado nem testado: é uma extrapolação linear a
  partir de uma capacidade por réplica derivada de benchmark de CPU, não de
  uma saturação end-to-end medida.
- O experimento de pod encerrado rodou com mais de duas réplicas ativas (o
  HPA já havia escalado por experimentos anteriores na mesma sessão); o
  post-mortem declara essa limitação e propõe como repetir o teste no
  cenário de mínimo configurado.
- O PodDisruptionBudget não foi exercitado por uma drenagem de nó real, só
  por remoção direta de pod, que não passa pelo PDB.
- `terraform test` cobre plano (contagem e atributos de recursos), não
  aplica contra o cluster dentro do próprio teste; a correção contra o
  cluster real é responsabilidade dos scripts em `infra/experiments/`.
- Diagnóstico de Kubernetes num laptop não equivale a operar um cluster
  gerenciado real, com múltiplos nós físicos, múltiplas zonas e tráfego de
  produção.

## Estrutura do projeto

```text
P04-plataforma-operavel-em-kubernetes/
  cmd/{origin,edge,loadgen,capacity}/
  internal/{originsrv,edgesrv,metrics,evidence,loadtest,capacity,httpx}/
  test/
  infra/
    kind/                 cluster.yaml, bootstrap.sh
    terraform/             bootstrap-observability/, edge-platform/, environments/local/
    experiments/            um script por experimento obrigatório
  runbooks/
  postmortem/
  evidence/
```

## Resumo da ópera

P04 pega o que P01 a P03 já mediram sobre HTTP, cache e rede e transforma numa
pergunta de operação: alguém de fora consegue confiar nesta plataforma? A
resposta, com evidência medida em três repetições por cenário, é que o HPA
reage, o rollback funciona, a NetworkPolicy bloqueia de verdade (só depois de
trocar o CNI padrão do kind), e a destruição é limpa e reversível.

O que não está aqui é qualquer alegação de que isso prova capacidade de
produção: o modelo de capacidade projeta milhares de réplicas a partir de uma
extrapolação linear, não de uma medição em escala, e um laptop com um cluster
kind continua sendo, antes de tudo, um ambiente de laboratório.
