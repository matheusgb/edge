// Comando tokend sobe o serviço de assinatura e validação de URLs.
//
// Ele é o serviço que a CDN consulta a cada requisição, então precisa ser
// rápido e previsível. Por isso não tem banco, não tem cache e não faz chamada
// externa: a decisão sai de um HMAC sobre dados que já vieram na requisição.
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

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/matheusgb/edge/p02-cdn-signed-cache/internal/metrics"
	"github.com/matheusgb/edge/p02-cdn-signed-cache/internal/signer"
	"github.com/matheusgb/edge/p02-cdn-signed-cache/internal/tokensrv"
)

func main() {
	addr := flag.String("addr", ":8082", "endereço público do serviço de token")
	adminAddr := flag.String("admin-addr", ":8092", "endereço administrativo (metrics)")
	defaultTTL := flag.Duration("default-ttl", 60*time.Second, "validade padrão do token")
	maxTTL := flag.Duration("max-ttl", 5*time.Minute, "validade máxima aceita")
	shutdownTimeout := flag.Duration("shutdown-timeout", 15*time.Second, "prazo do encerramento gracioso")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// As chaves vêm do ambiente no formato "id:segredo,id:segredo". Duas chaves ao
	// mesmo tempo é o que permite rotacionar sem invalidar quem já tem um link.
	keys, err := signer.ParseKeyset(os.Getenv("CDN_ACTIVE_KEY"), os.Getenv("CDN_SIGNING_KEYS"))
	if err != nil {
		logger.Error("configuração de chaves inválida", "err", err)
		logger.Error("defina CDN_SIGNING_KEYS=\"k1:...,k2:...\" e CDN_ACTIVE_KEY=k2")
		os.Exit(1)
	}

	m := metrics.NewToken()
	server := tokensrv.New(keys, m, logger, tokensrv.Options{DefaultTTL: *defaultTTL, MaxTTL: *maxTTL})

	public := &http.Server{
		Addr:    *addr,
		Handler: server.Handler(),
		// Timeouts curtos: este serviço está no hot path da CDN. Uma
		// requisição de autorização que demora mais que isso já virou problema.
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	adminMux := http.NewServeMux()
	adminMux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))
	admin := &http.Server{Addr: *adminAddr, Handler: adminMux, ReadHeaderTimeout: 2 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() { errCh <- listen(public) }()
	go func() { errCh <- listen(admin) }()

	// As chaves aparecem no log pelo IDENTIFICADOR, nunca pelo valor.
	logger.Info("serviço de token no ar",
		"addr", *addr, "admin", *adminAddr,
		"chaves_aceitas", keys.KeyIDs(), "chave_ativa", keys.ActiveKeyID(),
		"default_ttl", defaultTTL.String(), "max_ttl", maxTTL.String())

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("serviço de token caiu", "err", err)
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
	logger.Info("encerrado")
}

func listen(srv *http.Server) error {
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
