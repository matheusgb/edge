// Package objects gera os objetos sintéticos da origem.
//
// Os bytes são derivados do NOME do objeto, então qualquer máquina produz
// exatamente o mesmo conteúdo sem precisar baixar nem versionar arquivo. Isso
// resolve dois problemas de uma vez:
//
//   - o repositório não carrega binário grande;
//   - cada experimento pode inventar um nome novo e ter certeza de que aquele
//     objeto ainda não está no cache da borda, sem precisar limpar o cache.
//
// O segundo ponto é o que torna o experimento de stampede repetível: basta pedir
// um nome que nunca foi pedido para garantir um MISS.
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
// que a borda erre a normalização do caminho, um nome com barra, ".." ou byte
// estranho não passa daqui.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// sizes mapeia o sufixo declarado no nome para o tamanho em bytes.
var sizes = map[string]int64{
	"1KiB":   1 << 10,
	"64KiB":  64 << 10,
	"256KiB": 256 << 10,
	"1MiB":   1 << 20,
	"4MiB":   4 << 20,
}

// DefaultSize é usado quando o nome não declara tamanho.
const DefaultSize int64 = 64 << 10

// ValidName diz se o nome é aceitável.
//
// A expressão regular sozinha não basta: "." e ".." casam com ela e são nomes de
// diretório, não de objeto. É pouco provável que cheguem até aqui, porque a
// borda e o validador normalizam o caminho antes, mas a origem é a última linha
// de defesa e não deve depender de ninguém ter feito o trabalho direito.
func ValidName(name string) bool {
	if name == "." || name == ".." {
		return false
	}
	return nameRe.MatchString(name)
}

// SizeOf lê o tamanho declarado no nome, no formato "algo-64KiB-tag.bin".
func SizeOf(name string) int64 {
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '.' }) {
		if size, ok := sizes[part]; ok {
			return size
		}
	}
	return DefaultSize
}

// Sizes lista os tamanhos aceitos, para documentação e mensagens de erro.
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
// Poderíamos preencher com zeros, e seria mais rápido, mas conteúdo repetitivo
// comprime demais e distorceria qualquer medição de bytes na rede.
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

// ETag identifica a versão do objeto.
//
// Como o conteúdo é função do nome, o ETag também é: o mesmo nome sempre devolve
// o mesmo valor, em qualquer réplica da origem. Isso é o que permite à borda
// revalidar com If-None-Match e receber 304 em vez do corpo inteiro.
func ETag(name string) string {
	sum := sha256.Sum256([]byte("etag:" + name))
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}
