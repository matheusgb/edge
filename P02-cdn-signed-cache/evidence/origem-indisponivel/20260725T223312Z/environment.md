# Ambiente

- Cenário: origem-indisponivel
- Início (UTC): 2026-07-25T22:33:12Z
- Commit: 1ca41ef
- Máquina: windows-note
- Sistema: linux/amd64
- CPUs visíveis ao processo: 32
- Go: go1.26.5
- Kernel: Linux 5.15.167.4-microsoft-standard-WSL2
- Memória: Mem:            15Gi       7.3Gi       6.8Gi        35Mi       1.7Gi       8.1Gi
- Docker: 28.1.1
- Nginx da CDN: nginx version: nginx/1.27.5

## Observações

A origem foi derrubada pelo modo de falha da porta administrativa, não por parada de container. O caminho de erro exercitado é http_503; uma parada real também exercita timeout e recusa de conexão.
