# Ambiente

- Cenário: hit-ratio-ttl-curto
- Início (UTC): 2026-07-25T22:32:52Z
- Commit: 1ca41ef
- Máquina: windows-note
- Sistema: linux/amd64
- CPUs visíveis ao processo: 32
- Go: go1.26.5
- Kernel: Linux 5.15.167.4-microsoft-standard-WSL2
- Memória: Mem:            15Gi       7.3Gi       7.0Gi        34Mi       1.5Gi       8.1Gi
- Docker: 28.1.1
- Nginx da CDN: nginx version: nginx/1.27.5

## Observações

Cliente, CDN e origem dividem a mesma máquina e disputam CPU. Os números absolutos carregam essa interferência; a comparação entre cenários é o que vale.
