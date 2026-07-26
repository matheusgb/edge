# Ambiente

- cenário: escala
- início (UTC): 20260726T030208Z
- commit: d7f8355900e7af101d4d9e67a0d6a964ad1bec05
- máquina: windows-note
- sistema operacional: Linux 5.15.167.4-microsoft-standard-WSL2
- réplicas de origin no início: 2 (mínimo configurado)
- réplicas de origin ao final (após janela de descida de 120s): 7

## Estado do cluster

```
NAME                     STATUS   ROLES           AGE   VERSION   INTERNAL-IP   EXTERNAL-IP   OS-IMAGE                         KERNEL-VERSION                       CONTAINER-RUNTIME
edge-control-plane   Ready    control-plane   19m   v1.31.0   172.19.0.3    <none>        Debian GNU/Linux 12 (bookworm)   5.15.167.4-microsoft-standard-WSL2   containerd://1.7.18
edge-worker          Ready    <none>          19m   v1.31.0   172.19.0.4    <none>        Debian GNU/Linux 12 (bookworm)   5.15.167.4-microsoft-standard-WSL2   containerd://1.7.18
edge-worker2         Ready    <none>          19m   v1.31.0   172.19.0.2    <none>        Debian GNU/Linux 12 (bookworm)   5.15.167.4-microsoft-standard-WSL2   containerd://1.7.18
```
