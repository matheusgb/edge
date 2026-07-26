// Comando origin sobe a origem do P03.
//
// Duas portas, como nos projetos anteriores: a pública serve objetos, a
// administrativa serve /metrics, os contadores e os controles de degradação. A
// administrativa nunca é publicada para fora da máquina.
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

	"github.com/matheusgb/edge-lab/p03-roteamento-sob-congestionamento/internal/metrics"
	"github.com/matheusgb/edge-lab/p03-roteamento-sob-congestionamento/internal/originsrv"
)

func main() {
	addr := flag.String("addr", ":8080", "endereço público")
	adminAddr := flag.String("admin-addr", ":8090", "endereço administrativo (metrics e controles)")
	maxAge := flag.Duration("max-age", 30*time.Second, "Cache-Control max-age anunciado aos edges")
	latency := flag.Duration("latency", 0, "latência artificial por requisição")
	shutdownTimeout := flag.Duration("shutdown-timeout", 15*time.Second, "prazo do encerramento gracioso")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	m := metrics.NewOrigin()
	server := originsrv.New(m, logger, *maxAge, *latency)

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

	admin := &http.Server{Addr: *adminAddr, Handler: adminMux, ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() { errCh <- listen(public) }()
	go func() { errCh <- listen(admin) }()

	logger.Info("origem no ar", "addr", *addr, "admin", *adminAddr,
		"max_age", maxAge.String(), "latency", latency.String())

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
