package theme

import "testing"

func TestCanonical(t *testing.T) {
	for input, want := range map[string]string{
		"AUTO": "auto", "system": "auto", "dark": "groknight", "grok-day": "grokday",
		"tokyo": "tokyonight", "rose-pine": "rosepine-moon", "oscura": "oscura-midnight",
	} {
		if got, ok := Canonical(input); !ok || got != want {
			t.Fatalf("Canonical(%q)=%q,%v want %q,true", input, got, ok, want)
		}
	}
	if _, ok := Canonical("unknown"); ok {
		t.Fatal("unknown theme was accepted")
	}
}
