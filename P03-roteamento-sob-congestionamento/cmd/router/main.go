// Comando router sobe o roteador do P03.
//
// Ele é o único endereço que o cliente conhece, e é onde todas as decisões deste
// projeto acontecem: para qual edge mandar, quanto tempo esperar, se vale tentar
// de novo e quando recusar em vez de acumular.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/metrics"
	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/obs"
	"github.com/matheusgb/edge/p03-roteamento-sob-congestionamento/internal/router"
)

func main() {
	addr := flag.String("addr", ":8080", "endereço público")
	adminAddr := flag.String("admin-addr", ":8090", "endereço administrativo")
	destinos := flag.String("backends", "", "destinos no formato nome=url,nome=url")
	politica := flag.String("strategy", "adaptativa", "política inicial: round-robin ou adaptativa")

	attemptTimeout := flag.Duration("attempt-timeout", 800*time.Millisecond, "prazo de UMA tentativa")
	requestTimeout := flag.Duration("request-timeout", 2*time.Second, "prazo total da requisição do cliente")
	maxAttempts := flag.Int("max-attempts", 2, "tentativas por requisição, incluindo a primeira")
	minSlice := flag.Duration("min-slice", 50*time.Millisecond, "resto de prazo abaixo do qual não vale mais tentar")

	maxInflight := flag.Int("max-inflight", 256, "teto de requisições simultâneas")
	retryAfter := flag.Duration("retry-after", 1*time.Second, "espera sugerida a quem for recusado")
	retryRatio := flag.Float64("retry-ratio", 0.1, "fração de retry permitida no regime permanente")
	retryBudget := flag.Float64("retry-budget", 100, "tamanho do balde de fichas de retry")

	ewmaAlpha := flag.Float64("ewma-alpha", 0.2, "peso da amostra nova na média móvel")
	failsToOpen := flag.Int64("fails-to-open", 5, "falhas seguidas que abrem o disjuntor")
	openFor := flag.Duration("open-for", 2*time.Second, "quanto tempo o disjuntor fica aberto")
	errorPenalty := flag.Float64("error-penalty", 10, "peso da taxa de erro no custo do destino")
	agingWindow := flag.Duration("aging-window", 1*time.Second, "janela de envelhecimento da informação sobre um destino parado")

	otlp := flag.String("otlp-endpoint", os.Getenv("OTLP_ENDPOINT"), "host:porta do coletor OTLP; vazio desliga traces")
	amostra := flag.Float64("trace-sample", 1.0, "fração de traces amostrados")
	shutdownTimeout := flag.Duration("shutdown-timeout", 15*time.Second, "prazo do encerramento gracioso")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("service", "router")

	backends, err := parseBackends(*destinos)
	if err != nil {
		logger.Error("lista de destinos inválida", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tracer, encerraTraces, err := obs.Tracing(ctx, *otlp, "router", *amostra)
	if err != nil {
		logger.Error("traces não puderam ser ligados", "err", err)
		os.Exit(1)
	}

	cfg := router.Config{
		EWMAAlpha:      *ewmaAlpha,
		FailuresToOpen: *failsToOpen,
		OpenFor:        *openFor,
		ErrorPenalty:   *errorPenalty,
		AgingWindow:    *agingWindow,
	}

	var estrategia router.Strategy
	switch *politica {
	case "round-robin":
		estrategia = router.NewRoundRobin()
	case "adaptativa":
		estrategia = router.NewAdaptive(cfg)
	default:
		logger.Error("política desconhecida", "strategy", *politica)
		os.Exit(1)
	}

	m := metrics.NewRouter()

	// O cliente dos destinos tem pool generoso e sem timeout próprio: quem manda
	// no prazo é o contexto de cada tentativa, que já conhece o prazo do cliente.
	// Um Timeout aqui competiria com aquele e produziria dois prazos concorrentes
	// para a mesma requisição.
	cliente := &http.Client{
		Transport: otelhttp.NewTransport(&http.Transport{
			DialContext:         (&net.Dialer{Timeout: 1 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:        1024,
			MaxIdleConnsPerHost: 512,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
		}),
		// Redirecionamento não faz sentido entre roteador e edge, e seguir um
		// esconderia um erro de configuração atrás de uma resposta que funciona.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	server := router.New(router.Options{
		Backends:       backends,
		Strategy:       estrategia,
		Config:         cfg,
		Budget:         router.NewRetryBudget(*retryRatio, *retryBudget),
		Limiter:        router.NewLimiter(*maxInflight, *retryAfter),
		AttemptTimeout: *attemptTimeout,
		RequestTimeout: *requestTimeout,
		MaxAttempts:    *maxAttempts,
		MinSliceLeft:   *minSlice,
		Client:         cliente,
		Logger:         logger,
		Metrics:        m,
		Tracer:         tracer,
	})

	// As gauges são publicadas por amostragem, fora do caminho quente.
	go server.SampleGauges(ctx, 250*time.Millisecond)

	public := &http.Server{
		Addr:              *addr,
		Handler:           otelhttp.NewHandler(server.Handler(), "router"),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	adminMux := http.NewServeMux()
	adminMux.Handle("/", server.AdminHandler(promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})))
	adminMux.HandleFunc("GET /debug/pprof/", pprof.Index)
	adminMux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	// O perfil de goroutines é o instrumento do cenário de saturação: ele mostra
	// onde as requisições estão paradas, e não só quantas são.
	adminMux.HandleFunc("GET /debug/pprof/goroutine", pprof.Index)

	admin := &http.Server{Addr: *adminAddr, Handler: adminMux, ReadHeaderTimeout: 5 * time.Second}

	errCh := make(chan error, 2)
	go func() { errCh <- listen(public) }()
	go func() { errCh <- listen(admin) }()

	logger.Info("roteador no ar",
		"addr", *addr, "admin", *adminAddr,
		"strategy", estrategia.Name(),
		"backends", len(backends),
		"attempt_timeout", attemptTimeout.String(),
		"request_timeout", requestTimeout.String(),
		"max_attempts", *maxAttempts,
		"max_inflight", *maxInflight)

	select {
	case err := <-errCh:
		if err != nil {
			logger.Error("roteador caiu", "err", err)
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

// parseBackends lê a lista "nome=url,nome=url".
func parseBackends(raw string) ([]*router.Backend, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("informe -backends nome=url,nome=url")
	}
	var out []*router.Backend
	for _, item := range strings.Split(raw, ",") {
		nome, url, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok || nome == "" || url == "" {
			return nil, fmt.Errorf("item inválido %q: use nome=url", item)
		}
		// O palpite inicial de latência é igual para todos, para que a primeira
		// rodada de decisões saia distribuída em vez de eleger um favorito por
		// acidente de inicialização.
		out = append(out, router.NewBackend(nome, strings.TrimSuffix(url, "/"), 5*time.Millisecond))
	}
	return out, nil
}

func listen(srv *http.Server) error {
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
