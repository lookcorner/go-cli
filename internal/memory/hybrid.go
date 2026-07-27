package memory

import "math"

// DefaultSemanticDedupThreshold matches Rust SEMANTIC_DEDUP_SIMILARITY_THRESHOLD.
const DefaultSemanticDedupThreshold = 0.92

// MaxEmbeddingL2Distance is the theoretical max L2 between unit-norm vectors.
const MaxEmbeddingL2Distance = 2.0

// SemanticDedupKNNLimit matches Rust SEMANTIC_DEDUP_KNN_LIMIT.
const SemanticDedupKNNLimit = 3

// VecNeighbor is one KNN hit (chunk id + L2 distance).
type VecNeighbor struct {
	ChunkID  string
	Distance float64
}

// EffectiveSemanticDedupThreshold returns the flush cosine cutoff.
// Nil/out-of-range values fall back to 0.92 and are clamped to [0, 1].
func EffectiveSemanticDedupThreshold(threshold *float64) float64 {
	if threshold == nil {
		return DefaultSemanticDedupThreshold
	}
	return min(1, max(0, *threshold))
}

// DistanceToSimilarity maps unit-norm L2 distance to cosine-like similarity in [0,1].
func DistanceToSimilarity(distance float64) float64 {
	return min(1, max(0, 1-distance/MaxEmbeddingL2Distance))
}

// NormalizeFTSRanks maps FTS5 BM25 ranks (more negative = better) to [0,1].
func NormalizeFTSRanks(ranks map[string]float64) map[string]float64 {
	out := map[string]float64{}
	if len(ranks) == 0 {
		return out
	}
	minRank, maxRank := math.Inf(1), math.Inf(-1)
	for _, rank := range ranks {
		if rank < minRank {
			minRank = rank
		}
		if rank > maxRank {
			maxRank = rank
		}
	}
	span := maxRank - minRank
	if span < math.SmallestNonzeroFloat64 {
		span = math.SmallestNonzeroFloat64
	}
	for id, rank := range ranks {
		// Best (most negative) → 1.0
		out[id] = 1 - (rank-minRank)/span
	}
	return out
}

// MergeHybridScores combines normalized FTS and vector similarities.
// FTS-only chunks keep full FTS score; both-signal chunks use
// max(fts, text_weight*fts + vector_weight*vec); vector-only uses vector_weight*vec.
func MergeHybridScores(fts, vec map[string]float64, textWeight, vectorWeight float64) map[string]float64 {
	textWeight = min(1, max(0, textWeight))
	vectorWeight = min(1, max(0, vectorWeight))
	ids := map[string]struct{}{}
	for id := range fts {
		ids[id] = struct{}{}
	}
	for id := range vec {
		ids[id] = struct{}{}
	}
	out := make(map[string]float64, len(ids))
	for id := range ids {
		f := fts[id]
		v := vec[id]
		switch {
		case f > 0 && v > 0:
			hybrid := textWeight*f + vectorWeight*v
			out[id] = math.Max(hybrid, f)
		case f > 0:
			out[id] = f
		default:
			out[id] = vectorWeight * v
		}
	}
	return out
}

// IsSemanticallyDuplicate reports whether any neighbor exceeds threshold cosine
// similarity. Empty neighbors or missing hits fail open (not duplicate).
func IsSemanticallyDuplicate(neighbors []VecNeighbor, threshold float64) bool {
	threshold = min(1, max(0, threshold))
	for _, neighbor := range neighbors {
		if DistanceToSimilarity(neighbor.Distance) > threshold {
			return true
		}
	}
	return false
}
