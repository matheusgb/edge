package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateProduzConteudoDeterministico(t *testing.T) {
	t.Parallel()

	sizes := []int64{1 << 10, 4 << 10}

	dirA := t.TempDir()
	dirB := t.TempDir()

	objA, err := Generate(dirA, sizes)
	if err != nil {
		t.Fatalf("gerando em dirA: %v", err)
	}
	objB, err := Generate(dirB, sizes)
	if err != nil {
		t.Fatalf("gerando em dirB: %v", err)
	}

	if len(objA) != len(sizes) {
		t.Fatalf("esperava %d objetos, recebi %d", len(sizes), len(objA))
	}

	// Este é o contrato que sustenta a comparação do lab: em qualquer máquina, o
	// mesmo nome gera os mesmos bytes, logo o mesmo ETag.
	for i := range objA {
		if objA[i].ETag != objB[i].ETag {
			t.Errorf("%s: ETag divergiu entre diretórios: %s vs %s",
				objA[i].Name, objA[i].ETag, objB[i].ETag)
		}
		bytesA, err := os.ReadFile(objA[i].Path)
		if err != nil {
			t.Fatalf("lendo %s: %v", objA[i].Path, err)
		}
		bytesB, err := os.ReadFile(objB[i].Path)
		if err != nil {
			t.Fatalf("lendo %s: %v", objB[i].Path, err)
		}
		if string(bytesA) != string(bytesB) {
			t.Errorf("%s: conteúdo divergiu entre execuções", objA[i].Name)
		}
	}
}

func TestGenerateRespeitaTamanhoPedido(t *testing.T) {
	t.Parallel()

	// 100 não é múltiplo de 8, o que exercita o caminho em que o último bloco do
	// gerador precisa ser truncado.
	sizes := []int64{1, 100, 1 << 10}
	objects, err := Generate(t.TempDir(), sizes)
	if err != nil {
		t.Fatalf("gerando: %v", err)
	}

	for i, obj := range objects {
		info, err := os.Stat(obj.Path)
		if err != nil {
			t.Fatalf("stat de %s: %v", obj.Path, err)
		}
		if info.Size() != sizes[i] {
			t.Errorf("%s: esperava %d bytes em disco, tem %d", obj.Name, sizes[i], info.Size())
		}
		if obj.Size != sizes[i] {
			t.Errorf("%s: metadado diz %d bytes, esperava %d", obj.Name, obj.Size, sizes[i])
		}
	}
}

func TestGenerateNaoReescreveObjetoExistente(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sizes := []int64{2 << 10}

	first, err := Generate(dir, sizes)
	if err != nil {
		t.Fatalf("primeira geração: %v", err)
	}
	infoBefore, err := os.Stat(first[0].Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	second, err := Generate(dir, sizes)
	if err != nil {
		t.Fatalf("segunda geração: %v", err)
	}
	infoAfter, err := os.Stat(second[0].Path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Error("o objeto foi reescrito, mas já existia com o tamanho correto")
	}
	if first[0].ETag != second[0].ETag {
		t.Errorf("ETag mudou entre gerações: %s vs %s", first[0].ETag, second[0].ETag)
	}
}

func TestNameForSizeUsaUnidadeLegivel(t *testing.T) {
	t.Parallel()

	casos := []struct {
		size int64
		want string
	}{
		{512, "obj-512B.bin"},
		{1 << 10, "obj-1KiB.bin"},
		{64 << 10, "obj-64KiB.bin"},
		{1 << 20, "obj-1MiB.bin"},
		{16 << 20, "obj-16MiB.bin"},
	}
	for _, c := range casos {
		if got := NameForSize(c.size); got != c.want {
			t.Errorf("NameForSize(%d) = %q, esperava %q", c.size, got, c.want)
		}
	}
}

func TestLoadEncontraObjetosEReportaAusentes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := Generate(dir, []int64{1 << 10}); err != nil {
		t.Fatalf("gerando: %v", err)
	}
	// Um arquivo que não é .bin precisa ser ignorado pelo índice.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("nota"), 0o644); err != nil {
		t.Fatalf("escrevendo ruído: %v", err)
	}

	c, err := Load(dir)
	if err != nil {
		t.Fatalf("carregando: %v", err)
	}
	if c.Len() != 1 {
		t.Errorf("esperava 1 objeto no índice, achei %d", c.Len())
	}

	if _, err := c.Get("obj-1KiB.bin"); err != nil {
		t.Errorf("objeto existente não foi encontrado: %v", err)
	}

	_, err = c.Get("nao-existe.bin")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("esperava ErrNotFound, recebi %v", err)
	}
}

func TestLoadFalhaEmDiretorioVazio(t *testing.T) {
	t.Parallel()

	if _, err := Load(t.TempDir()); err == nil {
		t.Error("esperava erro ao carregar catálogo vazio, para o servidor não subir sem objetos")
	}
}

func TestListOrdenaPorTamanho(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := Generate(dir, []int64{4 << 10, 1 << 10, 2 << 10}); err != nil {
		t.Fatalf("gerando: %v", err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatalf("carregando: %v", err)
	}

	list := c.List()
	for i := 1; i < len(list); i++ {
		if list[i-1].Size > list[i].Size {
			t.Errorf("lista fora de ordem em %d: %d antes de %d",
				i, list[i-1].Size, list[i].Size)
		}
	}
}
