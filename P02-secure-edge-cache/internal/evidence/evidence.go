// Package evidence grava o pacote de evidência de um experimento.
//
// O contrato do EdgeLab é o mesmo em todos os projetos: cada execução deixa
// evidence/<cenário>/<timestamp UTC>/ com quatro arquivos.
//
//	environment.md  máquina, kernel, CPUs, commit
//	commands.txt    os comandos equivalentes, para outra pessoa repetir
//	summary.md      a tabela legível, com a conclusão
//	metrics.json    os mesmos dados para outra ferramenta consumir
//
// A razão de existir é simples: um número sem o ambiente em volta não significa
// nada. "97% de cache hit" só vira evidência quando vem com a máquina, o commit,
// o cenário e o comando que produziu o resultado.
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

// Doc é o pacote de evidência de um experimento.
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
	Notes     string    `json:"notes"`

	// Data são os números do experimento, no formato que cada comando escolher.
	Data any `json:"data"`

	// Summary é o markdown que vai para summary.md. Cada experimento monta o seu,
	// porque a tabela de um stampede não se parece com a de um teste negativo.
	Summary string `json:"-"`
}

// New preenche os campos de ambiente que dá para descobrir sozinho.
func New(scenario string) Doc {
	host, err := os.Hostname()
	if err != nil {
		host = "desconhecido"
	}
	return Doc{
		Scenario:  scenario,
		StartedAt: time.Now(),
		Commit:    output("git", "rev-parse", "--short", "HEAD"),
		Host:      host,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		NumCPU:    runtime.NumCPU(),
		GoVersion: runtime.Version(),
	}
}

// Save grava os quatro arquivos e devolve o diretório criado.
func Save(root string, doc Doc) (string, error) {
	stamp := doc.StartedAt.UTC().Format("20060102T150405Z")
	dir := filepath.Join(root, doc.Scenario, stamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("criando diretório de evidência: %w", err)
	}

	files := map[string]string{
		"environment.md": renderEnvironment(doc),
		"commands.txt":   strings.Join(doc.Commands, "\n") + "\n",
		"summary.md":     doc.Summary,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			return "", fmt.Errorf("escrevendo %s: %w", name, err)
		}
	}

	blob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("serializando metrics.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metrics.json"), append(blob, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("escrevendo metrics.json: %w", err)
	}
	return dir, nil
}

func renderEnvironment(doc Doc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Ambiente\n\n")
	fmt.Fprintf(&b, "- Cenário: %s\n", doc.Scenario)
	fmt.Fprintf(&b, "- Início (UTC): %s\n", doc.StartedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Commit: %s\n", doc.Commit)
	fmt.Fprintf(&b, "- Máquina: %s\n", doc.Host)
	fmt.Fprintf(&b, "- Sistema: %s/%s\n", doc.OS, doc.Arch)
	fmt.Fprintf(&b, "- CPUs visíveis ao processo: %d\n", doc.NumCPU)
	fmt.Fprintf(&b, "- Go: %s\n", doc.GoVersion)
	fmt.Fprintf(&b, "- Kernel: %s\n", output("uname", "-sr"))
	fmt.Fprintf(&b, "- Memória: %s\n", firstLineMatching(output("free", "-h"), "Mem:"))
	fmt.Fprintf(&b, "- Docker: %s\n", output("docker", "version", "--format", "{{.Server.Version}}"))
	fmt.Fprintf(&b, "- Nginx da borda: %s\n", output("docker", "compose", "exec", "-T", "edge", "nginx", "-v"))
	if doc.Notes != "" {
		fmt.Fprintf(&b, "\n## Observações\n\n%s\n", doc.Notes)
	}
	return b.String()
}

// output roda um comando e devolve a saída, ou uma marca de indisponível.
//
// Nunca derruba o experimento: um ambiente sem git, sem uname ou sem docker no
// PATH ainda produz medição válida, só com evidência menos completa.
func output(name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return "(indisponível)"
	}
	return strings.TrimSpace(string(out))
}

func firstLineMatching(text, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, prefix) {
			return strings.TrimSpace(line)
		}
	}
	return "(indisponível)"
}
