package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const mcpPreferencesVersion = 1

// MCPPreferenceSource records where a setup-required server came from.
type MCPPreferenceSource struct {
	Kind   string  `json:"kind"`
	Plugin *string `json:"plugin,omitempty"`
	Scope  *string `json:"scope,omitempty"`
}

// MCPServerPreferences stores select-field answers for one setup-required server.
type MCPServerPreferences struct {
	Values    map[string]string    `json:"values"`
	Source    *MCPPreferenceSource `json:"source,omitempty"`
	UpdatedAt *string              `json:"updatedAt,omitempty"`
}

// MCPPreferencesFile is $GROK_HOME/mcp_preferences.json.
type MCPPreferencesFile struct {
	Version uint32                          `json:"version"`
	Servers map[string]MCPServerPreferences `json:"servers"`
}

// MCPPreferencesLoad distinguishes missing vs corrupt preference files.
type MCPPreferencesLoad int

const (
	MCPPreferencesOK MCPPreferencesLoad = iota
	MCPPreferencesMissing
	MCPPreferencesCorrupt
)

type MCPPreferencesResult struct {
	Status MCPPreferencesLoad
	File   MCPPreferencesFile
}

func (r MCPPreferencesResult) Writable() bool {
	return r.Status != MCPPreferencesCorrupt
}

func MCPPreferencesPath() (string, error) {
	home, err := PolicyHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "mcp_preferences.json"), nil
}

func LoadMCPPreferences() MCPPreferencesResult {
	path, err := MCPPreferencesPath()
	if err != nil {
		return MCPPreferencesResult{Status: MCPPreferencesCorrupt, File: emptyMCPPreferences()}
	}
	return LoadMCPPreferencesAt(path)
}

func LoadMCPPreferencesAt(path string) MCPPreferencesResult {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return MCPPreferencesResult{Status: MCPPreferencesMissing, File: emptyMCPPreferences()}
	}
	if err != nil {
		return MCPPreferencesResult{Status: MCPPreferencesCorrupt, File: emptyMCPPreferences()}
	}
	var file MCPPreferencesFile
	if json.Unmarshal(data, &file) != nil {
		return MCPPreferencesResult{Status: MCPPreferencesCorrupt, File: emptyMCPPreferences()}
	}
	if file.Servers == nil {
		file.Servers = map[string]MCPServerPreferences{}
	}
	if file.Version == 0 {
		file.Version = mcpPreferencesVersion
	}
	return MCPPreferencesResult{Status: MCPPreferencesOK, File: file}
}

func SaveMCPPreferences(file MCPPreferencesFile) error {
	path, err := MCPPreferencesPath()
	if err != nil {
		return err
	}
	return SaveMCPPreferencesAt(path, file)
}

func SaveMCPPreferencesAt(path string, file MCPPreferencesFile) error {
	if LoadMCPPreferencesAt(path).Status == MCPPreferencesCorrupt {
		return fmt.Errorf("MCP preferences file is unreadable; fix or remove mcp_preferences.json before saving")
	}
	if file.Version == 0 {
		file.Version = mcpPreferencesVersion
	}
	if file.Servers == nil {
		file.Servers = map[string]MCPServerPreferences{}
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomic(path, data)
}

func emptyMCPPreferences() MCPPreferencesFile {
	return MCPPreferencesFile{Version: mcpPreferencesVersion, Servers: map[string]MCPServerPreferences{}}
}

// RestoreMCPPreferenceServer rolls back one server entry after a failed setup enable.
func RestoreMCPPreferenceServer(name string, previous *MCPServerPreferences) error {
	path, err := MCPPreferencesPath()
	if err != nil {
		return err
	}
	load := LoadMCPPreferencesAt(path)
	if !load.Writable() {
		return fmt.Errorf("MCP preferences file is unreadable; fix or remove mcp_preferences.json before saving")
	}
	prefs := load.File
	if previous == nil {
		delete(prefs.Servers, name)
	} else {
		prefs.Servers[name] = *previous
	}
	return SaveMCPPreferencesAt(path, prefs)
}

// MCPSetupKind is the outcome of ResolveSetup.
type MCPSetupKind int

const (
	MCPSetupResolved MCPSetupKind = iota
	MCPSetupRequired
	MCPSetupInvalid
)

type MCPSetupResolution struct {
	Kind   MCPSetupKind
	Config MCPServerConfig
	Setup  MCPSetupConfig
	Reason string
}

// ResolveSetup applies stored preferences to a setup schema (Rust v0: one select field).
func (c MCPServerConfig) ResolveSetup(prefs *MCPServerPreferences) MCPSetupResolution {
	if c.Setup == nil {
		return MCPSetupResolution{Kind: MCPSetupResolved, Config: c}
	}
	setup := *c.Setup
	if len(setup.Fields) != 1 {
		return MCPSetupResolution{Kind: MCPSetupInvalid, Setup: setup, Reason: "setup schema must declare exactly one select field (v0)"}
	}
	field := setup.Fields[0]
	if !strings.EqualFold(strings.TrimSpace(field.Type), "select") || len(field.Options) == 0 {
		return MCPSetupResolution{Kind: MCPSetupInvalid, Setup: setup, Reason: "setup field must be a non-empty select (v0)"}
	}
	if prefs == nil || prefs.Values == nil {
		return MCPSetupResolution{Kind: MCPSetupRequired, Setup: setup}
	}
	value, ok := prefs.Values[field.ID]
	if !ok {
		return MCPSetupResolution{Kind: MCPSetupRequired, Setup: setup}
	}
	allowed := false
	for _, option := range field.Options {
		if option.Value == value {
			allowed = true
			break
		}
	}
	if !allowed {
		return MCPSetupResolution{Kind: MCPSetupRequired, Setup: setup}
	}
	variables := map[string]string{}
	for name, derived := range setup.Variables {
		if derived.From != field.ID {
			return MCPSetupResolution{
				Kind:   MCPSetupInvalid,
				Setup:  setup,
				Reason: fmt.Sprintf("setup variable '%s' references unknown field '%s'", name, derived.From),
			}
		}
		mapped, ok := derived.Map[value]
		if !ok {
			return MCPSetupResolution{Kind: MCPSetupRequired, Setup: setup}
		}
		variables[name] = mapped
	}
	resolved := c
	resolved.Setup = nil
	if err := renderMCPSetupTemplates(&resolved, variables); err != nil {
		return MCPSetupResolution{Kind: MCPSetupInvalid, Setup: setup, Reason: err.Error()}
	}
	return MCPSetupResolution{Kind: MCPSetupResolved, Config: resolved}
}

func renderMCPSetupTemplates(config *MCPServerConfig, variables map[string]string) error {
	var err error
	if config.Command, err = renderSetupTemplate(config.Command, variables); err != nil {
		return err
	}
	if config.URL, err = renderSetupTemplate(config.URL, variables); err != nil {
		return err
	}
	for i := range config.Args {
		if config.Args[i], err = renderSetupTemplate(config.Args[i], variables); err != nil {
			return err
		}
	}
	for key, value := range config.Env {
		if config.Env[key], err = renderSetupTemplate(value, variables); err != nil {
			return err
		}
	}
	for key, value := range config.Headers {
		if config.Headers[key], err = renderSetupTemplate(value, variables); err != nil {
			return err
		}
	}
	return nil
}

func renderSetupTemplate(input string, variables map[string]string) (string, error) {
	var out strings.Builder
	rest := input
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			out.WriteString(rest)
			return out.String(), nil
		}
		out.WriteString(rest[:start])
		rest = rest[start+2:]
		end := strings.Index(rest, "}}")
		if end < 0 {
			return "", errors.New("unterminated setup variable template")
		}
		key := strings.TrimSpace(rest[:end])
		value, ok := variables[key]
		if !ok {
			return "", fmt.Errorf("unresolved setup variable '%s'", key)
		}
		out.WriteString(value)
		rest = rest[end+2:]
	}
}

// ApplyMCPSetupPreferences resolves setup schemas using mcp_preferences.json.
// Unresolved or invalid setup servers keep their Setup field so callers can skip starting them.
func ApplyMCPSetupPreferences(servers map[string]MCPServerConfig) map[string]MCPServerConfig {
	if len(servers) == 0 {
		return servers
	}
	prefs := LoadMCPPreferences().File
	out := cloneMCPServers(servers)
	for name, server := range out {
		if server.Setup == nil {
			continue
		}
		var pref *MCPServerPreferences
		if stored, ok := prefs.Servers[name]; ok {
			copy := stored
			pref = &copy
		}
		switch resolved := server.ResolveSetup(pref); resolved.Kind {
		case MCPSetupResolved:
			out[name] = resolved.Config
		case MCPSetupRequired, MCPSetupInvalid:
			out[name] = server
		}
	}
	return out
}

// NewMCPServerPreferences builds a preference entry for x.ai/mcp/setup.
func NewMCPServerPreferences(values map[string]string, source *MCPPreferenceSource) MCPServerPreferences {
	stamp := time.Now().UTC().Format(time.RFC3339)
	copied := map[string]string{}
	for key, value := range values {
		copied[key] = value
	}
	return MCPServerPreferences{Values: copied, Source: source, UpdatedAt: &stamp}
}

// NeedsSetup reports whether the server still requires interactive setup.
func (c MCPServerConfig) NeedsSetup() bool {
	return c.Setup != nil
}
