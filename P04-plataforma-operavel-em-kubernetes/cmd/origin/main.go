// Command origin sobe o serviço de origem mínimo do P04.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/matheusgb/edge-lab/p04-plataforma-operavel-em-kubernetes/internal/httpx"
	"github.com/matheusgb/edge-lab/p04-plataforma-operavel-em-kubernetes/internal/metrics"
	"github.com/matheusgb/edge-lab/p04-plataforma-operavel-em-kubernetes/internal/originsrv"
)

func main() {
	addr := flag.String("addr", ":8080", "endereço público")
	adminAddr := flag.String("admin-addr", ":8090", "endereço administrativo (metrics, pprof)")
	shutdownTimeout := flag.Duration("shutdown-timeout", 15*time.Second, "tempo máximo para drenar requisições no shutdown")
	warmup := flag.Duration("warmup", 2*time.Second, "atraso de warmup antes de ficar pronto")
	failReady := flag.Bool("fail-ready", envBool("ORIGIN_FAIL_READY"), "nunca fica pronto, usado no experimento de rollout inválido")
	maxWork := flag.Int("max-work", 5_000_000, "máximo de iterações aceitas em /work")
	objectSize := flag.Int("object-size", 65536, "tamanho em bytes dos objetos sintéticos")
	instanceID := flag.String("instance-id", envOr("ORIGIN_INSTANCE_ID", hostnameOrDefault()), "identificador desta instância")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "origin", "instance", *instanceID)

	m := metrics.NewOrigin()
	var ready atomic.Bool

	cfg := originsrv.Config{
		InstanceID: *instanceID,
		Warmup:     *warmup,
		FailReady:  *failReady,
		MaxWork:    *maxWork,
		ObjectSize: *objectSize,
	}
	srv := originsrv.New(cfg, log, m, &ready)

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
		return "origin"
	}
	return h
}
