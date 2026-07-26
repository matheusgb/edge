// Comando loadgen executa UM cenário de carga e imprime o resumo.
//
// Serve para inspeção rápida durante o desenvolvimento. Para a comparação do lab,
// use o comando matrix, que repete cada cenário e agrega o resultado.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/matheusgb/edge/p01-hot-path-http/internal/loadtest"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080", "endereço do servidor de origem")
	adminURL := flag.String("admin-url", "http://127.0.0.1:8081", "endereço administrativo, de onde vêm as métricas do servidor")
	object := flag.String("object", "obj-1MiB.bin", "objeto a requisitar")
	mode := flag.String("mode", "streamed", "modo: buffered ou streamed")
	concurrency := flag.Int("concurrency", 32, "clientes simultâneos")
	rate := flag.Int("rate", 0, "requisições por segundo; 0 = modelo fechado, limitado pela concorrência")
	duration := flag.Duration("duration", 10*time.Second, "janela de medição")
	warmup := flag.Duration("warmup", 2*time.Second, "aquecimento descartado da medição")
	timeout := flag.Duration("timeout", 30*time.Second, "timeout por requisição")
	asJSON := flag.Bool("json", false, "imprimir o resultado como JSON")
	flag.Parse()

	cfg := loadtest.Config{
		BaseURL:     *url,
		AdminURL:    *adminURL,
		Object:      *object,
		Mode:        *mode,
		Concurrency: *concurrency,
		Rate:        *rate,
		Duration:    *duration,
		Warmup:      *warmup,
		Timeout:     *timeout,
	}

	result, err := loadtest.Run(context.Background(), cfg)
	if err != nil {
		log.Fatalf("executando carga: %v", err)
	}

	if *asJSON {
		blob, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			log.Fatalf("serializando resultado: %v", err)
		}
		fmt.Println(string(blob))
		return
	}

	printResult(os.Stdout, result)
}

func printResult(w *os.File, r loadtest.Result) {
	fmt.Fprintf(w, "cenário         %s\n", r.Scenario)
	fmt.Fprintf(w, "janela          %.1fs\n", r.DurationSec)
	fmt.Fprintf(w, "carga oferecida %d (%.1f/s)\n", r.Offered, r.OfferedPerSec)
	fmt.Fprintf(w, "carga concluída %d (%.1f/s)\n", r.Completed, r.CompletedPerSec)
	fmt.Fprintf(w, "erros           %d (taxa %.2f%%) %v\n", r.Errors, r.ErrorRate*100, r.StatusCodes)
	fmt.Fprintf(w, "latência        p50 %.2fms  p95 %.2fms  p99 %.2fms  max %.2fms\n",
		r.P50Ms, r.P95Ms, r.P99Ms, r.MaxMs)
	fmt.Fprintf(w, "-- o servidor relatou --\n")
	fmt.Fprintf(w, "vazão           %.1f MB/s\n", r.ServerBytesPerSec/(1<<20))
	fmt.Fprintf(w, "alocado         %.2f GB na janela\n", r.ServerAllocBytes/1e9)
	fmt.Fprintf(w, "heap no fim     %.1f MB\n", r.ServerHeapInUse/1e6)
	fmt.Fprintf(w, "coletas         %.0f (número isolado engana, veja o README)\n", r.ServerGCCycles)
	fmt.Fprintf(w, "goroutines      %.0f no fim\n", r.ServerGoroutines)
	fmt.Fprintf(w, "cancelamentos   %.0f (cliente desistiu no meio)\n", r.ServerCancellations)
}
