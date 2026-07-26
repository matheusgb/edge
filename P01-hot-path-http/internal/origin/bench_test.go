package origin

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matheusgb/edge/p01-hot-path-http/internal/catalog"
	"github.com/matheusgb/edge/p01-hot-path-http/internal/metrics"
)

// benchSizes cobre a faixa em que a diferença entre os modos deveria aparecer.
//
// 4 KiB é pequeno o bastante para o custo ser dominado pelo protocolo, não pelo
// corpo. 4 MiB já está bem acima do limite em que o Go promove a alocação para o
// heap grande, onde cada objeto pesa no trabalho do coletor de lixo.
var benchSizes = []int64{4 << 10, 256 << 10, 4 << 20}

// BenchmarkServeObject mede os dois modos para vários tamanhos.
//
// Rode com -benchmem: a coluna B/op é a evidência mais direta da pergunta do lab.
// O esperado é que buffered aloque na ordem do tamanho do objeto por requisição,
// enquanto streamed fique praticamente constante. "Esperado" não é "medido": o
// número manda.
//
//	go test ./internal/origin -bench=BenchmarkServeObject -benchmem -run '^$'
func BenchmarkServeObject(b *testing.B) {
	for _, size := range benchSizes {
		for _, mode := range []Mode{ModeBuffered, ModeStreamed} {
			name := fmt.Sprintf("%s/%s", catalog.NameForSize(size), mode)
			b.Run(name, func(b *testing.B) {
				srv, want := benchServer(b, size, mode)
				url := srv.URL + "/objects/" + catalog.NameForSize(size)
				client := srv.Client()

				b.SetBytes(size)
				b.ReportAllocs()
				b.ResetTimer()

				for b.Loop() {
					resp, err := client.Get(url)
					if err != nil {
						b.Fatalf("requisição: %v", err)
					}
					n, err := io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if err != nil {
						b.Fatalf("lendo corpo: %v", err)
					}
					if n != want {
						b.Fatalf("esperava %d bytes, recebi %d", want, n)
					}
				}
			})
		}
	}
}

// BenchmarkServeObjectParalelo repete a medição com vários clientes ao mesmo
// tempo, que é a condição em que a pergunta do lab realmente se manifesta: uma
// alocação grande isolada é barata; centenas simultâneas não são.
//
//	go test ./internal/origin -bench=Paralelo -benchmem -run '^$' -cpu 1,4,8
func BenchmarkServeObjectParalelo(b *testing.B) {
	const size = 1 << 20

	for _, mode := range []Mode{ModeBuffered, ModeStreamed} {
		b.Run(string(mode), func(b *testing.B) {
			srv, want := benchServer(b, size, mode)
			url := srv.URL + "/objects/" + catalog.NameForSize(size)
			client := srv.Client()

			b.SetBytes(size)
			b.ReportAllocs()
			b.ResetTimer()

			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					resp, err := client.Get(url)
					if err != nil {
						b.Errorf("requisição: %v", err)
						return
					}
					n, err := io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if err != nil {
						b.Errorf("lendo corpo: %v", err)
						return
					}
					if n != want {
						b.Errorf("esperava %d bytes, recebi %d", want, n)
						return
					}
				}
			})
		})
	}
}

func benchServer(b *testing.B, size int64, mode Mode) (*httptest.Server, int64) {
	b.Helper()

	dir := b.TempDir()
	if _, err := catalog.Generate(dir, []int64{size}); err != nil {
		b.Fatalf("gerando catálogo: %v", err)
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		b.Fatalf("carregando catálogo: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(New(cat, metrics.New(), logger, mode).Handler())
	b.Cleanup(srv.Close)

	// O transporte do cliente precisa comportar a concorrência do benchmark,
	// senão medimos abertura de conexão em vez de entrega de objeto.
	if tr, ok := srv.Client().Transport.(*http.Transport); ok {
		tr.MaxIdleConnsPerHost = 256
		tr.MaxConnsPerHost = 256
	}

	return srv, size
}
