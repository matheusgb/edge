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

	"github.com/matheusgb/edge-lab/p01-hot-path-http/internal/loadtest"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080", "endereço do servidor de origem")
	object := flag.String("object", "obj-1MiB.bin", "objeto a requisitar")
	mode := flag.String("mode", "streamed", "modo: buffered ou streamed")
	concurrency := flag.Int("concurrency", 32, "clientes simultâneos")
	duration := flag.Duration("duration", 10*time.Second, "janela de medição")
	warmup := flag.Duration("warmup", 2*time.Second, "aquecimento descartado da medição")
	timeout := flag.Duration("timeout", 30*time.Second, "timeout por requisição")
	asJSON := flag.Bool("json", false, "imprimir o resultado como JSON")
	flag.Parse()

	cfg := loadtest.Config{
		BaseURL:     *url,
		Object:      *object,
		Mode:        *mode,
		Concurrency: *concurrency,
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
	fmt.Fprintf(w, "erros           %d (timeouts %d, taxa %.2f%%)\n", r.Errors, r.Timeouts, r.ErrorRate*100)
	fmt.Fprintf(w, "cancelados      %d (fim da janela, não é falha do servidor)\n", r.Cancelled)
	fmt.Fprintf(w, "vazão           %.1f MB/s\n", r.BytesPerSec/(1<<20))
	fmt.Fprintf(w, "latência        p50 %.2fms  p95 %.2fms  p99 %.2fms  max %.2fms\n",
		r.P50Ms, r.P95Ms, r.P99Ms, r.MaxMs)
}
