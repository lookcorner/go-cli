package memory

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
)

// EmbeddingProvider generates vectors for memory hybrid search / semantic dedup.
type EmbeddingProvider interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	ModelName() string
	Dimensions() int
}

// HashEmbeddingProvider is a deterministic local embedder for tests and
// offline fail-open paths (sha256 bytes → [0,1] floats; not unit-normalized).
type HashEmbeddingProvider struct {
	Model string
	Dims  int
}

func (p HashEmbeddingProvider) ModelName() string {
	if p.Model == "" {
		return "hash-embedding"
	}
	return p.Model
}

func (p HashEmbeddingProvider) Dimensions() int {
	if p.Dims > 0 {
		return p.Dims
	}
	return 1024
}

func (p HashEmbeddingProvider) Embed(_ context.Context, texts []string) ([][]float32, error) {
	dims := p.Dimensions()
	out := make([][]float32, len(texts))
	for i, text := range texts {
		sum := sha256.Sum256([]byte(text))
		vec := make([]float32, dims)
		for j := range vec {
			vec[j] = float32(sum[j%len(sum)]) / 255
		}
		out[i] = vec
	}
	return out, nil
}

// PackEmbedding encodes float32 LE bytes for SQLite BLOB storage.
func PackEmbedding(values []float32) []byte {
	buf := make([]byte, 4*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// UnpackEmbedding decodes float32 LE bytes from SQLite BLOB storage.
func UnpackEmbedding(buf []byte) ([]float32, error) {
	if len(buf)%4 != 0 {
		return nil, fmt.Errorf("embedding blob length %d not multiple of 4", len(buf))
	}
	out := make([]float32, len(buf)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return out, nil
}

func l2Distance(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var sum float64
	for i := 0; i < n; i++ {
		d := float64(a[i] - b[i])
		sum += d * d
	}
	return math.Sqrt(sum)
}
