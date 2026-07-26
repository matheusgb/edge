// Package loadtest implementa um gerador de carga em degraus (step
// load), reusado pelos cinco experimentos obrigatórios do P04. Cada
// degrau sustenta uma taxa de requisições por segundo por um intervalo
// fixo e grava p50/p95/p99, taxa de erro e bytes transferidos.
package loadtest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Step é um degrau da carga: uma taxa alvo sustentada por uma duração.
type Step struct {
	Name        string
	RPS         int
	Duration    time.Duration
	Concurrency int
}

// StepResult é o resultado agregado de um degrau.
type StepResult struct {
	Name           string        `json:"name"`
	TargetRPS      int           `json:"target_rps"`
	Duration       time.Duration `json:"duration_ns"`
	Requested      int64         `json:"requested"`
	Completed      int64         `json:"completed"`
	Errors         int64         `json:"errors"`
	BytesRead      int64         `json:"bytes_read"`
	P50Millis      float64       `json:"p50_ms"`
	P95Millis      float64       `json:"p95_ms"`
	P99Millis      float64       `json:"p99_ms"`
	ThroughputRPS  float64       `json:"throughput_rps"`
	ErrorRateRatio float64       `json:"error_rate_ratio"`
}

// Runner executa uma sequência de degraus contra uma URL alvo.
type Runner struct {
	Client *http.Client
	URL    string
}

// NewRunner cria um Runner com um cliente HTTP dedicado (fora do limite
// de CPU do serviço medido, conforme o protocolo de medição do repo).
func NewRunner(url string, timeout time.Duration) *Runner {
	return &Runner{
		URL: url,
		Client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 256,
			},
		},
	}
}

// RunStep executa um único degrau e retorna o resultado agregado.
// Coordinated omission é corrigida agendando cada requisição pelo seu
// horário alvo (ticker de intervalo fixo), não pelo horário em que a
// requisição anterior terminou.
func (r *Runner) RunStep(ctx context.Context, step Step) StepResult {
	interval := time.Second / time.Duration(step.RPS)
	deadline := time.Now().Add(step.Duration)

	var (
		requested int64
		completed int64
		errs      int64
		bytesRead int64
		mu        sync.Mutex
		latencies []float64
	)

	sem := make(chan struct{}, step.Concurrency)
	var wg sync.WaitGroup

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			goto drain
		case <-ticker.C:
		}
		atomic.AddInt64(&requested, 1)
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			start := time.Now()
			n, err := r.do(ctx)
			elapsed := time.Since(start)
			if err != nil {
				atomic.AddInt64(&errs, 1)
				return
			}
			atomic.AddInt64(&completed, 1)
			atomic.AddInt64(&bytesRead, int64(n))
			mu.Lock()
			latencies = append(latencies, float64(elapsed.Microseconds())/1000.0)
			mu.Unlock()
		}()
	}
drain:
	wg.Wait()

	sort.Float64s(latencies)
	res := StepResult{
		Name:           step.Name,
		TargetRPS:      step.RPS,
		Duration:       step.Duration,
		Requested:      requested,
		Completed:      completed,
		Errors:         errs,
		BytesRead:      bytesRead,
		P50Millis:      percentile(latencies, 0.50),
		P95Millis:      percentile(latencies, 0.95),
		P99Millis:      percentile(latencies, 0.99),
		ThroughputRPS:  float64(completed) / step.Duration.Seconds(),
		ErrorRateRatio: safeRatio(errs, requested),
	}
	return res
}

// RunSteps executa uma sequência de degraus, em ordem, e retorna todos
// os resultados.
func (r *Runner) RunSteps(ctx context.Context, steps []Step) []StepResult {
	results := make([]StepResult, 0, len(steps))
	for _, step := range steps {
		results = append(results, r.RunStep(ctx, step))
	}
	return results
}

func (r *Runner) do(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil {
		return int(n), err
	}
	if resp.StatusCode >= 500 {
		return int(n), fmt.Errorf("status %d", resp.StatusCode)
	}
	return int(n), nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func safeRatio(num, den int64) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}
