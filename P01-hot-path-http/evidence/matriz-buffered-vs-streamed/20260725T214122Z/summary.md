# Resumo: matriz-buffered-vs-streamed

Medição local em linux/amd64 com 32 CPUs. Isto NÃO é capacidade de produção:
gerador e servidor dividem a mesma máquina e disputam CPU entre si.

## O que o cliente observou

| Cenário | Modo | Objeto | Conc. | Oferecida/s | Concluída/s | p50 ms | p95 ms | p99 ms | Erro |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| buffered-obj-64KiB.bin-c8-rep1 | buffered | obj-64KiB.bin | 8 | 23245.2 | 23245.2 | 0.30 | 0.69 | 0.94 | 0.00% |
| buffered-obj-64KiB.bin-c8-rep2 | buffered | obj-64KiB.bin | 8 | 23440.4 | 23440.4 | 0.29 | 0.69 | 0.93 | 0.00% |
| streamed-obj-64KiB.bin-c8-rep1 | streamed | obj-64KiB.bin | 8 | 25870.3 | 25870.3 | 0.25 | 0.62 | 0.87 | 0.00% |
| streamed-obj-64KiB.bin-c8-rep2 | streamed | obj-64KiB.bin | 8 | 27708.4 | 27708.4 | 0.23 | 0.57 | 0.78 | 0.00% |
| buffered-obj-64KiB.bin-c128-rep1 | buffered | obj-64KiB.bin | 128 | 40219.1 | 40219.1 | 1.85 | 10.30 | 15.69 | 0.00% |
| buffered-obj-64KiB.bin-c128-rep2 | buffered | obj-64KiB.bin | 128 | 44212.4 | 44212.4 | 1.79 | 9.11 | 13.88 | 0.00% |
| streamed-obj-64KiB.bin-c128-rep1 | streamed | obj-64KiB.bin | 128 | 78516.8 | 78516.8 | 0.90 | 5.09 | 7.81 | 0.00% |
| streamed-obj-64KiB.bin-c128-rep2 | streamed | obj-64KiB.bin | 128 | 83857.1 | 83857.1 | 0.77 | 4.86 | 7.53 | 0.00% |
| buffered-obj-16MiB.bin-c8-rep1 | buffered | obj-16MiB.bin | 8 | 652.2 | 652.2 | 12.01 | 15.82 | 18.94 | 0.00% |
| buffered-obj-16MiB.bin-c8-rep2 | buffered | obj-16MiB.bin | 8 | 648.9 | 648.9 | 12.09 | 16.21 | 19.07 | 0.00% |
| streamed-obj-16MiB.bin-c8-rep1 | streamed | obj-16MiB.bin | 8 | 1766.1 | 1766.1 | 4.38 | 6.13 | 6.89 | 0.00% |
| streamed-obj-16MiB.bin-c8-rep2 | streamed | obj-16MiB.bin | 8 | 1674.9 | 1674.9 | 4.60 | 6.56 | 7.64 | 0.00% |
| buffered-obj-16MiB.bin-c128-rep1 | buffered | obj-16MiB.bin | 128 | 686.1 | 686.1 | 89.00 | 579.44 | 807.77 | 0.00% |
| buffered-obj-16MiB.bin-c128-rep2 | buffered | obj-16MiB.bin | 128 | 718.2 | 718.2 | 85.20 | 571.34 | 756.10 | 0.00% |
| streamed-obj-16MiB.bin-c128-rep1 | streamed | obj-16MiB.bin | 128 | 1854.0 | 1854.0 | 60.17 | 146.16 | 191.23 | 0.00% |
| streamed-obj-16MiB.bin-c128-rep2 | streamed | obj-16MiB.bin | 128 | 1832.7 | 1832.7 | 59.75 | 149.42 | 216.30 | 0.00% |

## O que o servidor relatou de si mesmo

| Cenário | MB/s entregues | Alocado na janela | Heap no fim | Coletas | Goroutines no fim |
|---|---:|---:|---:|---:|---:|
| buffered-obj-64KiB.bin-c8-rep1 | 1452.8 | 12.93 GB | 5.9 MB | 3264 | 27 |
| buffered-obj-64KiB.bin-c8-rep2 | 1465.0 | 13.03 GB | 8.3 MB | 2793 | 42 |
| streamed-obj-64KiB.bin-c8-rep1 | 1616.9 | 4.78 GB | 6.2 MB | 2052 | 58 |
| streamed-obj-64KiB.bin-c8-rep2 | 1731.8 | 5.11 GB | 6.0 MB | 2034 | 74 |
| buffered-obj-64KiB.bin-c128-rep1 | 2513.7 | 22.26 GB | 15.9 MB | 1342 | 330 |
| buffered-obj-64KiB.bin-c128-rep2 | 2763.3 | 24.46 GB | 25.9 MB | 1274 | 586 |
| streamed-obj-64KiB.bin-c128-rep1 | 4907.3 | 14.30 GB | 30.7 MB | 784 | 842 |
| streamed-obj-64KiB.bin-c128-rep2 | 5241.1 | 15.26 GB | 31.9 MB | 668 | 1098 |
| buffered-obj-16MiB.bin-c8-rep1 | 10434.4 | 54.97 GB | 169.6 MB | 420 | 1114 |
| buffered-obj-16MiB.bin-c8-rep2 | 10382.5 | 54.70 GB | 186.5 MB | 420 | 1130 |
| streamed-obj-16MiB.bin-c8-rep1 | 28257.3 | 0.32 GB | 27.0 MB | 21 | 1131 |
| streamed-obj-16MiB.bin-c8-rep2 | 26798.4 | 0.31 GB | 27.7 MB | 20 | 1130 |
| buffered-obj-16MiB.bin-c128-rep1 | 10977.9 | 59.18 GB | 795.5 MB | 34 | 1371 |
| buffered-obj-16MiB.bin-c128-rep2 | 11491.8 | 61.99 GB | 781.1 MB | 37 | 1610 |
| streamed-obj-16MiB.bin-c128-rep1 | 29664.7 | 0.34 GB | 1267.4 MB | 0 | 1610 |
| streamed-obj-16MiB.bin-c128-rep2 | 29323.6 | 0.34 GB | 57.2 MB | 11 | 1610 |
