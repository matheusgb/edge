# Resumo: matriz-buffered-vs-streamed

Medição local em linux/amd64 com 32 CPUs. Isto NÃO é capacidade de produção:
gerador e servidor dividem a mesma máquina e disputam CPU entre si.

| Cenário | Modo | Objeto | Conc. | Oferecida/s | Concluída/s | p50 ms | p95 ms | p99 ms | MB/s | Erro |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| buffered-obj-64KiB.bin-c8-rep1 | buffered | obj-64KiB.bin | 8 | 23017.6 | 23014.9 | 0.32 | 0.69 | 0.89 | 1438.4 | 0.00% |
| buffered-obj-64KiB.bin-c8-rep2 | buffered | obj-64KiB.bin | 8 | 25087.3 | 25084.6 | 0.27 | 0.68 | 0.88 | 1567.8 | 0.00% |
| streamed-obj-64KiB.bin-c8-rep1 | streamed | obj-64KiB.bin | 8 | 28832.3 | 28830.3 | 0.22 | 0.60 | 0.79 | 1801.9 | 0.00% |
| streamed-obj-64KiB.bin-c8-rep2 | streamed | obj-64KiB.bin | 8 | 28126.4 | 28123.7 | 0.23 | 0.63 | 0.83 | 1757.7 | 0.00% |
| buffered-obj-64KiB.bin-c128-rep1 | buffered | obj-64KiB.bin | 128 | 39138.6 | 39104.3 | 1.20 | 13.18 | 20.87 | 2444.0 | 0.00% |
| buffered-obj-64KiB.bin-c128-rep2 | buffered | obj-64KiB.bin | 128 | 39783.6 | 39741.3 | 1.22 | 13.01 | 20.54 | 2483.8 | 0.00% |
| streamed-obj-64KiB.bin-c128-rep1 | streamed | obj-64KiB.bin | 128 | 52145.1 | 52105.2 | 1.11 | 9.09 | 14.04 | 3256.6 | 0.00% |
| streamed-obj-64KiB.bin-c128-rep2 | streamed | obj-64KiB.bin | 128 | 51276.5 | 51247.8 | 1.15 | 9.21 | 13.85 | 3203.0 | 0.00% |
| buffered-obj-16MiB.bin-c8-rep1 | buffered | obj-16MiB.bin | 8 | 662.3 | 659.6 | 11.86 | 16.05 | 18.23 | 10560.8 | 0.00% |
| buffered-obj-16MiB.bin-c8-rep2 | buffered | obj-16MiB.bin | 8 | 658.3 | 655.6 | 11.89 | 16.05 | 19.17 | 10497.0 | 0.00% |
| streamed-obj-16MiB.bin-c8-rep1 | streamed | obj-16MiB.bin | 8 | 1650.6 | 1647.9 | 4.70 | 6.56 | 7.30 | 26383.1 | 0.00% |
| streamed-obj-16MiB.bin-c8-rep2 | streamed | obj-16MiB.bin | 8 | 1621.6 | 1618.9 | 4.79 | 6.68 | 7.57 | 25930.8 | 0.00% |
| buffered-obj-16MiB.bin-c128-rep1 | buffered | obj-16MiB.bin | 128 | 691.3 | 648.7 | 86.18 | 561.65 | 798.68 | 10452.0 | 0.00% |
| buffered-obj-16MiB.bin-c128-rep2 | buffered | obj-16MiB.bin | 128 | 678.6 | 636.0 | 94.66 | 575.83 | 685.71 | 10286.0 | 0.00% |
| streamed-obj-16MiB.bin-c128-rep1 | streamed | obj-16MiB.bin | 128 | 1828.6 | 1786.0 | 63.08 | 151.47 | 193.40 | 28805.0 | 0.00% |
| streamed-obj-16MiB.bin-c128-rep2 | streamed | obj-16MiB.bin | 128 | 1795.7 | 1753.7 | 61.40 | 160.23 | 197.28 | 28266.9 | 0.00% |
