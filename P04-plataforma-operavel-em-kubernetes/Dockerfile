## Build multi-stage: origin e edge do P04 na mesma imagem, cada binário
## chamado explicitamente na hora de rodar o container.
FROM golang:1.26.5-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/origin ./cmd/edge ./cmd/loadgen ./cmd/capacity

## Imagem final: distroless, usuário não root, sem shell.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build --chown=nonroot:nonroot /out/origin /out/edge /out/loadgen /out/capacity ./
USER nonroot:nonroot

# :8080 é o tráfego público (origin ou edge, conforme o binário
# executado); :8090 é administrativo (métricas, pprof) e nunca deve ser
# publicado para fora do cluster.
EXPOSE 8080 8090

# Sem ENTRYPOINT fixo: o comando completo (qual binário e quais flags)
# é decidido por quem roda o container (docker run ou o Deployment do
# Kubernetes), porque a mesma imagem serve origin, edge, loadgen e
# capacity.
CMD ["/app/origin"]
