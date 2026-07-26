package originsrv

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// BenchmarkWork mede o custo de /work por número de iterações. Este
// resultado alimenta o modelo de capacidade: réplicas = teto(rps_premissa
// / capacidade_medida_por_réplica / utilização_alvo). Rodar com
// `go test -bench=Work -benchtime=3s ./internal/originsrv/...`.
func BenchmarkWork(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			s, _ := newTestServer(b, Config{Warmup: time.Hour, MaxWork: n * 10, ObjectSize: 128})
			h := s.Handler()
			path := fmt.Sprintf("/work?n=%d", n)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != http.StatusOK {
					b.Fatalf("status inesperado: %d", w.Code)
				}
			}
		})
	}
}
