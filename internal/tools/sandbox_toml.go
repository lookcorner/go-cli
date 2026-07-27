package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

// SandboxProfileConflicts returns custom profile names that exist in both the
// global and project sandbox.toml with different definitions. Matches Rust
// sandbox_profile_conflicts (global wins; project cannot redefine).
func SandboxProfileConflicts(workspace string) []string {
	home, err := resolveGrokHome()
	if err != nil {
		home = ""
	}
	var global SandboxTOMLConfig
	if home != "" {
		if loaded := loadSandboxTOMLFile(filepath.Join(home, "sandbox.toml")); loaded != nil {
			global = *loaded
		}
	}
	if global.Profiles == nil {
		global.Profiles = map[string]SandboxTOMLProfile{}
	}

	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	var project SandboxTOMLConfig
	if loaded := loadSandboxTOMLFile(filepath.Join(workspace, ".grok", "sandbox.toml")); loaded != nil {
		project = *loaded
	}
	if project.Profiles == nil {
		project.Profiles = map[string]SandboxTOMLProfile{}
	}
	return mismatchedSandboxProfileNames(global, project)
}

func mismatchedSandboxProfileNames(global, project SandboxTOMLConfig) []string {
	var names []string
	for name, projectProfile := range project.Profiles {
		parsed, err := ParseSandboxProfile(name)
		if err != nil || IsBuiltinSandboxProfile(parsed) {
			continue
		}
		globalProfile, ok := global.Profiles[name]
		if !ok || sandboxTOMLProfilesEqual(globalProfile, projectProfile) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sandboxTOMLProfilesEqual(a, b SandboxTOMLProfile) bool {
	return reflect.DeepEqual(a, b)
}

// FormatSandboxProfileConflictFinding builds the doctor finding text for
// conflicting custom sandbox.toml profiles (Rust sandbox.profile-conflict).
func FormatSandboxProfileConflictFinding(conflicts []string, userSandboxPath string) string {
	if len(conflicts) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(conflicts))
	for _, name := range conflicts {
		quoted = append(quoted, "'"+name+"'")
	}
	if strings.TrimSpace(userSandboxPath) == "" {
		userSandboxPath = "$GROK_HOME/sandbox.toml"
	}
	return fmt.Sprintf(
		"Project and user sandbox settings define these profiles differently: %s\n"+
			"    Gork is using the user profile. Compare `.grok/sandbox.toml` with %s, then rename "+
			"or remove the conflicting project profile. Project settings can add profile names "+
			"but can't redefine a user profile.",
		strings.Join(quoted, ", "),
		userSandboxPath,
	)
}

// SandboxProfileConflictFinding reports a doctor finding for the workspace, or "".
func SandboxProfileConflictFinding(workspace string) string {
	conflicts := SandboxProfileConflicts(workspace)
	if len(conflicts) == 0 {
		return ""
	}
	path := "$GROK_HOME/sandbox.toml"
	if home, err := resolveGrokHome(); err == nil {
		path = filepath.Join(home, "sandbox.toml")
	}
	return FormatSandboxProfileConflictFinding(conflicts, path)
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
