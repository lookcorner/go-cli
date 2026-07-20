package memory

type Config struct {
	Enabled          bool        `json:"enabled"`
	InitialInjection bool        `json:"initial_injection"`
	Flush            FlushConfig `json:"flush"`
}

type FlushConfig struct {
	Enabled             bool   `json:"enabled"`
	SoftThresholdTokens int    `json:"soft_threshold_tokens"`
	Model               string `json:"flush_model,omitempty"`
	MaxWriteChars       int    `json:"max_flush_write_chars"`
}

func DefaultConfig() Config {
	return Config{
		InitialInjection: true,
		Flush:            FlushConfig{Enabled: true, SoftThresholdTokens: 4000, MaxWriteChars: 8000},
	}
}
