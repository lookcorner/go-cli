package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	embeddingMaxRetries     = 3
	embeddingInitialBackoff = time.Second
	embeddingDefaultBatch   = 32
)

// APIEmbeddingProvider calls an OpenAI-compatible POST /embeddings endpoint.
type APIEmbeddingProvider struct {
	BaseURL    string
	APIKey     string
	Model      string
	Dims       int
	HTTPClient *http.Client
	MaxBatch   int
}

// NewAPIEmbeddingProvider builds a provider when cfg.Model is set.
func NewAPIEmbeddingProvider(cfg EmbeddingConfig, baseURL, apiKey string, httpClient *http.Client) (*APIEmbeddingProvider, bool) {
	if cfg.Model == nil || strings.TrimSpace(*cfg.Model) == "" {
		return nil, false
	}
	dims := cfg.Dimensions
	if dims <= 0 {
		dims = 1024
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &APIEmbeddingProvider{
		BaseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:     apiKey,
		Model:      strings.TrimSpace(*cfg.Model),
		Dims:       dims,
		HTTPClient: httpClient,
		MaxBatch:   embeddingDefaultBatch,
	}, true
}

func (p *APIEmbeddingProvider) ModelName() string { return p.Model }
func (p *APIEmbeddingProvider) Dimensions() int {
	if p.Dims > 0 {
		return p.Dims
	}
	return 1024
}

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
}

func (p *APIEmbeddingProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if p == nil {
		return nil, fmt.Errorf("nil embedding provider")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	batch := p.MaxBatch
	if batch < 1 {
		batch = embeddingDefaultBatch
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batch {
		end := start + batch
		if end > len(texts) {
			end = len(texts)
		}
		part, err := p.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}

func (p *APIEmbeddingProvider) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embeddingRequest{
		Model: p.Model, Input: texts, Dimensions: p.Dimensions(),
	})
	if err != nil {
		return nil, err
	}
	url := p.BaseURL + "/embeddings"
	var lastErr error
	for attempt := 0; attempt < embeddingMaxRetries; attempt++ {
		if attempt > 0 {
			delay := embeddingInitialBackoff * time.Duration(1<<(attempt-1))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if p.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.APIKey)
		}
		resp, err := p.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("embedding API HTTP %d: %s", resp.StatusCode, truncateErr(payload))
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("embedding API HTTP %d: %s", resp.StatusCode, truncateErr(payload))
		}
		var parsed embeddingResponse
		if err := json.Unmarshal(payload, &parsed); err != nil {
			return nil, err
		}
		if len(parsed.Data) != len(texts) {
			return nil, fmt.Errorf("embedding API returned %d vectors for %d inputs", len(parsed.Data), len(texts))
		}
		// Preserve input order via index when present.
		ordered := make([][]float32, len(texts))
		for _, item := range parsed.Data {
			idx := item.Index
			if idx < 0 || idx >= len(ordered) {
				return nil, fmt.Errorf("embedding index %d out of range", idx)
			}
			vec := make([]float32, len(item.Embedding))
			for i, v := range item.Embedding {
				vec[i] = float32(v)
			}
			ordered[idx] = vec
		}
		for i, vec := range ordered {
			if vec == nil {
				return nil, fmt.Errorf("embedding missing index %d", i)
			}
		}
		return ordered, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("embedding API failed")
	}
	return nil, fmt.Errorf("embedding API failed after %d attempts: %w", embeddingMaxRetries, lastErr)
}

func truncateErr(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if len(text) > 240 {
		return text[:240] + "..."
	}
	return text
}
