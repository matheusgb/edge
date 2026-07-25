// Package loadtest gera carga HTTP reproduzível e registra a evidência.
//
// A carga é gerada pelo Vegeta. Escrever um gerador próprio seria escrever
// ferramenta, e a pergunta deste lab é sobre o SERVIDOR, não sobre o cliente.
// Além disso, um gerador caseiro erra em detalhes que custam caro:
//
//   - o corpo precisa ser lido até o fim, senão a medição para no primeiro byte
//     e o lab inteiro, que compara estratégias de entrega do corpo, mede nada;
//   - a latência precisa ser cravada depois da última leitura, não depois do
//     header;
//   - o pool de conexões precisa comportar a concorrência pedida, senão o que se
//     mede é handshake de TCP.
//
// Duas escolhas de configuração merecem explicação.
//
// A primeira é `Rate` zero, que no Vegeta significa "taxa máxima, limitada pelo
// número de workers". É o modelo fechado: N clientes em laço, cada um esperando
// a resposta antes de pedir de novo. É deliberado, porque a pergunta do P01 é o
// que acontece com N downloads SIMULTÂNEOS, e não a que taxa o servidor satura.
// Quando a pergunta for a segunda, basta informar uma taxa e o Vegeta passa a
// disparar na hora marcada, tratando coordinated omission.
//
// A segunda é `MaxBody(0)`, que faz o Vegeta drenar o corpo sem guardá-lo. Sem
// isso, o cliente aloca uma cópia de cada objeto de 16 MiB que recebe, e o
// gerador vira o maior alocador da máquina: exatamente o efeito que este lab
// quer observar no servidor. Os bytes entregues passam a vir do contador do
// próprio servidor, que é a fonte correta para essa informação.
package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/matheusgb/edge-lab/p01-hot-path-http/internal/promscrape"
)

// Config descreve um cenário de carga.
type Config struct {
	BaseURL     string        // ex.: http://127.0.0.1:8080
	AdminURL    string        // ex.: http://127.0.0.1:8081, de onde vêm as métricas do servidor
	Object      string        // nome do objeto no catálogo
	Mode        string        // buffered ou streamed
	Concurrency int           // clientes simultâneos (workers do Vegeta)
	Rate        int           // requisições por segundo; 0 = modelo fechado, limitado pela concorrência
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
	case c.Rate < 0:
		return fmt.Errorf("Rate não pode ser negativo, recebi %d", c.Rate)
	}
	return nil
}

// Result é o resumo agregado de um cenário.
//
// Ele tem dois grupos de números, e a separação é proposital: o primeiro é o que
// o CLIENTE observou, o segundo é o que o SERVIDOR relatou de si mesmo. Quando os
// dois discordam, a discordância é informação.
type Result struct {
	Scenario string `json:"scenario"`
	Object   string `json:"object"`
	Mode     string `json:"mode"`

	Concurrency int     `json:"concurrency"`
	TargetRate  int     `json:"target_rate"`
	DurationSec float64 `json:"duration_sec"`

	// --- o que o cliente observou ---

	// Offered é toda requisição que o gerador tentou fazer.
	Offered uint64 `json:"offered"`
	// Completed é a fatia que terminou com resposta de sucesso e corpo drenado.
	Completed uint64 `json:"completed"`
	Errors    uint64 `json:"errors"`

	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
	MaxMs float64 `json:"max_ms"`

	CompletedPerSec float64        `json:"completed_per_sec"`
	OfferedPerSec   float64        `json:"offered_per_sec"`
	ErrorRate       float64        `json:"error_rate"`
	StatusCodes     map[string]int `json:"status_codes"`
	ErrorMessages   []string       `json:"error_messages,omitempty"`

	// --- o que o servidor relatou ---

	// ServerBytes são os bytes de corpo que o servidor diz ter escrito. Vem do
	// contador dele porque o gerador drena o corpo sem contar, para não virar o
	// maior alocador da máquina.
	ServerBytes       float64 `json:"server_bytes"`
	ServerBytesPerSec float64 `json:"server_bytes_per_sec"`
	// ServerAllocBytes é quanta memória o servidor alocou durante a janela. É a
	// medida central do lab: dois modos entregam o mesmo conteúdo, e este número
	// mostra o preço que cada um cobrou por isso.
	ServerAllocBytes float64 `json:"server_alloc_bytes"`
	// ServerHeapInUse é o heap vivo no fim da janela.
	ServerHeapInUse float64 `json:"server_heap_in_use_bytes"`
	// ServerGCCycles são as coletas de lixo durante a janela. Sozinho, este
	// número engana: veja o README.
	ServerGCCycles float64 `json:"server_gc_cycles"`
	// ServerGoroutines é a foto no fim da janela: uma por requisição em curso,
	// mais as fixas do processo.
	ServerGoroutines float64 `json:"server_goroutines"`
	// ServerCancellations conta clientes que desistiram no meio.
	ServerCancellations float64 `json:"server_cancellations"`
}

// Run executa um cenário e devolve o resumo.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, fmt.Errorf("configuração inválida: %w", err)
	}

	url := fmt.Sprintf("%s/objects/%s?mode=%s",
		strings.TrimSuffix(cfg.BaseURL, "/"), cfg.Object, cfg.Mode)
	targeter := vegeta.NewStaticTargeter(vegeta.Target{Method: "GET", URL: url})
	rate := vegeta.Rate{Freq: cfg.Rate, Per: time.Second}

	// Aquecimento: as primeiras requisições pagam abertura de conexão, carga do
	// arquivo no cache de página do sistema operacional e caminhos do runtime
	// esquentando. Medir isso junto contamina o resultado com custos que não se
	// repetem.
	if cfg.Warmup > 0 {
		warm := newAttacker(cfg)
		for range warm.Attack(targeter, rate, cfg.Warmup, "warmup") {
		}
		warm.Stop()
	}

	// A leitura "antes" vai depois do aquecimento e imediatamente antes do
	// ataque: tudo que acontecer entre ela e a leitura final entra na conta.
	before, beforeErr := snapshot(ctx, cfg)

	attacker := newAttacker(cfg)
	var metrics vegeta.Metrics
	started := time.Now()
	for res := range attacker.Attack(targeter, rate, cfg.Duration, cfg.Mode) {
		metrics.Add(res)
	}
	metrics.Close()
	attacker.Stop()
	elapsed := time.Since(started)

	after, afterErr := snapshot(ctx, cfg)

	result := resultFrom(cfg, &metrics, elapsed)
	if beforeErr == nil && afterErr == nil {
		fillServerSide(&result, before, after, cfg.Mode, elapsed)
	}
	return result, nil
}

// newAttacker monta o atacante do Vegeta.
//
// Um por fase, nunca reaproveitado: o atacante guarda estado de uma campanha, e
// reusar a mesma instância entre o aquecimento e a medição faz a segunda
// terminar quase imediatamente, com um punhado de amostras.
func newAttacker(cfg Config) *vegeta.Attacker {
	return vegeta.NewAttacker(
		vegeta.Timeout(cfg.Timeout),
		vegeta.KeepAlive(true),
		// Workers e MaxWorkers iguais fixam a concorrência: nem menos clientes
		// que o pedido, nem o Vegeta subindo workers extras para perseguir uma
		// taxa. Com Rate zero, é exatamente o modelo fechado de N clientes.
		vegeta.Workers(uint64(cfg.Concurrency)),
		vegeta.MaxWorkers(uint64(cfg.Concurrency)),
		vegeta.Connections(cfg.Concurrency),
		vegeta.MaxConnections(cfg.Concurrency),
		// O corpo é drenado e descartado. Guardar 16 MiB por resposta faria o
		// gerador alocar mais que o servidor medido.
		vegeta.MaxBody(0),
	)
}

func snapshot(ctx context.Context, cfg Config) (promscrape.Snapshot, error) {
	if cfg.AdminURL == "" {
		return promscrape.Snapshot{}, errors.New("sem endereço administrativo")
	}
	return promscrape.Fetch(ctx, strings.TrimSuffix(cfg.AdminURL, "/")+"/metrics")
}

func resultFrom(cfg Config, m *vegeta.Metrics, elapsed time.Duration) Result {
	secs := elapsed.Seconds()
	if secs <= 0 {
		secs = 1
	}
	completed := uint64(float64(m.Requests) * m.Success)

	return Result{
		Scenario:    fmt.Sprintf("%s-%s-c%d", cfg.Mode, cfg.Object, cfg.Concurrency),
		Object:      cfg.Object,
		Mode:        cfg.Mode,
		Concurrency: cfg.Concurrency,
		TargetRate:  cfg.Rate,
		DurationSec: secs,

		Offered:   m.Requests,
		Completed: completed,
		Errors:    m.Requests - completed,

		P50Ms: millis(m.Latencies.P50),
		P95Ms: millis(m.Latencies.P95),
		P99Ms: millis(m.Latencies.P99),
		MaxMs: millis(m.Latencies.Max),

		CompletedPerSec: float64(completed) / secs,
		OfferedPerSec:   float64(m.Requests) / secs,
		ErrorRate:       1 - m.Success,
		StatusCodes:     m.StatusCodes,
		ErrorMessages:   m.Errors,
	}
}

// fillServerSide completa o resultado com o que o servidor relatou de si mesmo.
func fillServerSide(r *Result, before, after promscrape.Snapshot, mode string, elapsed time.Duration) {
	byMode := map[string]string{"mode": mode}

	r.ServerBytes = promscrape.Delta(before, after, "origin_http_response_bytes_total", byMode)
	r.ServerAllocBytes = promscrape.Delta(before, after, "go_memstats_alloc_bytes_total", nil)
	r.ServerGCCycles = promscrape.Delta(before, after, "go_gc_cycles_total_gc_cycles_total", nil)
	r.ServerCancellations = promscrape.Delta(before, after, "origin_http_client_cancellations_total", byMode)

	// Heap em uso e goroutines são gauges: o que interessa é o valor no fim da
	// janela, não a diferença.
	r.ServerHeapInUse = after.Sum("go_memstats_heap_inuse_bytes", nil)
	r.ServerGoroutines = after.Sum("go_goroutines", nil)

	if secs := elapsed.Seconds(); secs > 0 {
		r.ServerBytesPerSec = r.ServerBytes / secs
	}
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

	fmt.Fprintf(&b, "## O que o cliente observou\n\n")
	fmt.Fprintf(&b, "| Cenário | Modo | Objeto | Conc. | Oferecida/s | Concluída/s | p50 ms | p95 ms | p99 ms | Erro |\n")
	fmt.Fprintf(&b, "|---|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range ev.Results {
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %.1f | %.1f | %.2f | %.2f | %.2f | %.2f%% |\n",
			r.Scenario, r.Mode, r.Object, r.Concurrency,
			r.OfferedPerSec, r.CompletedPerSec,
			r.P50Ms, r.P95Ms, r.P99Ms, r.ErrorRate*100)
	}

	fmt.Fprintf(&b, "\n## O que o servidor relatou de si mesmo\n\n")
	fmt.Fprintf(&b, "| Cenário | MB/s entregues | Alocado na janela | Heap no fim | Coletas | Goroutines no fim |\n")
	fmt.Fprintf(&b, "|---|---:|---:|---:|---:|---:|\n")
	for _, r := range ev.Results {
		fmt.Fprintf(&b, "| %s | %.1f | %.2f GB | %.1f MB | %.0f | %.0f |\n",
			r.Scenario, r.ServerBytesPerSec/(1<<20),
			r.ServerAllocBytes/1e9, r.ServerHeapInUse/1e6,
			r.ServerGCCycles, r.ServerGoroutines)
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
