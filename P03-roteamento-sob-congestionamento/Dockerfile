# Imagem multi-stage com os três serviços Go do projeto.
#
# Um binário por serviço, uma imagem só. Eles compartilham dependências e base, e
# manter três Dockerfiles quase idênticos criaria três lugares para esquecer de
# atualizar.

# ---------- estágio 1: build ----------
FROM golang:1.26.5-alpine AS build

WORKDIR /src

# Os manifestos primeiro: enquanto as dependências não mudarem, o Docker
# reaproveita a camada de download a cada rebuild.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produz binário estático, que roda numa base sem sistema
# operacional. -trimpath tira caminhos da máquina de build, o que torna o binário
# reproduzível. -ldflags "-s -w" descarta símbolos e DWARF.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/ ./cmd/origin ./cmd/edge ./cmd/router

# ---------- estágio 2: imagem final ----------
# distroless "static" não tem shell nem gerenciador de pacotes: quem conseguir
# executar comando dentro do container não encontra /bin/sh para usar.
FROM gcr.io/distroless/static-debian12:nonroot

USER nonroot:nonroot

COPY --from=build --chown=nonroot:nonroot /out/origin /usr/local/bin/origin
COPY --from=build --chown=nonroot:nonroot /out/edge /usr/local/bin/edge
COPY --from=build --chown=nonroot:nonroot /out/router /usr/local/bin/router

# 8080 pública, 8090 administrativa. A administrativa não deve ser publicada para
# fora da máquina.
EXPOSE 8080 8090

ENTRYPOINT ["/usr/local/bin/router"]
