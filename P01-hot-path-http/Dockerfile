# Imagem multi-stage: compila num estágio e descarta o compilador no final.
#
# Por que isso importa: a imagem do Go tem ~800 MB, quase tudo ferramenta de build.
# Nada disso precisa ir para produção, e cada binário a mais na imagem é mais
# superfície para alguém explorar caso consiga executar comandos no container.

# ---------- estágio 1: build ----------
FROM golang:1.24.2-alpine AS build

WORKDIR /src

# Copiamos só os manifestos primeiro. Assim, enquanto as dependências não mudarem,
# o Docker reaproveita a camada de download em cada rebuild.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produz um binário estático, sem depender da libc da imagem final —
# é o que permite rodar a partir de uma base sem sistema operacional.
# -trimpath remove caminhos da máquina de build, tornando o binário reproduzível.
# -ldflags "-s -w" descarta tabela de símbolos e DWARF: imagem menor.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/origin ./cmd/origin \
    && CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/cataloggen ./cmd/cataloggen

# ---------- estágio 2: imagem final ----------
# distroless "static" não tem shell, gerenciador de pacotes nem utilitários.
# Se alguém conseguir execução de comando no container, não há /bin/sh para usar.
FROM gcr.io/distroless/static-debian12:nonroot

# nonroot é o usuário 65532. Rodar como root dentro do container significa que uma
# fuga do container começa com privilégio de root no host.
USER nonroot:nonroot

COPY --from=build --chown=nonroot:nonroot /out/origin /usr/local/bin/origin
COPY --from=build --chown=nonroot:nonroot /out/cataloggen /usr/local/bin/cataloggen

# 8080 é o tráfego público; 8081 tem métricas e pprof e NÃO deve ser publicada.
EXPOSE 8080
EXPOSE 8081

# O catálogo entra por volume. O container roda com o sistema de arquivos somente
# leitura (--read-only), e o volume dos objetos também é montado :ro — o servidor
# só lê os objetos, nunca escreve.
VOLUME ["/data/objects"]

ENTRYPOINT ["/usr/local/bin/origin"]
CMD ["-dir", "/data/objects", "-addr", ":8080", "-admin-addr", ":8081"]
