package objects

import (
	"bytes"
	"testing"
)

func TestContentEDeterministico(t *testing.T) {
	// A premissa de que os três edges servem o mesmo objeto depende disto.
	a, err := Content("obj-64KiB-1.bin")
	if err != nil {
		t.Fatalf("primeira geração: %v", err)
	}
	b, err := Content("obj-64KiB-1.bin")
	if err != nil {
		t.Fatalf("segunda geração: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("o mesmo nome gerou conteúdos diferentes")
	}
}

func TestNomesDiferentesGeramConteudosDiferentes(t *testing.T) {
	a, _ := Content("obj-1KiB-1.bin")
	b, _ := Content("obj-1KiB-2.bin")
	if bytes.Equal(a, b) {
		t.Fatal("nomes diferentes geraram o mesmo conteúdo")
	}
}

func TestSizeOf(t *testing.T) {
	casos := map[string]int64{
		"obj-1KiB-1.bin":   1 << 10,
		"obj-64KiB-9.bin":  64 << 10,
		"obj-256KiB-3.bin": 256 << 10,
		"obj-1MiB-2.bin":   1 << 20,
		"sem-tamanho.bin":  DefaultSize,
	}
	for nome, quer := range casos {
		if got := SizeOf(nome); got != quer {
			t.Errorf("SizeOf(%q) = %d, queria %d", nome, got, quer)
		}
	}
}

func TestNomesRecusados(t *testing.T) {
	for _, nome := range []string{"", ".", "..", "a/b", "../etc/passwd", "com espaço", "nome\x00nulo"} {
		if ValidName(nome) {
			t.Errorf("ValidName(%q) aceitou um nome que deveria recusar", nome)
		}
		if _, err := Content(nome); err == nil {
			t.Errorf("Content(%q) gerou conteúdo para um nome inválido", nome)
		}
	}
}

func TestETagEstavel(t *testing.T) {
	if ETag("obj-1KiB-1.bin") != ETag("obj-1KiB-1.bin") {
		t.Fatal("ETag mudou entre duas chamadas para o mesmo nome")
	}
	if ETag("obj-1KiB-1.bin") == ETag("obj-1KiB-2.bin") {
		t.Fatal("nomes diferentes produziram o mesmo ETag")
	}
}
