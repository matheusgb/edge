// Comando edge sobe um servidor de borda do P03.
//
// Os três edges do lab são o mesmo binário com nomes e portas diferentes. Eles
// não se conhecem e não compartilham cache: cada um aprende sozinho o que já foi
// pedido a ele, que é o que dá peso à decisão do roteador.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/edgesrv"
	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/metrics"
	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/obs"
)

func main() {
	nome := flag.String("name", "edge", "nome deste edge, usado em métrica e log")
	addr := flag.String("addr", ":8080", "endereço público")
	adminAddr := flag.String("admin-addr", ":8090", "endereço administrativo")
	origem := flag.String("origin", "http://origin:8080", "endereço da origem")
	capacidade := flag.Int("cache-entries", 64, "quantos objetos cabem no cache")
	ttl := flag.Duration("cache-ttl", 30*time.Second, "TTL usado quando a origem não declarar max-age")
	originTimeout := flag.Duration("origin-timeout", 3*time.Second, "prazo máximo de uma busca à origem")
	otlp := flag.String("otlp-endpoint", os.Getenv("OTLP_ENDPOINT"), "host:porta do coletor OTLP; vazio desliga traces")
	amostra := flag.Float64("trace-sample", 1.0, "fração de traces amostrados")
	shutdownTimeout := flag.Duration("shutdown-timeout", 15*time.Second, "prazo do encerramento gracioso")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("service", *nome)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	_, encerraTraces, err := obs.Tracing(ctx, *otlp, *nome, *amostra)
	if err != nil {
		logger.Error("traces não puderam ser ligados", "err", err)
		os.Exit(1)
	}

	m := metrics.NewEdge()

	// O cliente da origem tem pool próprio e timeout próprio. O prazo propagado
	// pelo roteador ainda manda quando for mais curto; este é o teto de quando
	// não houver prazo nenhum.
	cliente := &http.Client{
		Timeout: *originTimeout,
		Transport: otelhttp.NewTransport(&http.Transport{
			DialContext:         (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 256,
			IdleConnTimeout:     90 * time.Second,
			// Sem compressão: o conteúdo é pseudoaleatório e não comprime, então
			// gzip aqui só gastaria CPU dos dois lados para produzir o mesmo
			// tamanho.
			DisableCompression: true,
		}),
	}

	server, err := edgesrv.New(*nome, *origem, *capacidade, *ttl, cliente, m, logger)
	if err != nil {
		logger.Error("edge não subiu", "err", err)
		os.Exit(1)
	}

	public := &http.Server{
		Addr:              *addr,
		Handler:           otelhttp.NewHandler(server.Handler(), "edge"),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	adminMux := http.NewServeMux()
	adminMux.Handle("/", server.AdminHandler(promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})))
	adminMux.HandleFunc("GET /debug/pprof/", pprof.Index)
	adminMux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)

	admin := &http.Server{Addr: *adminAddr, Handler: adminMux, ReadHeaderTimeout: 5 * time.Second}

	errCh := make(chan error, 2)
	go func() { errCh <- listen(public) }()
	go func() { errCh <- listen(admin) }()

	logger.Info("edge no ar", "addr", *addr, "admin", *adminAddr,
		"origin", *origem, "cache_entries", *capacidade, "cache_ttl", ttl.String())

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("edge caiu", "err", err)
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
	if err := encerraTraces(shutdownCtx); err != nil {
		logger.Warn("traces pendentes não foram enviados", "err", err)
	}
	logger.Info("encerrado")
}

func listen(srv *http.Server) error {
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
