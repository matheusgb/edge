package loadtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunStepContaCompletadasESemErro(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	r := NewRunner(srv.URL, 2*time.Second)
	res := r.RunStep(context.Background(), Step{
		Name: "teste", RPS: 50, Duration: 200 * time.Millisecond, Concurrency: 16,
	})

	if res.Errors != 0 {
		t.Fatalf("esperava zero erros, obteve %d", res.Errors)
	}
	if res.Completed == 0 {
		t.Fatal("esperava ao menos uma requisição completada")
	}
	if res.P99Millis < res.P50Millis {
		t.Fatalf("p99 (%f) não pode ser menor que p50 (%f)", res.P99Millis, res.P50Millis)
	}
}

func TestRunStepContaErrosDeServidor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "falha", http.StatusInternalServerError)
	}))
	defer srv.Close()

	r := NewRunner(srv.URL, 2*time.Second)
	res := r.RunStep(context.Background(), Step{
		Name: "erro", RPS: 50, Duration: 200 * time.Millisecond, Concurrency: 8,
	})

	if res.Errors == 0 {
		t.Fatal("esperava erros com status 500")
	}
	if res.ErrorRateRatio <= 0 {
		t.Fatal("taxa de erro deveria ser maior que zero")
	}
}

func TestPercentil(t *testing.T) {
	sorted := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(sorted, 0); got != 1 {
		t.Fatalf("p0 esperado 1, obteve %f", got)
	}
	if got := percentile(sorted, 1); got != 10 {
		t.Fatalf("p100 esperado 10, obteve %f", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Fatalf("percentil de slice vazio deveria ser 0, obteve %f", got)
	}
}

func TestRunStepsExecutaEmOrdem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	r := NewRunner(srv.URL, 2*time.Second)
	steps := []Step{
		{Name: "a", RPS: 20, Duration: 50 * time.Millisecond, Concurrency: 4},
		{Name: "b", RPS: 20, Duration: 50 * time.Millisecond, Concurrency: 4},
	}
	results := r.RunSteps(context.Background(), steps)
	if len(results) != 2 {
		t.Fatalf("esperava 2 resultados, obteve %d", len(results))
	}
	if results[0].Name != "a" || results[1].Name != "b" {
		t.Fatalf("ordem incorreta: %s, %s", results[0].Name, results[1].Name)
	}
}
