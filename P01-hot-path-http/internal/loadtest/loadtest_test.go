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

func TestPercentileUsaAmostrasOrdenadas(t *testing.T) {
	t.Parallel()

	amostras := make([]time.Duration, 100)
	for i := range amostras {
		amostras[i] = time.Duration(i+1) * time.Millisecond
	}

	casos := []struct {
		q    float64
		want time.Duration
	}{
		{0.50, 51 * time.Millisecond},
		{0.95, 96 * time.Millisecond},
		{0.99, 100 * time.Millisecond},
		{1.0, 100 * time.Millisecond},
		{0.0, 1 * time.Millisecond},
	}
	for _, c := range casos {
		if got := percentile(amostras, c.q); got != c.want {
			t.Errorf("percentile(q=%.2f) = %s, esperava %s", c.q, got, c.want)
		}
	}

	// O percentil de uma amostra vazia é zero, não pânico: um cenário em que
	// nada concluiu ainda precisa produzir relatório.
	if got := percentile(nil, 0.99); got != 0 {
		t.Errorf("percentile de fatia vazia = %s, esperava 0", got)
	}
}

func TestPercentileSempreDevolveAmostraReal(t *testing.T) {
	t.Parallel()

	// Não interpolamos: o valor devolvido precisa ser um dos observados.
	amostras := []time.Duration{10 * time.Millisecond, 500 * time.Millisecond}
	got := percentile(amostras, 0.99)
	if got != 10*time.Millisecond && got != 500*time.Millisecond {
		t.Errorf("percentile inventou o valor %s, que não está nas amostras", got)
	}
}

func TestResultFromSeparaCargaOferecidaEConcluida(t *testing.T) {
	t.Parallel()

	c := &counters{}
	c.offered.Add(100)
	c.completed.Add(80)
	c.errors.Add(15)
	c.timeouts.Add(5)
	c.cancelled.Add(5)
	c.bytes.Add(80 * 1024)
	for i := range 80 {
		c.latency(time.Duration(i+1) * time.Millisecond)
	}

	cfg := Config{Object: "obj-1KiB.bin", Mode: "buffered", Concurrency: 8}
	r := resultFrom(cfg, c, 10*time.Second)

	if r.Offered != 100 {
		t.Errorf("Offered = %d, esperava 100", r.Offered)
	}
	if r.Completed != 80 {
		t.Errorf("Completed = %d, esperava 80", r.Completed)
	}
	// Este é o ponto do teste: as duas grandezas não podem colapsar numa só, senão
	// um servidor que rejeita metade da carga passaria por rápido.
	if r.Offered == r.Completed {
		t.Error("carga oferecida e concluída não podem ser o mesmo número aqui")
	}
	if r.OfferedPerSec != 10 {
		t.Errorf("OfferedPerSec = %.2f, esperava 10", r.OfferedPerSec)
	}
	if r.CompletedPerSec != 8 {
		t.Errorf("CompletedPerSec = %.2f, esperava 8", r.CompletedPerSec)
	}
	if r.ErrorRate != 0.15 {
		t.Errorf("ErrorRate = %.4f, esperava 0.15", r.ErrorRate)
	}
	if r.Cancelled != 5 {
		t.Errorf("Cancelled = %d, esperava 5", r.Cancelled)
	}
	if r.Scenario != "buffered-obj-1KiB.bin-c8" {
		t.Errorf("Scenario = %q, formato inesperado", r.Scenario)
	}
}

func TestResultFromNaoDivideporZero(t *testing.T) {
	t.Parallel()

	r := resultFrom(Config{Object: "x", Mode: "streamed"}, &counters{}, 0)
	if r.ErrorRate != 0 || r.CompletedPerSec != 0 {
		t.Errorf("resultado vazio deveria ser zerado, veio %+v", r)
	}
}

func TestRunMedeContraServidorReal(t *testing.T) {
	t.Parallel()

	corpo := make([]byte, 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(corpo)
	}))
	defer srv.Close()

	cfg := Config{
		BaseURL:     srv.URL,
		Object:      "qualquer.bin",
		Mode:        "streamed",
		Concurrency: 4,
		Duration:    300 * time.Millisecond,
		Timeout:     5 * time.Second,
	}

	r, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("executando: %v", err)
	}

	if r.Completed == 0 {
		t.Fatal("nenhuma requisição concluída contra um servidor saudável")
	}
	if r.Offered < r.Completed {
		t.Errorf("carga oferecida (%d) menor que a concluída (%d): impossível",
			r.Offered, r.Completed)
	}
	if r.Bytes < r.Completed*int64(len(corpo)) {
		t.Errorf("bytes contados (%d) abaixo do mínimo esperado para %d respostas",
			r.Bytes, r.Completed)
	}
	if r.P50Ms <= 0 {
		t.Error("p50 deveria ser positivo com requisições concluídas")
	}
	if r.P99Ms < r.P50Ms {
		t.Errorf("p99 (%.2f) menor que p50 (%.2f)", r.P99Ms, r.P50Ms)
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
		P50Ms: 1.5, P95Ms: 4, P99Ms: 9, BytesPerSec: 1 << 20, ErrorRate: 0.02,
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
