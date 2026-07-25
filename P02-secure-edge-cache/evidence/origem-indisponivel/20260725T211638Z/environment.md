# Ambiente

- Cenário: origem-indisponivel
- Início (UTC): 2026-07-25T21:16:38Z
- Commit: 94e69af
- Máquina: windows-note
- Sistema: linux/amd64
- CPUs visíveis ao processo: 32
- Go: go1.24.2
- Kernel: Linux 5.15.167.4-microsoft-standard-WSL2
- Memória: Mem:            15Gi       6.9Gi       7.4Gi       8.5Mi       1.5Gi       8.6Gi
- Docker: 28.1.1
- Nginx da borda: nginx version: nginx/1.27.5

## Observações

A origem foi derrubada pelo modo de falha da porta administrativa, não por parada de container. O caminho de erro exercitado é http_503; uma parada real também exercita timeout e recusa de conexão.
