// Package httpx implementa o padrão de dois servidores (público e
// administrativo) e o shutdown gracioso usado por origin e edge.
package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

// Pair representa o par de servidores HTTP de um serviço: um público,
// que atende tráfego de usuário, e um administrativo, que expõe
// /metrics e pprof e nunca é publicado para fora do cluster.
type Pair struct {
	Public *http.Server
	Admin  *http.Server

	// Ready é o mesmo *atomic.Bool usado pelo serviço (originsrv/edgesrv)
	// para controlar /readyz. É compartilhado, não copiado: o warmup do
	// serviço o liga, e o shutdown gracioso o desliga antes de drenar
	// requisições em curso.
	Ready *atomic.Bool

	log             *slog.Logger
	shutdownTimeout time.Duration
}

// New cria o par de servidores com os endereços informados. ready é
// compartilhado com o serviço que constrói publicHandler/adminHandler.
func New(log *slog.Logger, publicAddr, adminAddr string, publicHandler, adminHandler http.Handler, shutdownTimeout time.Duration, ready *atomic.Bool) *Pair {
	return &Pair{
		Ready: ready,
		Public: &http.Server{
			Addr:              publicAddr,
			Handler:           publicHandler,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       90 * time.Second,
		},
		Admin: &http.Server{
			Addr:              adminAddr,
			Handler:           adminHandler,
			ReadHeaderTimeout: 5 * time.Second,
		},
		log:             log,
		shutdownTimeout: shutdownTimeout,
	}
}

// Run inicia os dois servidores e bloqueia até receber SIGINT/SIGTERM ou
// até um dos servidores falhar. No encerramento, derruba /readyz
// imediatamente e drena requisições em curso até shutdownTimeout.
func (p *Pair) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() {
		p.log.Info("servidor público no ar", "addr", p.Public.Addr)
		if err := p.Public.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	go func() {
		p.log.Info("servidor administrativo no ar", "addr", p.Admin.Addr)
		if err := p.Admin.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		p.log.Info("sinal de encerramento recebido, iniciando shutdown gracioso")
	case err := <-errCh:
		p.log.Error("servidor falhou, iniciando shutdown gracioso", "erro", err)
	}

	// Deixa de aceitar tráfego novo antes de drenar o que está em curso.
	p.Ready.Store(false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), p.shutdownTimeout)
	defer cancel()

	var shutdownErr error
	for _, srv := range []*http.Server{p.Public, p.Admin} {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			shutdownErr = err
		}
	}
	return shutdownErr
}
