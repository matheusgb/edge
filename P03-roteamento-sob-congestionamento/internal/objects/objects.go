// Package objects gera os objetos sintéticos servidos pela origem.
//
// Os bytes vêm do NOME do objeto, então origem, edges e gerador de carga chegam
// ao mesmo conteúdo sem combinar nada e sem que o repositório carregue binário
// grande. No P03 isso importa por um motivo específico: os três edges têm caches
// independentes, e um objeto precisa ser byte a byte igual em qualquer um deles
// para que trocar de edge no meio da carga não mude a resposta do cliente.
package objects

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"regexp"
	"strings"
)

// MaxSize limita o objeto gerado. Sem teto, um nome pedindo gigabytes viraria um
// jeito trivial de derrubar a origem por memória.
const MaxSize = 4 << 20

// nameRe aceita apenas nomes simples. A origem é a última linha de defesa: mesmo
// que o roteador ou um edge erre a montagem do caminho, um nome com barra, ".."
// ou byte estranho não passa daqui.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// sizes mapeia o sufixo declarado no nome para o tamanho em bytes.
//
// Os tamanhos existem porque congestionamento não trata todo objeto igual: o de
// 1KiB mede custo por requisição, e o de 1MiB é o que enche uma fila quando a
// banda de um edge é limitada.
var sizes = map[string]int64{
	"1KiB":   1 << 10,
	"64KiB":  64 << 10,
	"256KiB": 256 << 10,
	"1MiB":   1 << 20,
}

// DefaultSize é usado quando o nome não declara tamanho.
const DefaultSize int64 = 64 << 10

// ValidName diz se o nome é aceitável.
//
// A expressão regular sozinha não basta: "." e ".." casam com ela e são nomes de
// diretório, não de objeto.
func ValidName(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	return nameRe.MatchString(name)
}

// SizeOf lê o tamanho declarado no nome, no formato "obj-64KiB-7.bin".
func SizeOf(name string) int64 {
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '.' }) {
		if size, ok := sizes[part]; ok {
			return size
		}
	}
	return DefaultSize
}

// Sizes lista os sufixos de tamanho aceitos, para documentação e mensagem de erro.
func Sizes() []string {
	names := make([]string, 0, len(sizes))
	for name := range sizes {
		names = append(names, name)
	}
	return names
}

// Content devolve os bytes determinísticos do objeto.
//
// O gerador é o ChaCha8 da biblioteca padrão, semeado com o SHA-256 do nome.
// Preencher com zeros seria mais rápido, mas conteúdo repetitivo comprime demais
// e distorceria qualquer medição de bytes na rede, que aqui é o que o cenário de
// banda limitada observa.
func Content(name string) ([]byte, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("nome inválido %q", name)
	}
	size := SizeOf(name)
	if size > MaxSize {
		return nil, fmt.Errorf("tamanho %d acima do máximo %d", size, MaxSize)
	}
	seed := sha256.Sum256([]byte(name))
	gen := rand.NewChaCha8(seed)
	buf := make([]byte, size)
	if _, err := gen.Read(buf); err != nil {
		return nil, fmt.Errorf("gerando %s: %w", name, err)
	}
	return buf, nil
}

// ETag identifica a versão do objeto. Como o conteúdo é função do nome, o ETag
// também é, e os três edges concordam sobre a versão sem trocar mensagem.
func ETag(name string) string {
	sum := sha256.Sum256([]byte("etag:" + name))
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}
