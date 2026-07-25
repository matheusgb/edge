// Package catalog cria e carrega um catálogo determinístico de objetos sintéticos.
//
// Determinístico quer dizer que, dado o mesmo nome e o mesmo tamanho, o conteúdo
// gerado é sempre byte a byte igual, em qualquer máquina, em qualquer execução.
// Isso importa porque as medições do lab comparam dois modos de servir o MESMO
// objeto. Se o conteúdo mudasse entre execuções, o ETag mudaria e a comparação
// perderia o sentido.
package catalog

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Object descreve um objeto do catálogo já materializado em disco.
type Object struct {
	Name string // nome público, usado na URL
	Path string // caminho absoluto no disco
	Size int64  // tamanho em bytes
	ETag string // ETag forte, derivado do conteúdo
}

// Catalog é o índice em memória dos objetos disponíveis.
type Catalog struct {
	Dir     string
	objects map[string]Object
}

// DefaultSizes são os tamanhos usados quando nenhum é informado.
//
// A escala é proposital: 1 KiB cabe folgado num único pacote de rede, 64 KiB é a
// ordem de grandeza do buffer interno que o Go usa ao copiar, 1 MiB já obriga
// várias idas ao kernel e 16 MiB é grande o bastante para que "carregar tudo em
// memória" custe visivelmente caro sob concorrência.
var DefaultSizes = []int64{1 << 10, 64 << 10, 1 << 20, 16 << 20}

// NameForSize devolve o nome canônico de um objeto de um dado tamanho.
func NameForSize(size int64) string {
	switch {
	case size >= 1<<20 && size%(1<<20) == 0:
		return fmt.Sprintf("obj-%dMiB.bin", size/(1<<20))
	case size >= 1<<10 && size%(1<<10) == 0:
		return fmt.Sprintf("obj-%dKiB.bin", size/(1<<10))
	default:
		return fmt.Sprintf("obj-%dB.bin", size)
	}
}

// Generate materializa em dir um objeto para cada tamanho pedido.
//
// Se o arquivo já existe com o tamanho certo, ele é preservado: gerar 16 MiB a
// cada execução seria desperdício, e o conteúdo é determinístico de qualquer forma.
func Generate(dir string, sizes []int64) ([]Object, error) {
	if len(sizes) == 0 {
		sizes = DefaultSizes
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("criando diretório do catálogo: %w", err)
	}

	objects := make([]Object, 0, len(sizes))
	for _, size := range sizes {
		if size < 0 {
			return nil, fmt.Errorf("tamanho inválido: %d", size)
		}
		name := NameForSize(size)
		path := filepath.Join(dir, name)

		if info, err := os.Stat(path); err == nil && info.Size() == size {
			obj, err := describe(name, path, size)
			if err != nil {
				return nil, err
			}
			objects = append(objects, obj)
			continue
		}

		if err := writeDeterministic(path, name, size); err != nil {
			return nil, err
		}
		obj, err := describe(name, path, size)
		if err != nil {
			return nil, err
		}
		objects = append(objects, obj)
	}

	sort.Slice(objects, func(i, j int) bool { return objects[i].Size < objects[j].Size })
	return objects, nil
}

// writeDeterministic escreve size bytes derivados do nome.
//
// Usamos um gerador xorshift64* semeado pelo nome: é barato, não depende do
// math/rand global (que outros pacotes poderiam re-semear) e produz bytes que não
// comprimem bem, o que importa para o tamanho na rede corresponder ao tamanho do
// objeto, sem uma compressão acidental mascarar a medição.
func writeDeterministic(path, name string, size int64) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("criando %s: %w", tmp, err)
	}

	state := seedFromName(name)
	buf := make([]byte, 64<<10)
	var written int64
	for written < size {
		chunk := int64(len(buf))
		if remaining := size - written; remaining < chunk {
			chunk = remaining
		}
		for i := int64(0); i < chunk; i += 8 {
			state = nextState(state)
			var word [8]byte
			binary.LittleEndian.PutUint64(word[:], state)
			copy(buf[i:min(i+8, chunk)], word[:])
		}
		if _, err := f.Write(buf[:chunk]); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("escrevendo %s: %w", tmp, err)
		}
		written += chunk
	}

	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("fechando %s: %w", tmp, err)
	}
	// Rename é atômico no mesmo sistema de arquivos: ou o objeto aparece completo,
	// ou não aparece. Evita que uma execução interrompida deixe um arquivo curto.
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renomeando para %s: %w", path, err)
	}
	return nil
}

func seedFromName(name string) uint64 {
	sum := sha256.Sum256([]byte(name))
	state := binary.LittleEndian.Uint64(sum[:8])
	if state == 0 {
		state = 0x9E3779B97F4A7C15
	}
	return state
}

func nextState(state uint64) uint64 {
	state ^= state >> 12
	state ^= state << 25
	state ^= state >> 27
	return state * 0x2545F4914F6CDD1D
}

// describe calcula o ETag do objeto a partir do conteúdo em disco.
func describe(name, path string, size int64) (Object, error) {
	f, err := os.Open(path)
	if err != nil {
		return Object{}, fmt.Errorf("abrindo %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return Object{}, fmt.Errorf("lendo %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return Object{
		Name: name,
		Path: abs,
		Size: size,
		// Aspas fazem parte da sintaxe do header ETag (RFC 9110). Sem prefixo W/,
		// é um ETag forte: promete igualdade byte a byte, que é o nosso caso.
		ETag: `"` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`,
	}, nil
}

// Load lê um diretório já gerado e monta o índice em memória.
func Load(dir string) (*Catalog, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("lendo catálogo em %s: %w", dir, err)
	}

	c := &Catalog{Dir: dir, objects: make(map[string]Object)}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".bin") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("lendo metadados de %s: %w", entry.Name(), err)
		}
		obj, err := describe(entry.Name(), filepath.Join(dir, entry.Name()), info.Size())
		if err != nil {
			return nil, err
		}
		c.objects[obj.Name] = obj
	}

	if len(c.objects) == 0 {
		return nil, fmt.Errorf("catálogo vazio em %s: rode o cataloggen antes", dir)
	}
	return c, nil
}

// ErrNotFound indica que o objeto pedido não existe no catálogo.
var ErrNotFound = errors.New("objeto não encontrado no catálogo")

// Get devolve um objeto pelo nome.
func (c *Catalog) Get(name string) (Object, error) {
	obj, ok := c.objects[name]
	if !ok {
		return Object{}, fmt.Errorf("%q: %w", name, ErrNotFound)
	}
	return obj, nil
}

// List devolve os objetos ordenados por tamanho.
func (c *Catalog) List() []Object {
	out := make([]Object, 0, len(c.objects))
	for _, obj := range c.objects {
		out = append(out, obj)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size < out[j].Size })
	return out
}

// Len devolve quantos objetos o catálogo conhece.
func (c *Catalog) Len() int { return len(c.objects) }
