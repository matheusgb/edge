package objects

import (
	"bytes"
	"testing"
)

func TestContentEhDeterministico(t *testing.T) {
	a, err := Content("img-64KiB-x.bin")
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	b, err := Content("img-64KiB-x.bin")
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("o mesmo nome produziu conteúdos diferentes")
	}
	if int64(len(a)) != 64<<10 {
		t.Fatalf("tamanho = %d, esperava 65536", len(a))
	}
}

func TestNomesDiferentesProduzemConteudosDiferentes(t *testing.T) {
	a, _ := Content("img-1KiB-a.bin")
	b, _ := Content("img-1KiB-b.bin")
	if bytes.Equal(a, b) {
		t.Fatal("nomes diferentes produziram o mesmo conteúdo")
	}
}

func TestSizeOf(t *testing.T) {
	cases := map[string]int64{
		"img-1KiB-a.bin":   1 << 10,
		"seg-1MiB-z.bin":   1 << 20,
		"seg-256KiB-z.bin": 256 << 10,
		"sem-tamanho.bin":  DefaultSize,
	}
	for name, want := range cases {
		if got := SizeOf(name); got != want {
			t.Errorf("SizeOf(%q) = %d, esperava %d", name, got, want)
		}
	}
}

// A origem recusa nomes que não sejam simples. É defesa em profundidade: se a
// CDN errar a normalização do caminho, o traversal ainda morre aqui.
func TestNomesPerigososSaoRecusados(t *testing.T) {
	for _, name := range []string{"", "..", "../etc/passwd", "a/b.bin", "a b.bin", "a%2e%2e.bin"} {
		if ValidName(name) {
			t.Errorf("ValidName(%q) = true, esperava false", name)
		}
		if _, err := Content(name); err == nil {
			t.Errorf("Content(%q) devia falhar", name)
		}
	}
}

func TestETagEstavelEDistinto(t *testing.T) {
	if ETag("a.bin") != ETag("a.bin") {
		t.Fatal("ETag não é estável para o mesmo nome")
	}
	if ETag("a.bin") == ETag("b.bin") {
		t.Fatal("nomes diferentes produziram o mesmo ETag")
	}
}
