package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeManagedRequirements(t *testing.T) {
	raw := []byte("[auth]\npreferred_method = \"oidc\"\n")
	encoded := base64.StdEncoding.EncodeToString(raw)
	wrapped := encoded[:4] + " \n\t" + encoded[4:]
	if got := decodeManagedRequirements(wrapped); string(got) != string(raw) {
		t.Fatalf("decoded MDM requirements=%q", got)
	}
	for _, invalid := range []string{"not base64", base64.StdEncoding.EncodeToString([]byte("[broken")), base64.StdEncoding.EncodeToString([]byte("\xff")), base64.StdEncoding.EncodeToString([]byte(""))} {
		if got := decodeManagedRequirements(invalid); got != nil {
			t.Fatalf("invalid MDM payload was accepted: %q", got)
		}
	}
}

func TestMDMRequirementsHaveHighestPrecedence(t *testing.T) {
	cfg := Config{}
	for _, data := range []struct {
		source string
		data   string
	}{
		{"user", "[auth]\npreferred_method = \"api_key\"\n[[permission.rules]]\naction = \"allow\"\ntool = \"bash\"\n"},
		{"system", "[auth]\npreferred_method = \"api_key\"\n"},
		{mdmRequirementsSource, "[auth]\npreferred_method = \"oidc\"\n[[permission.rules]]\naction = \"deny\"\ntool = \"bash\"\n"},
	} {
		if err := applyRequirementsData(&cfg, []byte(data.data), data.source, false, false); err != nil {
			t.Fatal(err)
		}
	}
	if cfg.PreferredAuthMethod != "oidc" || len(cfg.Permission.Rules) != 1 || cfg.Permission.Rules[0].Action != "deny" {
		t.Fatalf("MDM requirements did not win: %#v", cfg)
	}
}

func TestMDMFailClosedRejectsInvalidVersionOverride(t *testing.T) {
	data := []byte("fail_closed = true\n[[version_overrides]]\nminimum_version = \"v1.0.0\"\n")
	err := applyRequirementsData(&Config{}, data, mdmRequirementsSource, false, false)
	if err == nil || !strings.Contains(err.Error(), mdmRequirementsSource) {
		t.Fatalf("invalid fail-closed MDM policy error=%v", err)
	}
}
