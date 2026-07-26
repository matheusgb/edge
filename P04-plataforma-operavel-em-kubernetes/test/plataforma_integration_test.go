// Teste de integração: sobe origin e edge como processos HTTP reais
// (não httptest.Server em memória) e exercita o caminho completo:
// cache hit/miss na borda, passthrough de /work, probes e shutdown
// gracioso drenando uma requisição em andamento.
package test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matheusgb/edge/p04-plataforma-operavel-em-kubernetes/internal/edgesrv"
	"github.com/matheusgb/edge/p04-plataforma-operavel-em-kubernetes/internal/httpx"
	"github.com/matheusgb/edge/p04-plataforma-operavel-em-kubernetes/internal/metrics"
	"github.com/matheusgb/edge/p04-plataforma-operavel-em-kubernetes/internal/originsrv"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startOrigin(t *testing.T) (publicAddr string, stop func()) {
	t.Helper()
	pubPort := freePort(t)
	adminPort := freePort(t)
	pub := fmt.Sprintf("127.0.0.1:%d", pubPort)
	admin := fmt.Sprintf("127.0.0.1:%d", adminPort)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.NewOrigin()
	var ready atomic.Bool
	srv := originsrv.New(originsrv.Config{Warmup: time.Millisecond, MaxWork: 100_000, ObjectSize: 512}, log, m, &ready)
	pair := httpx.New(log, pub, admin, srv.Handler(), srv.AdminHandler(promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})), 5*time.Second, &ready)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pair.Run(ctx)
		close(done)
	}()

	waitReady(t, "http://"+pub+"/", admin)
	return "http://" + pub, func() { cancel(); <-done }
}

func startEdge(t *testing.T, originURL string) (publicAddr string, stop func()) {
	t.Helper()
	pubPort := freePort(t)
	adminPort := freePort(t)
	pub := fmt.Sprintf("127.0.0.1:%d", pubPort)
	admin := fmt.Sprintf("127.0.0.1:%d", adminPort)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := metrics.NewEdge()
	var ready atomic.Bool
	srv := edgesrv.New(edgesrv.Config{OriginURL: originURL, CacheTTL: time.Minute, Warmup: time.Millisecond}, log, m, &ready)
	pair := httpx.New(log, pub, admin, srv.Handler(), srv.AdminHandler(promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})), 5*time.Second, &ready)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pair.Run(ctx)
		close(done)
	}()

	waitReady(t, "http://"+pub+"/", admin)
	return "http://" + pub, func() { cancel(); <-done }
}

func waitReady(t *testing.T, _ string, adminAddr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + adminAddr + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("serviço não ficou pronto a tempo")
}

func TestIntegracaoOrigemBordaCacheECache(t *testing.T) {
	originURL, stopOrigin := startOrigin(t)
	defer stopOrigin()
	edgeURL, stopEdge := startEdge(t, originURL)
	defer stopEdge()

	resp1, err := http.Get(edgeURL + "/object/foo.bin")
	if err != nil {
		t.Fatal(err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.Header.Get("X-Cache") != "MISS" {
		t.Fatalf("esperava MISS na primeira chamada, obteve %s", resp1.Header.Get("X-Cache"))
	}

	resp2, err := http.Get(edgeURL + "/object/foo.bin")
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.Header.Get("X-Cache") != "HIT" {
		t.Fatalf("esperava HIT na segunda chamada, obteve %s", resp2.Header.Get("X-Cache"))
	}
	if string(body1) != string(body2) {
		t.Fatal("corpo servido do cache deveria ser idêntico ao original")
	}
}

func TestIntegracaoWorkPassaPelaBordaSemCache(t *testing.T) {
	originURL, stopOrigin := startOrigin(t)
	defer stopOrigin()
	edgeURL, stopEdge := startEdge(t, originURL)
	defer stopEdge()

	resp, err := http.Get(edgeURL + "/work?n=100")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("esperava 200, obteve %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Cache"); got != "BYPASS" {
		t.Fatalf("esperava BYPASS em /work através da borda, obteve %s", got)
	}
}

func TestIntegracaoShutdownGraciosoDrenaRequisicaoEmCurso(t *testing.T) {
	originURL, stopOrigin := startOrigin(t)
	defer stopOrigin()

	// Uma requisição de trabalho razoavelmente longa começa antes do
	// shutdown; o servidor deve terminar de atendê-la, não cortá-la.
	resultCh := make(chan int, 1)
	go func() {
		resp, err := http.Get(originURL + "/work?n=3000000")
		if err != nil {
			resultCh <- -1
			return
		}
		defer resp.Body.Close()
		resultCh <- resp.StatusCode
	}()

	time.Sleep(20 * time.Millisecond)
	stopOrigin() // aciona o shutdown gracioso enquanto a requisição acima está em curso

	select {
	case status := <-resultCh:
		if status != http.StatusOK {
			t.Fatalf("esperava a requisição em curso terminar com 200, obteve %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("requisição em curso não terminou a tempo do shutdown gracioso")
	}
}
