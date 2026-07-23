package agents

import "testing"

func TestDiscoverAppliesAgentToggles(t *testing.T) {
	catalog, errs := Discover(Config{Toggles: map[string]bool{"explore": false}})
	if len(errs) != 0 {
		t.Fatalf("errors=%v", errs)
	}
	definition, ok := catalog.ByName("explore")
	if !ok || definition.Enabled {
		t.Fatalf("definition=%#v found=%v", definition, ok)
	}
	definition, ok = catalog.ByName("plan")
	if !ok || !definition.Enabled {
		t.Fatalf("untoggled definition=%#v found=%v", definition, ok)
	}
}
