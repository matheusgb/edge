// Package evidence grava o contrato comum de evidência do edge-lab:
// evidence/<scenario>/<timestamp>/{environment.md,commands.txt,summary.md,metrics.json}.
package evidence

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Doc é o registro de um experimento, capturado antes de medir e
// preenchido com os dados coletados durante a execução.
type Doc struct {
	Scenario  string    `json:"scenario"`
	StartedAt time.Time `json:"started_at"`
	Commit    string    `json:"commit"`
	Host      string    `json:"host"`
	OS        string    `json:"os"`
	Arch      string    `json:"arch"`
	NumCPU    int       `json:"num_cpu"`
	GoVersion string    `json:"go_version"`
	Commands  []string  `json:"commands"`
	Notes     []string  `json:"notes,omitempty"`
	Network   any       `json:"network,omitempty"`
	Data      any       `json:"data"`
	Summary   string    `json:"-"`
}

// Capture registra o ambiente no momento em que o experimento começa.
func Capture(scenario string) *Doc {
	host, _ := os.Hostname()
	commit := strings.TrimSpace(gitCommit())
	return &Doc{
		Scenario:  scenario,
		StartedAt: time.Now().UTC(),
		Commit:    commit,
		Host:      host,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		NumCPU:    runtime.NumCPU(),
		GoVersion: runtime.Version(),
	}
}

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "desconhecido"
	}
	return string(out)
}

// AddCommand registra um comando original executado durante o experimento.
func (d *Doc) AddCommand(cmd string) {
	d.Commands = append(d.Commands, cmd)
}

// AddNote registra uma observação livre sobre a rodada.
func (d *Doc) AddNote(note string) {
	d.Notes = append(d.Notes, note)
}

// Write grava os quatro arquivos do contrato em evidence/<scenario>/<timestamp>/.
func (d *Doc) Write(baseDir string) (string, error) {
	ts := d.StartedAt.Format("20060102T150405Z")
	dir := filepath.Join(baseDir, d.Scenario, ts)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("criar diretório de evidência: %w", err)
	}

	if err := d.writeEnvironment(dir); err != nil {
		return "", err
	}
	if err := d.writeCommands(dir); err != nil {
		return "", err
	}
	if err := d.writeSummary(dir); err != nil {
		return "", err
	}
	if err := d.writeMetrics(dir); err != nil {
		return "", err
	}
	return dir, nil
}

func (d *Doc) writeEnvironment(dir string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Ambiente\n\n")
	fmt.Fprintf(&b, "- cenário: %s\n", d.Scenario)
	fmt.Fprintf(&b, "- início (UTC): %s\n", d.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- commit: %s\n", d.Commit)
	fmt.Fprintf(&b, "- máquina: %s\n", d.Host)
	fmt.Fprintf(&b, "- sistema operacional: %s\n", d.OS)
	fmt.Fprintf(&b, "- arquitetura: %s\n", d.Arch)
	fmt.Fprintf(&b, "- CPUs lógicas: %d\n", d.NumCPU)
	fmt.Fprintf(&b, "- versão do Go: %s\n", d.GoVersion)
	if d.Network != nil {
		net, _ := json.MarshalIndent(d.Network, "", "  ")
		fmt.Fprintf(&b, "\n## Estado do cluster/rede no início\n\n```json\n%s\n```\n", net)
	}
	if len(d.Notes) > 0 {
		fmt.Fprintf(&b, "\n## Notas\n\n")
		for _, n := range d.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	return os.WriteFile(filepath.Join(dir, "environment.md"), []byte(b.String()), 0o644)
}

func (d *Doc) writeCommands(dir string) error {
	content := strings.Join(d.Commands, "\n") + "\n"
	return os.WriteFile(filepath.Join(dir, "commands.txt"), []byte(content), 0o644)
}

func (d *Doc) writeSummary(dir string) error {
	return os.WriteFile(filepath.Join(dir, "summary.md"), []byte(d.Summary), 0o644)
}

func (d *Doc) writeMetrics(dir string) error {
	b, err := json.MarshalIndent(d.Data, "", "  ")
	if err != nil {
		return fmt.Errorf("serializar metrics.json: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "metrics.json"), b, 0o644)
}
