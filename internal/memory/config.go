package memory

const defaultRecencyDecay = 0.95

type Config struct {
	Enabled                  bool         `json:"enabled"`
	InitialInjection         bool         `json:"initial_injection"`
	InitialInjectionMinScore *float64     `json:"initial_injection_min_score,omitempty"`
	SaveOnEnd                bool         `json:"save_on_end"`
	Flush                    FlushConfig  `json:"flush"`
	Index                    IndexConfig  `json:"index"`
	Search                   SearchConfig `json:"search"`
	GC                       GCConfig     `json:"gc"`
	Dream                    DreamConfig  `json:"dream"`
}

type IndexConfig struct {
	MaxChunkChars     int `json:"max_chunk_chars"`
	ChunkOverlapChars int `json:"chunk_overlap_chars"`
}

type SearchConfig struct {
	MaxResults    int                 `json:"max_results"`
	MinScore      float64             `json:"min_score"`
	RecencyDecay  float64             `json:"recency_decay"`
	TemporalDecay TemporalDecayConfig `json:"temporal_decay"`
	MMR           MMRConfig           `json:"mmr"`
	SourceWeights map[string]float64  `json:"source_weights"`
}

type TemporalDecayConfig struct {
	Enabled      bool    `json:"enabled"`
	HalfLifeDays float64 `json:"half_life_days"`
}

type MMRConfig struct {
	Enabled bool    `json:"enabled"`
	Lambda  float64 `json:"lambda"`
}

type GCConfig struct {
	MaxAgeDays uint64 `json:"max_age_days"`
}

type DreamConfig struct {
	Enabled              bool    `json:"enabled"`
	MinHours             uint64  `json:"min_hours"`
	MinSessions          uint64  `json:"min_sessions"`
	StaleLockSeconds     uint64  `json:"stale_lock_secs"`
	CheckIntervalSeconds *uint64 `json:"check_interval_secs,omitempty"`
}

type FlushConfig struct {
	Enabled             bool    `json:"enabled"`
	SoftThresholdTokens int     `json:"soft_threshold_tokens"`
	Model               string  `json:"flush_model,omitempty"`
	MaxWriteChars       int     `json:"max_flush_write_chars"`
	IdleTimeoutSeconds  *uint64 `json:"idle_timeout_secs,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		InitialInjection: true,
		SaveOnEnd:        true,
		Flush:            FlushConfig{Enabled: true, SoftThresholdTokens: 4000, MaxWriteChars: 8000},
		Index:            IndexConfig{MaxChunkChars: 1600, ChunkOverlapChars: 320},
		Search: SearchConfig{
			MaxResults: 6, MinScore: 0.35, RecencyDecay: defaultRecencyDecay,
			TemporalDecay: TemporalDecayConfig{Enabled: true, HalfLifeDays: 7},
			MMR:           MMRConfig{Lambda: 0.7},
			SourceWeights: map[string]float64{"workspace": 1, "session": 1, "global": 1},
		},
		GC:    GCConfig{MaxAgeDays: 30},
		Dream: DreamConfig{Enabled: true, MinHours: 4, MinSessions: 3, StaleLockSeconds: 3600},
	}
}
