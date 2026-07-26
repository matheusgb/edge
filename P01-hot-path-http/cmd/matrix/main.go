// Comando matrix executa a matriz de carga do lab e grava a evidência.
//
// A matriz é o produto (modos × objetos × concorrências), repetido N vezes.
// Medição confiável pede ao menos três repetições por cenário e que se mude uma
// variável relevante por comparação. Mudar duas ao mesmo tempo torna impossível
// atribuir a diferença a qualquer uma delas. Por isso a ordem de execução mantém
// tudo fixo e varia um eixo de cada vez.
//
// A repetição não existe para "pegar o melhor". Existe para mostrar dispersão: se
// as três rodadas de um cenário discordam muito entre si, a conclusão é frágil e
// o relatório precisa dizer isso, não esconder atrás de uma média.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/matheusgb/edge/p01-hot-path-http/internal/loadtest"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080", "endereço do servidor de origem")
	adminURL := flag.String("admin-url", "http://127.0.0.1:8081", "endereço administrativo, de onde vêm as métricas do servidor")
	modes := flag.String("modes", "buffered,streamed", "modos a comparar")
	objects := flag.String("objects", "obj-64KiB.bin,obj-1MiB.bin,obj-16MiB.bin", "objetos a comparar")
	concurrencies := flag.String("concurrency", "8,64,256", "níveis de concorrência")
	rate := flag.Int("rate", 0, "requisições por segundo; 0 = modelo fechado, limitado pela concorrência")
	repeats := flag.Int("repeats", 3, "repetições por cenário")
	duration := flag.Duration("duration", 10*time.Second, "janela de medição por rodada")
	warmup := flag.Duration("warmup", 2*time.Second, "aquecimento por rodada")
	timeout := flag.Duration("timeout", 30*time.Second, "timeout por requisição")
	settle := flag.Duration("settle", 2*time.Second, "pausa entre rodadas para o sistema assentar")
	evidenceDir := flag.String("evidence", "evidence", "raiz da pasta de evidência")
	scenario := flag.String("scenario", "matriz-buffered-vs-streamed", "nome do cenário")
	notes := flag.String("notes", "", "observações a incluir na evidência")
	flag.Parse()

	if *repeats < 1 {
		log.Fatalf("repeats deve ser ao menos 1, recebi %d", *repeats)
	}

	modeList := splitList(*modes)
	objectList := splitList(*objects)
	concList, err := splitInts(*concurrencies)
	if err != nil {
		log.Fatalf("concorrências inválidas: %v", err)
	}
	if len(modeList) == 0 || len(objectList) == 0 || len(concList) == 0 {
		log.Fatal("modes, objects e concurrency não podem ser vazios")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startedAt := time.Now()
	ev := loadtest.NewEvidence(*scenario, startedAt)
	ev.Notes = *notes

	total := len(modeList) * len(objectList) * len(concList) * *repeats
	estimate := time.Duration(total) * (*duration + *warmup + *settle)
	log.Printf("matriz: %d rodadas, ~%s de execução", total, estimate.Round(time.Second))

	done := 0
	for _, object := range objectList {
		for _, conc := range concList {
			for _, mode := range modeList {
				for rep := 1; rep <= *repeats; rep++ {
					if ctx.Err() != nil {
						log.Printf("interrompido após %d rodadas; gravando o que já foi medido", done)
						finish(ctx, ev, *evidenceDir, startedAt)
						return
					}

					cfg := loadtest.Config{
						BaseURL:     *url,
						AdminURL:    *adminURL,
						Object:      object,
						Mode:        mode,
						Concurrency: conc,
						Rate:        *rate,
						Duration:    *duration,
						Warmup:      *warmup,
						Timeout:     *timeout,
					}
					done++
					log.Printf("[%d/%d] modo=%s objeto=%s conc=%d rep=%d",
						done, total, mode, object, conc, rep)

					result, err := loadtest.Run(ctx, cfg)
					if err != nil {
						log.Printf("  rodada falhou: %v", err)
						continue
					}
					result.Scenario = fmt.Sprintf("%s-rep%d", result.Scenario, rep)
					ev.Results = append(ev.Results, result)
					ev.Commands = append(ev.Commands, commandFor(*url, cfg))

					log.Printf("  concluída=%.1f/s p99=%.2fms %.1fMB/s alocado=%.1fGB erro=%.2f%%",
						result.CompletedPerSec, result.P99Ms,
						result.ServerBytesPerSec/(1<<20), result.ServerAllocBytes/1e9,
						result.ErrorRate*100)

					// A pausa deixa o coletor de lixo terminar, as conexões em
					// TIME_WAIT drenarem e o cache de página assentar. Sem ela, a
					// rodada seguinte herda a bagunça da anterior.
					if *settle > 0 {
						select {
						case <-time.After(*settle):
						case <-ctx.Done():
						}
					}
				}
			}
		}
	}

	finish(ctx, ev, *evidenceDir, startedAt)
}

func finish(_ context.Context, ev loadtest.Evidence, evidenceDir string, startedAt time.Time) {
	if len(ev.Results) == 0 {
		log.Fatal("nenhuma rodada concluída: nada a gravar")
	}
	dir, err := loadtest.SaveEvidence(evidenceDir, ev)
	if err != nil {
		log.Fatalf("gravando evidência: %v", err)
	}
	log.Printf("evidência em %s (%d rodadas, %s no total)",
		dir, len(ev.Results), time.Since(startedAt).Round(time.Second))
}

// commandFor registra o comando equivalente àquela rodada, para que alguém possa
// reproduzir um cenário isolado sem reler o código da matriz.
func commandFor(url string, cfg loadtest.Config) string {
	return fmt.Sprintf(
		"go run ./cmd/loadgen -url %s -admin-url %s -object %s -mode %s -concurrency %d -rate %d -duration %s -warmup %s -timeout %s",
		url, cfg.AdminURL, cfg.Object, cfg.Mode, cfg.Concurrency, cfg.Rate, cfg.Duration, cfg.Warmup, cfg.Timeout)
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitInts(raw string) ([]int, error) {
	var out []int
	for _, part := range splitList(raw) {
		var n int
		if _, err := fmt.Sscanf(part, "%d", &n); err != nil {
			return nil, fmt.Errorf("%q não é um número: %w", part, err)
		}
		if n <= 0 {
			return nil, fmt.Errorf("concorrência deve ser positiva, recebi %d", n)
		}
		out = append(out, n)
	}
	return out, nil
}
