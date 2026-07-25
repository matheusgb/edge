# Resumo: matriz-buffered-vs-streamed

Medição local em linux/amd64 com 32 CPUs. Isto NÃO é capacidade de produção:
gerador e servidor dividem a mesma máquina e disputam CPU entre si.

| Cenário | Modo | Objeto | Conc. | Oferecida/s | Concluída/s | p50 ms | p95 ms | p99 ms | MB/s | Erro |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| buffered-obj-64KiB.bin-c8-rep1 | buffered | obj-64KiB.bin | 8 | 14919.7 | 14917.7 | 0.50 | 1.04 | 1.37 | 932.4 | 0.00% |
| buffered-obj-64KiB.bin-c8-rep2 | buffered | obj-64KiB.bin | 8 | 16907.3 | 16905.0 | 0.41 | 0.97 | 1.25 | 1056.6 | 0.00% |
| streamed-obj-64KiB.bin-c8-rep1 | streamed | obj-64KiB.bin | 8 | 19528.4 | 19527.8 | 0.34 | 0.87 | 1.12 | 1220.5 | 0.00% |
| streamed-obj-64KiB.bin-c8-rep2 | streamed | obj-64KiB.bin | 8 | 19839.9 | 19837.5 | 0.33 | 0.86 | 1.10 | 1239.9 | 0.00% |
| buffered-obj-64KiB.bin-c128-rep1 | buffered | obj-64KiB.bin | 128 | 26884.7 | 26843.1 | 2.14 | 17.68 | 27.58 | 1677.7 | 0.00% |
| buffered-obj-64KiB.bin-c128-rep2 | buffered | obj-64KiB.bin | 128 | 27800.8 | 27758.5 | 2.02 | 17.70 | 28.33 | 1734.9 | 0.00% |
| streamed-obj-64KiB.bin-c128-rep1 | streamed | obj-64KiB.bin | 128 | 36304.4 | 36261.8 | 1.95 | 12.03 | 18.73 | 2266.4 | 0.00% |
| streamed-obj-64KiB.bin-c128-rep2 | streamed | obj-64KiB.bin | 128 | 35779.9 | 35751.0 | 1.89 | 12.46 | 19.93 | 2234.4 | 0.00% |
| buffered-obj-16MiB.bin-c8-rep1 | buffered | obj-16MiB.bin | 8 | 584.6 | 582.0 | 13.30 | 18.10 | 21.58 | 9330.8 | 0.00% |
| buffered-obj-16MiB.bin-c8-rep2 | buffered | obj-16MiB.bin | 8 | 557.0 | 554.3 | 13.79 | 19.52 | 23.27 | 8886.6 | 0.00% |
| streamed-obj-16MiB.bin-c8-rep1 | streamed | obj-16MiB.bin | 8 | 1188.3 | 1185.6 | 6.51 | 9.27 | 10.51 | 18986.9 | 0.00% |
| streamed-obj-16MiB.bin-c8-rep2 | streamed | obj-16MiB.bin | 8 | 1232.6 | 1229.9 | 6.32 | 8.96 | 9.94 | 19700.1 | 0.00% |
| buffered-obj-16MiB.bin-c128-rep1 | buffered | obj-16MiB.bin | 128 | 687.9 | 645.8 | 87.12 | 517.09 | 710.63 | 10419.5 | 0.00% |
| buffered-obj-16MiB.bin-c128-rep2 | buffered | obj-16MiB.bin | 128 | 618.0 | 576.5 | 94.97 | 579.94 | 750.98 | 9287.0 | 0.00% |
| streamed-obj-16MiB.bin-c128-rep1 | streamed | obj-16MiB.bin | 128 | 1521.5 | 1480.2 | 63.16 | 228.85 | 284.29 | 23752.3 | 0.00% |
| streamed-obj-16MiB.bin-c128-rep2 | streamed | obj-16MiB.bin | 128 | 1393.8 | 1351.8 | 44.32 | 311.67 | 378.58 | 21715.2 | 0.00% |
