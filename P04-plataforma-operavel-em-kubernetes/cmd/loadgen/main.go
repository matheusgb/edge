// Command loadgen aplica uma carga em degraus contra uma URL e grava
// evidência no contrato comum do repositório. É reusado pelos cinco
// experimentos obrigatórios do P04, cada um passando seu próprio
// cenário e sua própria sequência de degraus via flags.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/matheusgb/edge-lab/p04-plataforma-operavel-em-kubernetes/internal/evidence"
	"github.com/matheusgb/edge-lab/p04-plataforma-operavel-em-kubernetes/internal/loadtest"
)

func main() {
	url := flag.String("url", "", "URL alvo (obrigatório)")
	scenario := flag.String("scenario", "", "nome do cenário de evidência (obrigatório)")
	stepsFlag := flag.String("steps", "", "degraus no formato nome:rps:duração:concorrência, separados por vírgula")
	evidenceDir := flag.String("evidence-dir", "evidence", "diretório raiz de evidência")
	note := flag.String("note", "", "nota livre gravada em environment.md")
	timeout := flag.Duration("timeout", 5*time.Second, "timeout por requisição")
	flag.Parse()

	if *url == "" || *scenario == "" || *stepsFlag == "" {
		fmt.Fprintln(os.Stderr, "uso: loadgen -url=<url> -scenario=<nome> -steps=nome:rps:duração:concorrência[,...]")
		os.Exit(2)
	}

	steps, err := parseSteps(*stepsFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "degraus inválidos:", err)
		os.Exit(2)
	}

	doc := evidence.Capture(*scenario)
	doc.AddCommand(strings.Join(os.Args, " "))
	if *note != "" {
		doc.AddNote(*note)
	}

	runner := loadtest.NewRunner(*url, *timeout)
	results := runner.RunSteps(context.Background(), steps)

	doc.Data = map[string]any{
		"url":   *url,
		"steps": results,
	}
	doc.Summary = summarize(*scenario, *url, results)

	dir, err := doc.Write(*evidenceDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "falha ao gravar evidência:", err)
		os.Exit(1)
	}

	fmt.Println("evidência gravada em", dir)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(results)
}

func parseSteps(spec string) ([]loadtest.Step, error) {
	var steps []loadtest.Step
	for _, part := range strings.Split(spec, ",") {
		fields := strings.Split(part, ":")
		if len(fields) != 4 {
			return nil, fmt.Errorf("degrau %q deve ter 4 campos: nome:rps:duração:concorrência", part)
		}
		rps, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("rps inválido em %q: %w", part, err)
		}
		dur, err := time.ParseDuration(fields[2])
		if err != nil {
			return nil, fmt.Errorf("duração inválida em %q: %w", part, err)
		}
		conc, err := strconv.Atoi(fields[3])
		if err != nil {
			return nil, fmt.Errorf("concorrência inválida em %q: %w", part, err)
		}
		steps = append(steps, loadtest.Step{Name: fields[0], RPS: rps, Duration: dur, Concurrency: conc})
	}
	return steps, nil
}

func summarize(scenario, url string, results []loadtest.StepResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", scenario)
	fmt.Fprintf(&b, "Carga em degraus contra `%s`.\n\n", url)
	fmt.Fprintf(&b, "| degrau | rps alvo | completadas | erros | throughput (rps) | p50 (ms) | p95 (ms) | p99 (ms) |\n")
	fmt.Fprintf(&b, "| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range results {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %.1f | %.2f | %.2f | %.2f |\n",
			r.Name, r.TargetRPS, r.Completed, r.Errors, r.ThroughputRPS, r.P50Millis, r.P95Millis, r.P99Millis)
	}
	return b.String()
}
