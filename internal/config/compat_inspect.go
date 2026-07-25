package config

import (
	"strings"

	"github.com/lookcorner/go-cli/internal/compat"
)

type CompatibilitySetting struct {
	Vendor  string `json:"vendor"`
	Surface string `json:"surface"`
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"`
}

func (c Config) CompatibilitySettings() []CompatibilitySetting {
	type vendorConfig struct {
		name       string
		resolved   compat.Vendor
		configured compat.Vendor
		surfaces   []string
	}
	vendors := []vendorConfig{
		{name: "cursor", resolved: c.Compat.Cursor, configured: c.compatConfigured.Cursor, surfaces: []string{"skills", "rules", "agents", "mcps", "hooks", "sessions"}},
		{name: "claude", resolved: c.Compat.Claude, configured: c.compatConfigured.Claude, surfaces: []string{"skills", "rules", "agents", "mcps", "hooks", "sessions"}},
		{name: "codex", resolved: c.Compat.Codex, configured: c.compatConfigured.Codex, surfaces: []string{"sessions"}},
	}
	result := make([]CompatibilitySetting, 0, 13)
	for _, vendor := range vendors {
		for _, surface := range vendor.surfaces {
			source := "default"
			if _, ok := envBool(compatEnvName(vendor.name, surface)); ok {
				source = "env"
			} else if compatValue(vendor.configured, surface) {
				source = "config"
			}
			result = append(result, CompatibilitySetting{
				Vendor: vendor.name, Surface: surface, Enabled: compatValue(vendor.resolved, surface), Source: source,
			})
		}
	}
	return result
}

func compatValue(vendor compat.Vendor, surface string) bool {
	switch surface {
	case "skills":
		return vendor.Skills
	case "rules":
		return vendor.Rules
	case "agents":
		return vendor.Agents
	case "mcps":
		return vendor.Mcps
	case "hooks":
		return vendor.Hooks
	case "sessions":
		return vendor.Sessions
	default:
		return false
	}
}

func compatEnvName(vendor, surface string) string {
	return "GROK_" + strings.ToUpper(vendor) + "_" + strings.ToUpper(surface) + "_ENABLED"
}
