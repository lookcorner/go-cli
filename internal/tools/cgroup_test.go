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
