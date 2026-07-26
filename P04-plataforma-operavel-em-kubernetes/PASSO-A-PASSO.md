# Passo a passo: P04

Execução guiada, do zero até a destruição limpa. Todos os comandos assumem que
o diretório atual é `P04-plataforma-operavel-em-kubernetes/`, salvo indicação
contrária.

## Passo 1: ferramentas

```bash
which kubectl kind terraform tflint helm
```

Se alguma faltar, instalar (versões usadas neste laboratório):

```bash
curl -LO "https://dl.k8s.io/release/v1.31.4/bin/linux/amd64/kubectl"
install -m0755 kubectl ~/.local/bin/kubectl

curl -Lo kind https://kind.sigs.k8s.io/dl/v0.24.0/kind-linux-amd64
install -m0755 kind ~/.local/bin/kind

curl -Lo terraform.zip https://releases.hashicorp.com/terraform/1.9.8/terraform_1.9.8_linux_amd64.zip
unzip terraform.zip && install -m0755 terraform ~/.local/bin/terraform

curl -Lo tflint.zip https://github.com/terraform-linters/tflint/releases/download/v0.53.0/tflint_linux_amd64.zip
unzip tflint.zip && install -m0755 tflint ~/.local/bin/tflint

curl -Lo helm.tar.gz https://get.helm.sh/helm-v3.16.2-linux-amd64.tar.gz
tar xzf helm.tar.gz && install -m0755 linux-amd64/helm ~/.local/bin/helm
```

Confirmar que `~/.local/bin` está no `PATH`.

## Passo 2: testes do módulo Go

```bash
go test -race ./...
go vet ./...
go test -bench=Work -benchtime=200x ./internal/originsrv/...
```

Todos os pacotes devem passar; o benchmark alimenta o modelo de capacidade do
Passo 8.

## Passo 3: cluster kind com Calico

```bash
infra/kind/bootstrap.sh up
```

Isso cria o cluster (`kind-edge-lab`, 1 control-plane + 2 workers), aplica o
Calico (o CNI padrão do kind não aplica NetworkPolicy), builda a imagem
`p04-edge-lab:local` e uma segunda imagem `p04-edge-lab:fail-ready` (usada só
no experimento de rollout inválido), e carrega as duas no cluster.

Confirmar:

```bash
kubectl get nodes -o wide
kubectl -n kube-system get pods -l k8s-app=calico-node
```

## Passo 4: Terraform

```bash
cd infra/terraform/environments/local
terraform fmt -check -recursive ../../
terraform init
terraform validate
tflint --chdir=../../bootstrap-observability --init && tflint --chdir=../../bootstrap-observability
tflint --chdir=../../edge-platform --init && tflint --chdir=../../edge-platform
tflint --init && tflint
terraform test # roda em cada módulo que tiver tests/, ou entre em cada um e rode lá
terraform plan -out=edge.tfplan
terraform apply edge.tfplan
```

Confirmar que Prometheus, Grafana, ingress-nginx e metrics-server subiram:

```bash
kubectl -n observability get pods
kubectl -n edge get pods,hpa,networkpolicy
```

## Passo 5: smoke test manual (opcional)

```bash
kubectl run -n edge smoke --rm -i --restart=Never \
  --image=p04-edge-lab:local --image-pull-policy=Never \
  --overrides='{"metadata":{"labels":{"role":"experiments"}}}' \
  --command -- /app/loadgen \
  -url=http://edge.edge.svc.cluster.local:8080/object/smoke.bin \
  -scenario=smoke -steps=x:5:5s:2 -evidence-dir=/tmp/ev
```

## Passo 6: os cinco experimentos

Cada script roda o número de repetições passado como argumento (3 é o
protocolo do repositório) e grava evidência em `evidence/<cenário>/`.

```bash
cd ../../../..   # volta para a raiz do projeto P04
infra/experiments/escala.sh 3            # ~10-12 minutos
infra/experiments/pod-encerrado.sh 3     # ~2-3 minutos
infra/experiments/rollout-invalido.sh 3  # ~10-12 minutos
infra/experiments/restricao-de-rede.sh 3 # ~2 minutos
infra/experiments/destruicao.sh 3        # ~5-7 minutos (destrói e reaplica a cada rodada)
```

Conferir cada `evidence/<cenário>/<timestamp>/summary.md` depois.

## Passo 7: dashboards e alertas

```bash
kubectl -n observability port-forward svc/kube-prometheus-stack-grafana 3000:80
# usuário/senha padrão do chart, ver a documentação do kube-prometheus-stack
```

O dashboard "edge-lab P04 - plataforma" já vem carregado (ConfigMap rotulado
`edge_lab_dashboard=1`, recolhido pelo sidecar do Grafana). As regras de
alerta ficam em `infra/terraform/edge-platform/alerts/edge-lab-p04-rules.yaml`
e são aplicadas via `kubectl apply` dentro do próprio `terraform apply`
(ver `infra/terraform/edge-platform/alerts.tf` para o motivo de não usar
`kubernetes_manifest` diretamente).

## Passo 8: modelo de capacidade

```bash
go run ./cmd/capacity \
  -capacity-per-replica-rps=126.6 \
  -target-utilization=0.5 \
  -headroom=1.2 \
  -cache-hit-rate=0 \
  -avg-response-bytes=65536 \
  -zones=3 \
  -zone-failure-tolerant=true
```

Ver `evidence/modelo-de-capacidade.md` para de onde vem cada número de
entrada e os limites explícitos do resultado.

## Passo 9: destruição final

```bash
cd infra/terraform/environments/local
terraform destroy
cd ../../../..
infra/kind/bootstrap.sh down
```

O primeiro comando derruba o que o Terraform criou (namespaces `edge` e
`observability`); o segundo derruba o cluster kind inteiro. São escopos
diferentes de propósito, ver o experimento de destruição
(`evidence/destruicao/`) para a confirmação medida disso.

## Encerrando

Depois do Passo 9, `kind get clusters` não deve listar `edge-lab`, e nenhum
recurso Docker do laboratório deve continuar rodando (`docker ps` sem
containers `edge-lab-*`).

## Se algo der errado

- **`terraform apply` trava num `helm_release`**: checar se o `docker info`
  reportado dentro do WSL2 (não só no Windows) tem memória suficiente; o
  kube-prometheus-stack sozinho já pede uns 500MB de folga.
- **HPA mostra `TARGETS: <unknown>/50%` por muito tempo**: o metrics-server
  ainda não coletou a primeira amostra; esperar ~1 minuto ou checar
  `kubectl -n observability logs deploy/metrics-server`.
- **`kubectl run` do experimento fica pendurado**: confirmar que a imagem
  `p04-edge-lab:local` foi carregada no cluster certo
  (`infra/kind/bootstrap.sh load`) e que o pod está com
  `image_pull_policy=Never`, não tentando puxar de um registry inexistente.
- **Experimento de restrição de rede mostra tráfego liberado onde deveria
  bloquear**: confirmar que o CNI é o Calico, não o kindnet padrão
  (`kubectl -n kube-system get pods -l k8s-app=calico-node`); sem ele, nenhuma
  NetworkPolicy é aplicada de verdade.
