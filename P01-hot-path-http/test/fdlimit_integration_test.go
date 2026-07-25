//go:build integration

// Teste de integração da falha controlada: esgotar os descritores de arquivo.
//
// Este é o experimento que os testes unitários não conseguem fazer. Um servidor
// não quebra só por estar lento; ele quebra por acabar um recurso. E o recurso
// mais fácil de acabar num servidor HTTP é o descritor de arquivo, porque cada
// conexão aberta consome um.
//
// Na máquina de desenvolvimento o limite é de mais de um milhão, folgado demais
// para esgotar por acidente. Por isso o experimento precisa de um container
// descartável com o limite baixado de propósito, e é o testcontainers que sobe,
// espera e derruba esse container.
//
// O que o teste prova: sob mais clientes do que descritores disponíveis, a carga
// OFERECIDA continua alta e a CONCLUÍDA despenca. A diferença entre as duas é
// exatamente o que o servidor não conseguiu aceitar. Um relatório que contasse só
// o que deu certo mostraria um servidor saudável.
//
//	docker build -t p01-origin .
//	go test -tags=integration ./test/... -v
package test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/matheusgb/edge-lab/p01-hot-path-http/internal/catalog"
	"github.com/matheusgb/edge-lab/p01-hot-path-http/internal/loadtest"
)

// limiteDeDescritores é baixo o bastante para o problema aparecer em segundos.
// O servidor precisa de descritores para as conexões aceitas, para os arquivos
// abertos no modo streamed e para os sockets de escuta.
const limiteDeDescritores = 64

// subirOrigem sobe o servidor num container com o limite de descritores pedido.
func subirOrigem(ctx context.Context, t *testing.T, nofile int64) (baseURL, adminURL string) {
	t.Helper()

	// Os objetos são gerados aqui e montados somente leitura no container. São
	// determinísticos: os mesmos bytes em qualquer máquina, sem versionar nada.
	dir := t.TempDir()
	if _, err := catalog.Generate(dir, []int64{1 << 10, 1 << 20}); err != nil {
		t.Fatalf("gerando catálogo: %v", err)
	}
	absoluto, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("caminho absoluto: %v", err)
	}

	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    "..",
			Dockerfile: "Dockerfile",
			KeepImage:  true,
		},
		ExposedPorts: []string{"8080/tcp", "8081/tcp"},
		Cmd: []string{
			"-dir", "/data/objects",
			"-addr", ":8080",
			"-admin-addr", ":8081",
		},
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, absoluto+":/data/objects:ro")
			if nofile > 0 {
				hc.Ulimits = append(hc.Ulimits, &container.Ulimit{
					Name: "nofile", Soft: nofile, Hard: nofile,
				})
			}
		},
		WaitingFor: wait.ForHTTP("/healthz").WithPort("8080/tcp").WithStartupTimeout(2 * time.Minute),
	}

	origem, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("subindo a origem: %v", err)
	}
	testcontainers.CleanupContainer(t, origem)

	endereco := func(porta string) string {
		host, err := origem.Host(ctx)
		if err != nil {
			t.Fatalf("host do container: %v", err)
		}
		mapeada, err := origem.MappedPort(ctx, porta)
		if err != nil {
			t.Fatalf("porta %s: %v", porta, err)
		}
		return fmt.Sprintf("http://%s:%s", host, mapeada.Port())
	}
	return endereco("8080/tcp"), endereco("8081/tcp")
}

// Com folga de descritores, tudo que foi oferecido conclui. É a linha de base:
// sem ela, o teste da falha não teria com o que comparar.
func TestCargaNormalConcluiTudo(t *testing.T) {
	ctx := context.Background()
	baseURL, adminURL := subirOrigem(ctx, t, 0)

	r, err := loadtest.Run(ctx, loadtest.Config{
		BaseURL: baseURL, AdminURL: adminURL,
		Object: "obj-1KiB.bin", Mode: "streamed",
		Concurrency: 16, Duration: 3 * time.Second, Warmup: time.Second, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("executando carga: %v", err)
	}

	if r.Completed == 0 {
		t.Fatal("nenhuma requisição concluída")
	}
	if r.ErrorRate > 0.01 {
		t.Errorf("taxa de erro = %.2f%% com folga de descritores, esperava perto de zero", r.ErrorRate*100)
	}
	if r.ServerBytes <= 0 {
		t.Error("o servidor deveria ter relatado bytes entregues")
	}
	t.Logf("baseline: oferecida %d, concluída %d, p99 %.2fms, %.1f MB/s",
		r.Offered, r.Completed, r.P99Ms, r.ServerBytesPerSec/(1<<20))
}

// A falha controlada: mais clientes simultâneos do que descritores disponíveis.
func TestEsgotarDescritoresSeparaOferecidaDeConcluida(t *testing.T) {
	ctx := context.Background()
	baseURL, adminURL := subirOrigem(ctx, t, limiteDeDescritores)

	r, err := loadtest.Run(ctx, loadtest.Config{
		BaseURL: baseURL, AdminURL: adminURL,
		Object: "obj-1MiB.bin", Mode: "streamed",
		// Bem mais clientes que descritores: o servidor não tem como aceitar
		// todas as conexões, por mais rápido que responda.
		Concurrency: 256, Duration: 5 * time.Second, Warmup: 0, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("executando carga: %v", err)
	}

	t.Logf("com nofile=%d: oferecida %d, concluída %d, erros %d (%.1f%%), códigos %v",
		limiteDeDescritores, r.Offered, r.Completed, r.Errors, r.ErrorRate*100, r.StatusCodes)
	if len(r.ErrorMessages) > 0 {
		t.Logf("primeira mensagem de erro: %s", r.ErrorMessages[0])
	}

	if r.Errors == 0 {
		t.Fatalf("nenhum erro com apenas %d descritores e 256 clientes: o limite não foi aplicado", limiteDeDescritores)
	}
	// O ponto do experimento: as duas grandezas se separam. Um relatório que só
	// contasse a concluída mostraria um servidor saudável.
	if r.Offered == r.Completed {
		t.Error("carga oferecida e concluída ficaram iguais: a exaustão não apareceu")
	}
}
