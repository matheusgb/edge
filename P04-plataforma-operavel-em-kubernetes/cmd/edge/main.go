// Command edge sobe a CDN mínima do P04.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/matheusgb/edge/p04-plataforma-operavel-em-kubernetes/internal/edgesrv"
	"github.com/matheusgb/edge/p04-plataforma-operavel-em-kubernetes/internal/httpx"
	"github.com/matheusgb/edge/p04-plataforma-operavel-em-kubernetes/internal/metrics"
)

func main() {
	addr := flag.String("addr", ":8080", "endereço público")
	adminAddr := flag.String("admin-addr", ":8090", "endereço administrativo (metrics, pprof)")
	shutdownTimeout := flag.Duration("shutdown-timeout", 15*time.Second, "tempo máximo para drenar requisições no shutdown")
	warmup := flag.Duration("warmup", 2*time.Second, "atraso de warmup antes de ficar pronto")
	failReady := flag.Bool("fail-ready", envBool("EDGE_FAIL_READY"), "nunca fica pronto, usado no experimento de rollout inválido")
	originURL := flag.String("origin-url", envOr("EDGE_ORIGIN_URL", "http://origin:8080"), "URL base da origem")
	cacheTTL := flag.Duration("cache-ttl", 5*time.Second, "TTL do cache em memória para /object/{name}")
	instanceID := flag.String("instance-id", envOr("EDGE_INSTANCE_ID", hostnameOrDefault()), "identificador desta instância")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "edge", "instance", *instanceID)

	m := metrics.NewEdge()
	var ready atomic.Bool

	cfg := edgesrv.Config{
		InstanceID: *instanceID,
		OriginURL:  *originURL,
		CacheTTL:   *cacheTTL,
		Warmup:     *warmup,
		FailReady:  *failReady,
	}
	srv := edgesrv.New(cfg, log, m, &ready)

	pair := httpx.New(log, *addr, *adminAddr, srv.Handler(), srv.AdminHandler(promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})), *shutdownTimeout, &ready)

	if err := pair.Run(context.Background()); err != nil {
		log.Error("encerrado com erro", "erro", err)
		os.Exit(1)
	}
	log.Info("encerrado")
}

func envBool(key string) bool {
	v, ok := os.LookupEnv(key)
	return ok && (v == "1" || v == "true" || v == "TRUE")
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func hostnameOrDefault() string {
	h, err := os.Hostname()
	if err != nil {
		return "edge"
	}
	return h
}
