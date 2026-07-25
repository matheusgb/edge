// Comando origin sobe o servidor de origem do P02.
//
// Duas portas, pelo mesmo motivo do P01: a pública serve objetos, a
// administrativa serve /metrics, os contadores e os controles de degradação. A
// porta administrativa nunca é publicada para fora da máquina.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/matheusgb/edge-lab/p02-secure-edge-cache/internal/metrics"
	"github.com/matheusgb/edge-lab/p02-secure-edge-cache/internal/originsrv"
)

func main() {
	addr := flag.String("addr", ":8080", "endereço público")
	adminAddr := flag.String("admin-addr", ":8090", "endereço administrativo (metrics e controles)")
	maxAge := flag.Duration("max-age", 60*time.Second, "Cache-Control max-age das respostas")
	latency := flag.Duration("latency", 0, "latência artificial por requisição")
	shutdownTimeout := flag.Duration("shutdown-timeout", 15*time.Second, "prazo do encerramento gracioso")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// O segredo compartilhado chega por ambiente, nunca por flag. Flag aparece em
	// `ps`, em log de shell e em arquivo de histórico; variável de ambiente do
	// processo é bem menos exposta.
	secret := os.Getenv("EDGE_ORIGIN_SECRET")
	if secret == "" {
		logger.Warn("EDGE_ORIGIN_SECRET vazio: a origem vai aceitar qualquer chamada direta")
	}

	m := metrics.NewOrigin()
	server := originsrv.New(m, logger, secret, *maxAge, *latency)

	public := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	adminMux := http.NewServeMux()
	adminMux.Handle("/", server.AdminHandler(promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})))
	adminMux.HandleFunc("GET /debug/pprof/", pprof.Index)
	adminMux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	adminMux.HandleFunc("GET /debug/pprof/heap", pprof.Index)

	admin := &http.Server{
		Addr:              *adminAddr,
		Handler:           adminMux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() { errCh <- listen(public) }()
	go func() { errCh <- listen(admin) }()

	logger.Info("origem no ar",
		"addr", *addr, "admin", *adminAddr,
		"max_age", maxAge.String(), "latency", latency.String(),
		"segredo_configurado", secret != "")

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("origem caiu", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("sinal recebido, encerrando com calma", "timeout", shutdownTimeout.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
	defer cancel()
	for _, srv := range []*http.Server{public, admin} {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("encerramento forçado", "addr", srv.Addr, "err", err)
		}
	}
	logger.Info("encerrada")
}

func listen(srv *http.Server) error {
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
