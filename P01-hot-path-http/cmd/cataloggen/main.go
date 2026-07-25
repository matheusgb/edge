// Comando cataloggen materializa o catálogo de objetos sintéticos em disco.
//
// Separar a geração do servidor é proposital: os arquivos de carga não vão para o
// Git (são grandes e regeneráveis), e quem clonar o repositório roda este comando
// uma vez para reproduzir exatamente os mesmos bytes.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/matheusgb/edge-lab/p01-hot-path-http/internal/catalog"
)

func main() {
	dir := flag.String("dir", "testdata/objects", "diretório onde os objetos serão gerados")
	sizes := flag.String("sizes", "", "tamanhos em bytes, separados por vírgula (vazio = padrão do lab)")
	flag.Parse()

	parsed, err := parseSizes(*sizes)
	if err != nil {
		log.Fatalf("tamanhos inválidos: %v", err)
	}

	objects, err := catalog.Generate(*dir, parsed)
	if err != nil {
		log.Fatalf("gerando catálogo: %v", err)
	}

	fmt.Fprintf(os.Stdout, "catálogo em %s\n", *dir)
	for _, obj := range objects {
		fmt.Fprintf(os.Stdout, "  %-16s %10d bytes  %s\n", obj.Name, obj.Size, obj.ETag)
	}
}

func parseSizes(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q não é um número: %w", part, err)
		}
		if n <= 0 {
			return nil, fmt.Errorf("tamanho deve ser positivo, recebi %d", n)
		}
		out = append(out, n)
	}
	return out, nil
}
