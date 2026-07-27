package tools

import "testing"

func TestDefaultCgroupMemoryConfig(t *testing.T) {
	cfg := DefaultCgroupMemoryConfig()
	if cfg.MemoryHighBytes != 512<<20 || cfg.HeadroomBytes != 256<<20 {
		t.Fatalf("cfg=%#v", cfg)
	}
	if cfg.memoryMax() != 768<<20 {
		t.Fatalf("memoryMax=%d", cfg.memoryMax())
	}
	zero := CgroupMemoryConfig{}.normalized()
	if zero.MemoryHighBytes != cfg.MemoryHighBytes || zero.HeadroomBytes != cfg.HeadroomBytes {
		t.Fatalf("normalized zero=%#v", zero)
	}
}

func TestParseMemoryEventsHigh(t *testing.T) {
	n, ok := parseMemoryEventsHigh("low 1\nhigh 42\nmax 3\n")
	if !ok || n != 42 {
		t.Fatalf("got %d ok=%v", n, ok)
	}
	if _, ok := parseMemoryEventsHigh("low 1\n"); ok {
		t.Fatal("expected missing high")
	}
}

func TestMemoryHighBufferNinetyPercent(t *testing.T) {
	if memoryHighBuffer(1000) != 900 {
		t.Fatalf("buffer=%d", memoryHighBuffer(1000))
	}
}
