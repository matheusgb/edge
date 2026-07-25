// Package loadtest gera carga HTTP reproduzível e registra a evidência.
//
// Duas ideias governam este pacote.
//
// A primeira é separar carga OFERECIDA de carga CONCLUÍDA. Um gerador ingênuo
// conta só o que deu certo, e aí um servidor que rejeita metade das requisições
// parece rápido, porque as lentas viraram erro e sumiram da conta. Aqui as duas
// grandezas são contadas separadamente e aparecem lado a lado no relatório.
//
// A segunda é que o corpo da resposta precisa ser lido até o fim. Se você só olha
// o header e descarta o corpo, mediu o tempo até o primeiro byte, não o tempo de
// entregar o objeto, e o lab inteiro compara justamente estratégias de entrega
// do corpo.
package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config descreve um cenário de carga.
type Config struct {
	BaseURL     string        // ex.: http://127.0.0.1:8080
	Object      string        // nome do objeto no catálogo
	Mode        string        // buffered ou streamed
	Concurrency int           // quantos clientes simultâneos
	Duration    time.Duration // por quanto tempo medir
	Warmup      time.Duration // tempo de aquecimento, descartado da medição
	Timeout     time.Duration // timeout por requisição
}

// Validate confere a configuração antes de gastar tempo medindo.
func (c Config) Validate() error {
	switch {
	case c.BaseURL == "":
		return errors.New("BaseURL é obrigatório")
	case c.Object == "":
		return errors.New("Object é obrigatório")
	case c.Concurrency <= 0:
		return fmt.Errorf("Concurrency deve ser maior que zero, recebi %d", c.Concurrency)
	case c.Duration <= 0:
		return fmt.Errorf("Duration deve ser maior que zero, recebi %s", c.Duration)
	case c.Timeout <= 0:
		return fmt.Errorf("Timeout deve ser maior que zero, recebi %s", c.Timeout)
	}
	return nil
}

// Result é o resumo agregado de um cenário.
type Result struct {
	Scenario string `json:"scenario"`
	Object   string `json:"object"`
	Mode     string `json:"mode"`

	Concurrency int     `json:"concurrency"`
	DurationSec float64 `json:"duration_sec"`

	// Offered é toda requisição que o gerador tentou fazer.
	Offered int64 `json:"offered"`
	// Completed é a fatia que terminou com resposta 2xx e corpo lido inteiro.
	Completed int64 `json:"completed"`
	Errors    int64 `json:"errors"`
	// Timeouts e Cancelled são erros, contados à parte por diagnosticarem coisas
	// diferentes: o primeiro é o servidor demorando, o segundo é o teste acabando.
	Timeouts  int64 `json:"timeouts"`
	Cancelled int64 `json:"cancelled"`

	Bytes int64 `json:"bytes"`

	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
	MaxMs float64 `json:"max_ms"`

	CompletedPerSec float64 `json:"completed_per_sec"`
	OfferedPerSec   float64 `json:"offered_per_sec"`
	BytesPerSec     float64 `json:"bytes_per_sec"`
	ErrorRate       float64 `json:"error_rate"`
}

// counters acumula os números durante a execução.
//
// São int64 manipulados por atomic porque dezenas de goroutines escrevem neles ao
// mesmo tempo. As latências ficam num slice por worker, protegido por mutex só na
// hora de juntar, assim o hot path do gerador não disputa um lock.
type counters struct {
	offered   atomic.Int64
	completed atomic.Int64
	errors    atomic.Int64
	timeouts  atomic.Int64
	cancelled atomic.Int64
	bytes     atomic.Int64

	mu        sync.Mutex
	latencies []time.Duration
}

func (c *counters) latency(d time.Duration) {
	c.mu.Lock()
	c.latencies = append(c.latencies, d)
	c.mu.Unlock()
}

// Run executa um cenário e devolve o resumo.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, fmt.Errorf("configuração inválida: %w", err)
	}

	client := &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			// O pool precisa comportar toda a concorrência. Com o padrão de 2
			// conexões ociosas por host, o gerador ficaria reabrindo conexão a cada
			// requisição e mediria handshake de TCP, não entrega de objeto.
			MaxIdleConns:        cfg.Concurrency * 2,
			MaxIdleConnsPerHost: cfg.Concurrency * 2,
			MaxConnsPerHost:     cfg.Concurrency * 2,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true, // comprimir mudaria os bytes na rede e sujaria a medição
		},
	}
	defer client.CloseIdleConnections()

	url := fmt.Sprintf("%s/objects/%s?mode=%s",
		strings.TrimSuffix(cfg.BaseURL, "/"), cfg.Object, cfg.Mode)

	// Aquecimento: as primeiras requisições pagam abertura de conexão, carga do
	// arquivo no cache de página e o JIT do runtime aquecendo caminhos. Medir isso
	// junto contamina a média com custos que não se repetem.
	if cfg.Warmup > 0 {
		warmCtx, cancel := context.WithTimeout(ctx, cfg.Warmup)
		execute(warmCtx, client, url, cfg.Concurrency, &counters{})
		cancel()
	}

	runCtx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()

	c := &counters{}
	started := time.Now()
	execute(runCtx, client, url, cfg.Concurrency, c)
	elapsed := time.Since(started)

	if err := ctx.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return Result{}, fmt.Errorf("execução interrompida: %w", err)
	}
	return resultFrom(cfg, c, elapsed), nil
}

// execute dispara os workers e espera todos terminarem.
func execute(ctx context.Context, client *http.Client, url string, concurrency int, c *counters) {
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()
			// Buffer reaproveitado por worker: o gerador não pode ser o que aloca
			// mais que o servidor medido, senão o coletor de lixo DELE vira o gargalo.
			buf := make([]byte, 32<<10)
			for {
				if ctx.Err() != nil {
					return
				}
				doRequest(ctx, client, url, c, buf)
			}
		}()
	}
	wg.Wait()
}

// doRequest faz uma requisição e contabiliza o desfecho.
func doRequest(ctx context.Context, client *http.Client, url string, c *counters, buf []byte) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.offered.Add(1)
		c.errors.Add(1)
		return
	}

	c.offered.Add(1)
	started := time.Now()

	resp, err := client.Do(req)
	if err != nil {
		classify(ctx, err, c)
		return
	}
	defer resp.Body.Close()

	// Ler o corpo até o fim é o ponto todo da medição: é aqui que a diferença
	// entre servir de um []byte e servir de um arquivo aparece.
	n, err := io.CopyBuffer(io.Discard, resp.Body, buf)
	c.bytes.Add(n)
	if err != nil {
		classify(ctx, err, c)
		return
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.errors.Add(1)
		return
	}

	c.latency(time.Since(started))
	c.completed.Add(1)
}

// classify separa "o teste acabou" de "o servidor falhou".
//
// Quando a janela de medição fecha, os workers que estavam no meio de uma
// requisição recebem um erro de contexto cancelado. Contar isso como falha do
// servidor inflaria a taxa de erro artificialmente, e quanto maior o objeto,
// maior a inflação, o que enviesaria exatamente a comparação do lab.
func classify(ctx context.Context, err error, c *counters) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(ctx.Err(), context.Canceled):
		c.cancelled.Add(1)
	case errors.Is(err, context.DeadlineExceeded):
		if ctx.Err() != nil {
			c.cancelled.Add(1) // a janela do cenário fechou
		} else {
			c.timeouts.Add(1) // a requisição estourou o próprio timeout
			c.errors.Add(1)
		}
	case os.IsTimeout(err):
		c.timeouts.Add(1)
		c.errors.Add(1)
	default:
		c.errors.Add(1)
	}
}

// resultFrom transforma contadores brutos no resumo agregado.
func resultFrom(cfg Config, c *counters, elapsed time.Duration) Result {
	c.mu.Lock()
	lat := make([]time.Duration, len(c.latencies))
	copy(lat, c.latencies)
	c.mu.Unlock()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })

	secs := elapsed.Seconds()
	if secs <= 0 {
		secs = 1
	}
	offered := c.offered.Load()
	completed := c.completed.Load()

	r := Result{
		Scenario:    fmt.Sprintf("%s-%s-c%d", cfg.Mode, cfg.Object, cfg.Concurrency),
		Object:      cfg.Object,
		Mode:        cfg.Mode,
		Concurrency: cfg.Concurrency,
		DurationSec: secs,
		Offered:     offered,
		Completed:   completed,
		Errors:      c.errors.Load(),
		Timeouts:    c.timeouts.Load(),
		Cancelled:   c.cancelled.Load(),
		Bytes:       c.bytes.Load(),

		P50Ms: millis(percentile(lat, 0.50)),
		P95Ms: millis(percentile(lat, 0.95)),
		P99Ms: millis(percentile(lat, 0.99)),
		MaxMs: millis(percentile(lat, 1.0)),

		CompletedPerSec: float64(completed) / secs,
		OfferedPerSec:   float64(offered) / secs,
		BytesPerSec:     float64(c.bytes.Load()) / secs,
	}
	if offered > 0 {
		r.ErrorRate = float64(r.Errors) / float64(offered)
	}
	return r
}

// percentile usa o método do posto mais próximo sobre amostras JÁ ORDENADAS.
//
// Não interpolamos entre amostras: um p99 interpolado inventa um valor que
// ninguém observou. Preferimos devolver uma latência que realmente aconteceu.
func percentile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(q * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func millis(d time.Duration) float64 {
	return float64(d.Nanoseconds()) / 1e6
}

// Evidence é o pacote de evidência de uma bateria de cenários.
type Evidence struct {
	Scenario  string    `json:"scenario"`
	StartedAt time.Time `json:"started_at"`
	Commit    string    `json:"commit"`
	Host      string    `json:"host"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	NumCPU    int       `json:"num_cpu"`
	GoVersion string    `json:"go_version"`
	Commands  []string  `json:"commands"`
	Results   []Result  `json:"results"`
	Notes     string    `json:"notes"`
}

// NewEvidence preenche os campos de ambiente que dá para descobrir sozinho.
func NewEvidence(scenario string, startedAt time.Time) Evidence {
	host, err := os.Hostname()
	if err != nil {
		host = "desconhecido"
	}
	return Evidence{
		Scenario:  scenario,
		StartedAt: startedAt,
		Commit:    commandOutput("git", "rev-parse", "--short", "HEAD"),
		Host:      host,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		NumCPU:    runtime.NumCPU(),
		GoVersion: runtime.Version(),
	}
}

// commandOutput roda um comando e devolve a saída, ou uma marca de indisponível.
//
// Nunca falha o experimento por causa disso: um ambiente sem git ou sem uname
// ainda produz medição válida, só com evidência menos completa.
func commandOutput(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "(indisponível)"
	}
	return strings.TrimSpace(string(out))
}

// SaveEvidence grava a evidência em evidence/<scenario>/<timestamp>/.
//
// São sempre quatro arquivos: environment.md, commands.txt, summary.md e
// metrics.json. Os três primeiros são para humano ler; o quarto é para outra
// ferramenta consumir sem reparsear texto.
func SaveEvidence(root string, ev Evidence) (string, error) {
	stamp := ev.StartedAt.UTC().Format("20060102T150405Z")
	dir := filepath.Join(root, ev.Scenario, stamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("criando diretório de evidência: %w", err)
	}

	files := map[string]string{
		"environment.md": renderEnvironment(ev),
		"commands.txt":   strings.Join(ev.Commands, "\n") + "\n",
		"summary.md":     renderSummary(ev),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return "", fmt.Errorf("escrevendo %s: %w", name, err)
		}
	}

	blob, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		return "", fmt.Errorf("serializando metrics.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metrics.json"), append(blob, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("escrevendo metrics.json: %w", err)
	}
	return dir, nil
}

func renderEnvironment(ev Evidence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Ambiente\n\n")
	fmt.Fprintf(&b, "- Cenário: %s\n", ev.Scenario)
	fmt.Fprintf(&b, "- Início (UTC): %s\n", ev.StartedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Commit: %s\n", ev.Commit)
	fmt.Fprintf(&b, "- Máquina: %s\n", ev.Host)
	fmt.Fprintf(&b, "- Sistema: %s/%s\n", ev.OS, ev.Arch)
	fmt.Fprintf(&b, "- CPUs visíveis ao processo: %d\n", ev.NumCPU)
	fmt.Fprintf(&b, "- Go: %s\n", ev.GoVersion)
	fmt.Fprintf(&b, "- Kernel: %s\n", commandOutput("uname", "-sr"))
	fmt.Fprintf(&b, "- Memória: %s\n", firstLineMatching(commandOutput("free", "-h"), "Mem:"))
	// Cada limite é consultado numa chamada própria. Numa única chamada, um shell
	// que não conheça uma das opções derruba o comando inteiro e leva junto os
	// limites que ele sabia responder. O `dash`, que costuma ser o /bin/sh em
	// Debian e Ubuntu, não aceita `ulimit -u` e causava exatamente isso.
	fmt.Fprintf(&b, "\n## Limites do processo\n\n```text\ndescritores de arquivo (ulimit -n): %s\nprocessos e threads (ulimit -u):   %s\n```\n",
		commandOutput("sh", "-c", "ulimit -n"),
		commandOutput("sh", "-c", "ulimit -u 2>/dev/null || echo '(não suportado por este shell)'"))
	if ev.Notes != "" {
		fmt.Fprintf(&b, "\n## Observações\n\n%s\n", ev.Notes)
	}
	return b.String()
}

func renderSummary(ev Evidence) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Resumo: %s\n\n", ev.Scenario)
	fmt.Fprintf(&b, "Medição local em %s/%s com %d CPUs. Isto NÃO é capacidade de produção:\n", ev.OS, ev.Arch, ev.NumCPU)
	fmt.Fprintf(&b, "gerador e servidor dividem a mesma máquina e disputam CPU entre si.\n\n")
	fmt.Fprintf(&b, "| Cenário | Modo | Objeto | Conc. | Oferecida/s | Concluída/s | p50 ms | p95 ms | p99 ms | MB/s | Erro |\n")
	fmt.Fprintf(&b, "|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range ev.Results {
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %.1f | %.1f | %.2f | %.2f | %.2f | %.1f | %.2f%% |\n",
			r.Scenario, r.Mode, r.Object, r.Concurrency,
			r.OfferedPerSec, r.CompletedPerSec,
			r.P50Ms, r.P95Ms, r.P99Ms,
			r.BytesPerSec/(1<<20), r.ErrorRate*100)
	}
	return b.String()
}

func firstLineMatching(text, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, prefix) {
			return strings.TrimSpace(line)
		}
	}
	return "(indisponível)"
}
