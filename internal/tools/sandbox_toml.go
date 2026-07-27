package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"
)

// SandboxTOMLConfig is the on-disk shape of sandbox.toml.
type SandboxTOMLConfig struct {
	Profiles map[string]SandboxTOMLProfile `toml:"profiles"`
}

// SandboxTOMLProfile is one custom profile entry.
type SandboxTOMLProfile struct {
	Extends         *string  `toml:"extends"`
	RestrictNetwork *bool    `toml:"restrict_network"`
	ReadOnly        []string `toml:"read_only"`
	ReadWrite       []string `toml:"read_write"`
	Deny            []string `toml:"deny"`
}

// ResolvedSandboxProfile is a fully expanded profile for Landlock / child wrap.
type ResolvedSandboxProfile struct {
	Name            string
	ChildBase       SandboxProfile // built-in used for child bwrap/Seatbelt mounts
	DefaultRead     bool
	ReadOnly        []string
	ReadWrite       []string
	Deny            []string
	RestrictNetwork bool
	Custom          bool
}

// IsBuiltinSandboxProfile reports whether name is a built-in profile token.
func IsBuiltinSandboxProfile(profile SandboxProfile) bool {
	switch profile {
	case "", SandboxOff, SandboxWorkspace, SandboxReadOnly, SandboxStrict:
		return true
	default:
		return false
	}
}

// LoadSandboxTOML loads $GROK_HOME/sandbox.toml then <workspace>/.grok/sandbox.toml.
// Project entries are additive only (cannot replace a globally defined name).
func LoadSandboxTOML(workspace string) SandboxTOMLConfig {
	config := SandboxTOMLConfig{Profiles: map[string]SandboxTOMLProfile{}}
	if home, err := resolveGrokHome(); err == nil {
		if global := loadSandboxTOMLFile(filepath.Join(home, "sandbox.toml")); global != nil {
			config = *global
			if config.Profiles == nil {
				config.Profiles = map[string]SandboxTOMLProfile{}
			}
		}
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	if project := loadSandboxTOMLFile(filepath.Join(workspace, ".grok", "sandbox.toml")); project != nil {
		mergeProjectSandboxProfiles(&config, *project)
	}
	return config
}

func loadSandboxTOMLFile(path string) *SandboxTOMLConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var config SandboxTOMLConfig
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil
	}
	if config.Profiles == nil {
		config.Profiles = map[string]SandboxTOMLProfile{}
	}
	return &config
}

func mergeProjectSandboxProfiles(dst *SandboxTOMLConfig, project SandboxTOMLConfig) {
	if dst.Profiles == nil {
		dst.Profiles = map[string]SandboxTOMLProfile{}
	}
	for name, profile := range project.Profiles {
		if _, exists := dst.Profiles[name]; exists {
			continue
		}
		dst.Profiles[name] = profile
	}
}

// ResolveSandboxProfile expands a built-in or custom profile name for workspace.
func ResolveSandboxProfile(name, workspace string) (ResolvedSandboxProfile, error) {
	profile, err := ParseSandboxProfile(name)
	if err != nil {
		return ResolvedSandboxProfile{}, err
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	config := LoadSandboxTOML(workspace)
	return resolveSandboxProfile(profile, workspace, config)
}

func resolveSandboxProfile(profile SandboxProfile, workspace string, config SandboxTOMLConfig) (ResolvedSandboxProfile, error) {
	if profile == "" || profile == SandboxOff {
		return ResolvedSandboxProfile{Name: "off", ChildBase: SandboxOff}, nil
	}
	if IsBuiltinSandboxProfile(profile) {
		return resolveBuiltinSandboxProfile(profile, workspace)
	}
	return resolveCustomSandboxProfile(string(profile), workspace, config)
}

func resolveBuiltinSandboxProfile(profile SandboxProfile, workspace string) (ResolvedSandboxProfile, error) {
	switch profile {
	case SandboxWorkspace:
		return ResolvedSandboxProfile{
			Name:            "workspace",
			ChildBase:       SandboxWorkspace,
			DefaultRead:     true,
			ReadWrite:       sandboxWritableMountPaths(SandboxWorkspace, workspace),
			RestrictNetwork: false,
		}, nil
	case SandboxReadOnly:
		return ResolvedSandboxProfile{
			Name:            "read-only",
			ChildBase:       SandboxReadOnly,
			DefaultRead:     true,
			ReadWrite:       sandboxWritableMountPaths(SandboxReadOnly, workspace),
			RestrictNetwork: true,
		}, nil
	case SandboxStrict:
		return ResolvedSandboxProfile{
			Name:            "strict",
			ChildBase:       SandboxStrict,
			DefaultRead:     false,
			ReadOnly:        sandboxReadablePaths(SandboxStrict, workspace),
			ReadWrite:       sandboxWritableMountPaths(SandboxStrict, workspace),
			RestrictNetwork: true,
		}, nil
	default:
		return ResolvedSandboxProfile{}, fmt.Errorf("unsupported sandbox profile %q", profile)
	}
}

func resolveCustomSandboxProfile(name, workspace string, config SandboxTOMLConfig) (ResolvedSandboxProfile, error) {
	entry, ok := config.Profiles[name]
	if !ok {
		return ResolvedSandboxProfile{}, fmt.Errorf(
			"custom sandbox profile %q not found; define it in $GROK_HOME/sandbox.toml or .grok/sandbox.toml",
			name,
		)
	}
	baseName := "workspace"
	if entry.Extends != nil && strings.TrimSpace(*entry.Extends) != "" {
		baseName = strings.ToLower(strings.TrimSpace(*entry.Extends))
	}
	base, err := ParseSandboxProfile(baseName)
	if err != nil || !IsBuiltinSandboxProfile(base) || base == SandboxOff {
		if baseName == "devbox" {
			return ResolvedSandboxProfile{}, fmt.Errorf("custom profile %q extends unsupported base %q (Go supports workspace, read-only, strict)", name, baseName)
		}
		if baseName == "off" || baseName == "none" {
			return ResolvedSandboxProfile{}, fmt.Errorf("custom profile %q cannot extend %q", name, baseName)
		}
		return ResolvedSandboxProfile{}, fmt.Errorf("custom profile %q extends invalid base %q (only built-ins)", name, baseName)
	}
	resolved, err := resolveBuiltinSandboxProfile(base, workspace)
	if err != nil {
		return ResolvedSandboxProfile{}, err
	}
	resolved.Name = name
	resolved.Custom = true
	if entry.RestrictNetwork != nil {
		resolved.RestrictNetwork = *entry.RestrictNetwork
	}
	for _, path := range entry.ReadOnly {
		if cleaned := resolveSandboxPath(path, workspace); cleaned != "" {
			resolved.ReadOnly = append(resolved.ReadOnly, cleaned)
		}
	}
	for _, path := range entry.ReadWrite {
		if cleaned := resolveSandboxPath(path, workspace); cleaned != "" {
			resolved.ReadWrite = append(resolved.ReadWrite, cleaned)
		}
	}
	for _, path := range entry.Deny {
		if cleaned := resolveSandboxPath(path, workspace); cleaned != "" {
			resolved.Deny = append(resolved.Deny, cleaned)
		}
	}
	return resolved, nil
}

func resolveSandboxPath(path, workspace string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	return filepath.Clean(path)
}

func validCustomSandboxName(name string) bool {
	if name == "" || IsBuiltinSandboxProfile(SandboxProfile(name)) {
		return false
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}
