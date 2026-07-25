# Ambiente

- Cenário: hit-ratio-ttl-longo
- Início (UTC): 2026-07-25T22:32:28Z
- Commit: 1ca41ef
- Máquina: windows-note
- Sistema: linux/amd64
- CPUs visíveis ao processo: 32
- Go: go1.26.5
- Kernel: Linux 5.15.167.4-microsoft-standard-WSL2
- Memória: Mem:            15Gi       7.4Gi       7.1Gi        21Mi       1.4Gi       8.1Gi
- Docker: 28.1.1
- Nginx da CDN: nginx version: nginx/1.27.5

## Observações

Cliente, CDN e origem dividem a mesma máquina e disputam CPU. Os números absolutos carregam essa interferência; a comparação entre cenários é o que vale.
