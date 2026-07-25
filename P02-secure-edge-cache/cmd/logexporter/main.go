// Comando logexporter segue o log de acesso do Nginx e expõe /metrics.
//
// O Nginx de código aberto não tem endpoint de estatística de cache. Ele sabe se
// cada resposta foi HIT, MISS, EXPIRED, STALE ou BYPASS e escreve isso no log,
// então o caminho mais curto para ter hit ratio medido é ler o log.
//
// Seguir arquivo parece trivial e não é: tem rotação, truncamento, linha
// incompleta e reabertura por inode. Isso é problema resolvido, e aqui é a
// biblioteca nxadm/tail que resolve. O que é do projeto, transformar linha em
// métrica com os rótulos certos, está no pacote logexport.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nxadm/tail"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/matheusgb/edge-lab/p02-secure-edge-cache/internal/logexport"
)

func main() {
	logPath := flag.String("log", "/var/log/nginx/access.json", "log de acesso do Nginx em JSON")
	addr := flag.String("addr", ":8093", "endereço onde expor /metrics")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	collector := logexport.NewCollector()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(collector.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	server := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("servidor de métricas caiu", "err", err)
			stop()
		}
	}()

	// ReOpen trata o arquivo sendo rotacionado; Poll evita depender de inotify,
	// que não funciona de forma confiável em volume montado de container.
	// Location nil significa "do começo do arquivo": o exporter sobe junto com a
	// borda, e perder as primeiras linhas estragaria a contagem do experimento.
	follower, err := tail.TailFile(*logPath, tail.Config{
		Follow:    true,
		ReOpen:    true,
		Poll:      true,
		MustExist: false,
		Logger:    tail.DiscardingLogger,
	})
	if err != nil {
		logger.Error("não consegui seguir o log", "path", *logPath, "err", err)
		os.Exit(1)
	}

	logger.Info("exportador no ar", "log", *logPath, "addr", *addr)

	go func() {
		<-ctx.Done()
		_ = follower.Stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	for line := range follower.Lines {
		if line.Err != nil {
			logger.Warn("erro lendo o log", "err", line.Err)
			continue
		}
		collector.Feed([]byte(line.Text))
	}

	logger.Info("encerrado")
}
