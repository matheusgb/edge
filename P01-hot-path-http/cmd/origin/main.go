// Comando origin sobe o servidor de origem do lab.
//
// São dois servidores HTTP em portas diferentes, de propósito:
//
//   - a porta pública serve só os objetos;
//   - a porta administrativa serve /metrics e /debug/pprof.
//
// Manter pprof fora da porta pública não é preciosismo. O pprof expõe nomes de
// funções, memória e goroutines do processo, e o endpoint de profile por si só
// consome CPU. Servir isso na mesma porta que atende o tráfego significaria
// deixar qualquer cliente degradar a medição, além de vazar informação interna.
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

	"github.com/matheusgb/edge/p01-hot-path-http/internal/catalog"
	"github.com/matheusgb/edge/p01-hot-path-http/internal/metrics"
	"github.com/matheusgb/edge/p01-hot-path-http/internal/origin"
)

func main() {
	addr := flag.String("addr", ":8080", "endereço público")
	adminAddr := flag.String("admin-addr", ":8081", "endereço administrativo (metrics e pprof)")
	dir := flag.String("dir", "testdata/objects", "diretório do catálogo")
	modeFlag := flag.String("mode", "streamed", "modo padrão: buffered ou streamed")
	readHeaderTimeout := flag.Duration("read-header-timeout", 5*time.Second, "timeout para ler os headers")
	writeTimeout := flag.Duration("write-timeout", 120*time.Second, "timeout para escrever a resposta")
	idleTimeout := flag.Duration("idle-timeout", 90*time.Second, "timeout de conexão ociosa")
	shutdownTimeout := flag.Duration("shutdown-timeout", 15*time.Second, "prazo do encerramento gracioso")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	mode, err := origin.ParseMode(*modeFlag)
	if err != nil {
		logger.Error("flag -mode inválida", "err", err)
		os.Exit(1)
	}

	cat, err := catalog.Load(*dir)
	if err != nil {
		logger.Error("não consegui carregar o catálogo", "dir", *dir, "err", err)
		os.Exit(1)
	}

	m := metrics.New()
	server := origin.New(cat, m, logger, mode)

	public := &http.Server{
		Addr:    *addr,
		Handler: server.Handler(),
		// ReadHeaderTimeout protege contra cliente que abre conexão e não manda
		// nada (slowloris). É o timeout mais importante de um servidor exposto.
		ReadHeaderTimeout: *readHeaderTimeout,
		// WriteTimeout precisa caber a resposta INTEIRA, inclusive o objeto de
		// 16 MiB para um cliente lento. Curto demais e o servidor corta entregas
		// legítimas, o que apareceria na medição como erro do modo streamed.
		WriteTimeout: *writeTimeout,
		IdleTimeout:  *idleTimeout,
	}

	adminMux := http.NewServeMux()
	adminMux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))
	adminMux.HandleFunc("GET /debug/pprof/", pprof.Index)
	adminMux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	adminMux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	adminMux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	adminMux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)

	admin := &http.Server{
		Addr:              *adminAddr,
		Handler:           adminMux,
		ReadHeaderTimeout: *readHeaderTimeout,
	}

	// NotifyContext cancela o contexto no primeiro SIGINT ou SIGTERM. Um segundo
	// sinal volta ao comportamento padrão e mata o processo, assim um operador
	// impaciente consegue forçar a saída sem procurar o PID.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() { errCh <- listen(public, "público") }()
	go func() { errCh <- listen(admin, "admin") }()

	logger.Info("servidor no ar",
		"addr", *addr, "admin", *adminAddr,
		"mode", string(mode), "objetos", cat.Len(), "dir", *dir)

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("servidor caiu", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("sinal recebido, encerrando com calma", "timeout", shutdownTimeout.String())
	}

	// Shutdown para de aceitar conexões novas e espera as em andamento. É o que
	// diferencia "encerrar" de "derrubar": um deploy não corta download no meio.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
	defer cancel()

	var failed bool
	for name, srv := range map[string]*http.Server{"público": public, "admin": admin} {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("encerramento forçado", "servidor", name, "err", err)
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
	logger.Info("encerrado")
}

func listen(srv *http.Server, name string) error {
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	_ = name
	return nil
}
