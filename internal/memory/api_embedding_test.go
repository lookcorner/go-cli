package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIEmbeddingProviderEmbedsAndRetries(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate"}`))
			return
		}
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Model != "text-embedding-3-small" || req.Dimensions != 4 || len(req.Input) != 2 {
			t.Fatalf("req=%#v", req)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float64{0.5, 0.5, 0.5, 0.5}},
				{"index": 0, "embedding": []float64{1, 0, 0, 0}},
			},
		})
	}))
	defer server.Close()

	model := "text-embedding-3-small"
	provider, ok := NewAPIEmbeddingProvider(
		EmbeddingConfig{Model: &model, Dimensions: 4},
		server.URL, "secret", server.Client(),
	)
	if !ok {
		t.Fatal("expected provider")
	}
	vectors, err := provider.Embed(t.Context(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][0] != 0.5 {
		t.Fatalf("attempts=%d vectors=%v", attempts, vectors)
	}
}

func TestNewAPIEmbeddingProviderRequiresModel(t *testing.T) {
	if _, ok := NewAPIEmbeddingProvider(EmbeddingConfig{}, "https://api.example", "k", nil); ok {
		t.Fatal("empty model should skip provider")
	}
}

func TestAPIEmbeddingProviderNonRetryableError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad input"}`))
	}))
	defer server.Close()
	model := "embed"
	provider, _ := NewAPIEmbeddingProvider(EmbeddingConfig{Model: &model, Dimensions: 2}, server.URL, "", server.Client())
	_, err := provider.Embed(t.Context(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err=%v", err)
	}
}
