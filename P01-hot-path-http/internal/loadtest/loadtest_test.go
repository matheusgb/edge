package loadtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestValidateRejeitaConfiguracaoIncompleta(t *testing.T) {
	t.Parallel()

	base := Config{
		BaseURL: "http://127.0.0.1:8080", Object: "obj.bin", Mode: "streamed",
		Concurrency: 4, Duration: time.Second, Timeout: time.Second,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("configuração completa foi rejeitada: %v", err)
	}

	casos := map[string]func(*Config){
		"sem URL":               func(c *Config) { c.BaseURL = "" },
		"sem objeto":            func(c *Config) { c.Object = "" },
		"concorrência zero":     func(c *Config) { c.Concurrency = 0 },
		"duração zero":          func(c *Config) { c.Duration = 0 },
		"timeout zero":          func(c *Config) { c.Timeout = 0 },
		"concorrência negativa": func(c *Config) { c.Concurrency = -1 },
		"taxa negativa":         func(c *Config) { c.Rate = -1 },
	}
	for nome, quebrar := range casos {
		t.Run(nome, func(t *testing.T) {
			cfg := base
			quebrar(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("%s: esperava erro de validação", nome)
			}
		})
	}
}

// Taxa zero não é configuração faltando: é o modelo fechado, em que a
// concorrência é quem limita o ritmo. Precisa continuar válido.
func TestTaxaZeroEhValida(t *testing.T) {
	t.Parallel()

	cfg := Config{
		BaseURL: "http://127.0.0.1:8080", Object: "obj.bin", Mode: "streamed",
		Concurrency: 4, Rate: 0, Duration: time.Second, Timeout: time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("taxa zero foi rejeitada: %v", err)
	}
}

func TestRunMedeContraServidorReal(t *testing.T) {
	t.Parallel()

	corpo := make([]byte, 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(corpo)
	}))
	defer srv.Close()

	r, err := Run(context.Background(), Config{
		BaseURL:     srv.URL,
		Object:      "qualquer.bin",
		Mode:        "streamed",
		Concurrency: 4,
		Duration:    300 * time.Millisecond,
		Timeout:     5 * time.Second,
	})
	if err != nil {
		t.Fatalf("executando: %v", err)
	}

	if r.Completed == 0 {
		t.Fatal("nenhuma requisição concluída contra um servidor saudável")
	}
	// A carga oferecida nunca pode ser menor que a concluída: seria concluir o
	// que não foi pedido.
	if r.Offered < r.Completed {
		t.Errorf("carga oferecida (%d) menor que a concluída (%d): impossível", r.Offered, r.Completed)
	}
	if r.P50Ms <= 0 {
		t.Error("p50 deveria ser positivo com requisições concluídas")
	}
	if r.P99Ms < r.P50Ms {
		t.Errorf("p99 (%.2f) menor que p50 (%.2f)", r.P99Ms, r.P50Ms)
	}
	if r.Scenario != "streamed-qualquer.bin-c4" {
		t.Errorf("Scenario = %q, formato inesperado", r.Scenario)
	}
}

// O gerador drena o corpo mas não o guarda, então quem informa os bytes é o
// servidor. Este teste sobe um servidor com /metrics de verdade e confere que os
// números do lado do servidor chegam ao resultado.
func TestRunLeOsNumerosDoServidor(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	bytesTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "origin_http_response_bytes_total",
		Help: "bytes escritos, por modo",
	}, []string{"mode"})
	registry.MustRegister(bytesTotal)
	registry.MustRegister(prometheus.NewGoCollector())

	corpo := make([]byte, 2048)
	mux := http.NewServeMux()
	mux.HandleFunc("/objects/", func(w http.ResponseWriter, r *http.Request) {
		n, _ := w.Write(corpo)
		bytesTotal.WithLabelValues(r.URL.Query().Get("mode")).Add(float64(n))
	})
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	r, err := Run(context.Background(), Config{
		BaseURL: srv.URL, AdminURL: srv.URL, Object: "x.bin", Mode: "buffered",
		Concurrency: 2, Duration: 300 * time.Millisecond, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("executando: %v", err)
	}

	if r.ServerBytes < float64(r.Completed)*float64(len(corpo)) {
		t.Errorf("bytes do servidor (%.0f) abaixo do mínimo para %d respostas", r.ServerBytes, r.Completed)
	}
	if r.ServerBytesPerSec <= 0 {
		t.Error("vazão do servidor deveria ser positiva")
	}
	// O contador de alocação é do runtime e sempre anda quando há trabalho.
	if r.ServerAllocBytes <= 0 {
		t.Error("o servidor deveria ter alocado memória durante a janela")
	}
	if r.ServerGoroutines <= 0 {
		t.Error("o servidor deveria relatar goroutines vivas")
	}
}

// Sem endereço administrativo, a medição do lado do cliente continua válida e os
// campos do servidor ficam zerados. Um lab sem /metrics ainda mede latência.
func TestRunSemEnderecoAdministrativoNaoFalha(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	r, err := Run(context.Background(), Config{
		BaseURL: srv.URL, Object: "x.bin", Mode: "streamed",
		Concurrency: 2, Duration: 200 * time.Millisecond, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("executando: %v", err)
	}
	if r.Completed == 0 {
		t.Fatal("nenhuma requisição concluída")
	}
	if r.ServerBytes != 0 {
		t.Errorf("sem /metrics, os campos do servidor deveriam ficar zerados, veio %.0f", r.ServerBytes)
	}
}

func TestRunContabilizaErrosDoServidor(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "falhou", http.StatusInternalServerError)
	}))
	defer srv.Close()

	r, err := Run(context.Background(), Config{
		BaseURL: srv.URL, Object: "x.bin", Mode: "streamed",
		Concurrency: 2, Duration: 200 * time.Millisecond, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("executando: %v", err)
	}

	if r.Completed != 0 {
		t.Errorf("resposta 500 não pode contar como concluída, contou %d", r.Completed)
	}
	if r.Errors == 0 {
		t.Error("esperava erros contabilizados")
	}
	if r.ErrorRate <= 0.9 {
		t.Errorf("taxa de erro = %.2f, esperava perto de 1", r.ErrorRate)
	}
	if r.StatusCodes["500"] == 0 {
		t.Errorf("os códigos HTTP deveriam aparecer no resultado: %v", r.StatusCodes)
	}
}

func TestRunRejeitaConfiguracaoInvalida(t *testing.T) {
	t.Parallel()

	if _, err := Run(context.Background(), Config{}); err == nil {
		t.Error("esperava erro para configuração vazia")
	}
}

func TestSaveEvidenceContrato(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	started := time.Date(2026, 7, 25, 18, 30, 0, 0, time.UTC)

	ev := NewEvidence("teste-cenario", started)
	ev.Commands = []string{"go run ./cmd/loadgen -object obj-1MiB.bin"}
	ev.Results = []Result{{
		Scenario: "streamed-obj-1MiB.bin-c8", Object: "obj-1MiB.bin", Mode: "streamed",
		Concurrency: 8, DurationSec: 10, Offered: 100, Completed: 98,
		P50Ms: 1.5, P95Ms: 4, P99Ms: 9, ErrorRate: 0.02,
		ServerBytes: 1 << 20, ServerBytesPerSec: 1 << 20, ServerAllocBytes: 5e9,
	}}

	dir, err := SaveEvidence(root, ev)
	if err != nil {
		t.Fatalf("gravando evidência: %v", err)
	}

	// A evidência precisa ter exatamente estes quatro arquivos, sempre.
	for _, nome := range []string{"environment.md", "commands.txt", "summary.md", "metrics.json"} {
		caminho := filepath.Join(dir, nome)
		info, err := os.Stat(caminho)
		if err != nil {
			t.Errorf("arquivo obrigatório ausente: %s (%v)", nome, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s está vazio", nome)
		}
	}

	// O caminho precisa ser evidence/<cenário>/<timestamp>/.
	if got := filepath.Base(dir); got != "20260725T183000Z" {
		t.Errorf("timestamp do diretório = %q, formato inesperado", got)
	}
	if got := filepath.Base(filepath.Dir(dir)); got != "teste-cenario" {
		t.Errorf("diretório do cenário = %q, esperava teste-cenario", got)
	}

	// metrics.json precisa ser legível por máquina, sem reparsear texto.
	blob, err := os.ReadFile(filepath.Join(dir, "metrics.json"))
	if err != nil {
		t.Fatalf("lendo metrics.json: %v", err)
	}
	var lido Evidence
	if err := json.Unmarshal(blob, &lido); err != nil {
		t.Fatalf("metrics.json não é JSON válido: %v", err)
	}
	if len(lido.Results) != 1 || lido.Results[0].Completed != 98 {
		t.Errorf("metrics.json não preservou os resultados: %+v", lido.Results)
	}
	if lido.Results[0].ServerAllocBytes != 5e9 {
		t.Error("metrics.json precisa preservar também os números do lado do servidor")
	}
	if lido.NumCPU <= 0 {
		t.Error("a evidência precisa registrar quantas CPUs a máquina tinha")
	}
	if lido.OS == "" || lido.Arch == "" {
		t.Error("a evidência precisa registrar sistema operacional e arquitetura")
	}
}

func TestNewEvidencePreencheAmbiente(t *testing.T) {
	t.Parallel()

	ev := NewEvidence("cenario", time.Now())
	if ev.GoVersion == "" {
		t.Error("versão do Go não registrada")
	}
	if ev.Host == "" {
		t.Error("máquina não registrada")
	}
	if ev.NumCPU <= 0 {
		t.Error("número de CPUs não registrado")
	}
}
