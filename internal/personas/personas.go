package personas

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/pelletier/go-toml/v2"
)

type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
	ScopeBundled Scope = "bundled"
)

type Persona struct {
	Name             string
	Description      string
	Instructions     string
	Model            string
	ReasoningEffort  string
	DefaultIsolation string
	Path             string
	Scope            Scope
	HasInputs        bool
	HasOutputs       bool
}

func (p Persona) Editable() bool { return p.Scope == ScopeUser || p.Scope == ScopeProject }

type Draft struct {
	Name, Description, Instructions string
	Scope                           Scope
}

type Service struct {
	workspace string
	home      string
}

type source struct {
	dir   string
	scope Scope
}

func New(workspace string) *Service {
	home := strings.TrimSpace(os.Getenv("GROK_HOME"))
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".grok")
		}
	}
	if home != "" {
		home = filepath.Clean(home)
	}
	return &Service{workspace: filepath.Clean(workspace), home: home}
}

func (s *Service) List() ([]Persona, error) {
	seen := make(map[string]bool)
	var result []Persona
	sources := []source{{filepath.Join(s.workspace, ".grok", "personas"), ScopeProject}}
	if s.home != "" {
		sources = append([]source{{filepath.Join(s.home, "bundled", "personas"), ScopeBundled}}, sources...)
		sources = append(sources, source{filepath.Join(s.home, "personas"), ScopeUser})
	}
	for _, source := range sources {
		if _, err := os.Lstat(source.dir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("inspect personas directory %q: %w", source.dir, err)
		}
		dir, err := s.resolveSourceDir(source, false)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("read personas directory %q: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".toml" {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".toml")
			if seen[name] {
				continue
			}
			path, scope, err := s.validatePath(filepath.Join(dir, entry.Name()), false)
			if err != nil || scope != source.scope {
				continue
			}
			persona, err := readPersona(path, name, source.scope)
			if err != nil {
				continue
			}
			seen[name] = true
			result = append(result, persona)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Scope != result[j].Scope {
			return scopeRank(result[i].Scope) < scopeRank(result[j].Scope)
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (s *Service) Create(draft Draft) (Persona, error) {
	name, err := sanitizeName(draft.Name)
	if err != nil {
		return Persona{}, err
	}
	dir, err := s.writableDir(draft.Scope)
	if err != nil {
		return Persona{}, err
	}
	dir, err = s.resolveSourceDir(source{dir: dir, scope: draft.Scope}, true)
	if err != nil {
		return Persona{}, err
	}
	path := filepath.Join(dir, name+".toml")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return Persona{}, fmt.Errorf("persona %q already exists", name)
		}
		return Persona{}, fmt.Errorf("create persona: %w", err)
	}
	content, marshalErr := toml.Marshal(struct {
		Description  string `toml:"description,omitempty"`
		Instructions string `toml:"instructions,omitempty"`
	}{Description: strings.TrimSpace(draft.Description), Instructions: strings.TrimSpace(draft.Instructions)})
	if marshalErr == nil {
		_, marshalErr = file.Write(content)
	}
	closeErr := file.Close()
	if marshalErr != nil {
		_ = os.Remove(path)
		return Persona{}, fmt.Errorf("write persona: %w", marshalErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return Persona{}, fmt.Errorf("close persona: %w", closeErr)
	}
	return readPersona(path, name, draft.Scope)
}

func (s *Service) Read(path string) (string, error) {
	clean, _, err := s.validatePath(path, false)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return "", fmt.Errorf("read persona: %w", err)
	}
	return string(data), nil
}

func (s *Service) Delete(path string) error {
	clean, scope, err := s.validatePath(path, true)
	if err != nil {
		return err
	}
	if scope == ScopeBundled {
		return errors.New("cannot delete bundled personas")
	}
	if err := os.Remove(clean); err != nil {
		return fmt.Errorf("delete persona: %w", err)
	}
	return nil
}

// Update edits the user/project persona fields while preserving other TOML data.
func (s *Service) Update(path string, draft Draft) (Persona, error) {
	clean, scope, err := s.validatePath(path, true)
	if err != nil {
		return Persona{}, err
	}
	if scope == ScopeBundled {
		return Persona{}, errors.New("cannot edit bundled personas")
	}
	name, err := sanitizeName(draft.Name)
	if err != nil {
		return Persona{}, err
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return Persona{}, fmt.Errorf("read persona: %w", err)
	}
	values := map[string]any{}
	if err := toml.Unmarshal(data, &values); err != nil {
		return Persona{}, fmt.Errorf("parse persona: %w", err)
	}
	values["name"] = name
	values["description"] = strings.TrimSpace(draft.Description)
	values["instructions"] = strings.TrimSpace(draft.Instructions)
	encoded, err := toml.Marshal(values)
	if err != nil {
		return Persona{}, fmt.Errorf("encode persona: %w", err)
	}
	dir := filepath.Dir(clean)
	tmp, err := os.CreateTemp(dir, ".persona-*.toml")
	if err != nil {
		return Persona{}, fmt.Errorf("create persona temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(encoded)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Persona{}, fmt.Errorf("write persona: %w", err)
	}
	target := filepath.Join(dir, name+".toml")
	if target != clean {
		if _, err := os.Stat(target); err == nil {
			return Persona{}, fmt.Errorf("persona %q already exists", name)
		} else if !os.IsNotExist(err) {
			return Persona{}, fmt.Errorf("check persona target: %w", err)
		}
	}
	backupFile, err := os.CreateTemp(dir, ".persona-backup-*")
	if err != nil {
		return Persona{}, fmt.Errorf("create persona backup: %w", err)
	}
	backup := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		_ = os.Remove(backup)
		return Persona{}, fmt.Errorf("close persona backup: %w", err)
	}
	_ = os.Remove(backup)
	if err := os.Rename(clean, backup); err != nil {
		return Persona{}, fmt.Errorf("stage persona replacement: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Rename(backup, clean)
		return Persona{}, fmt.Errorf("replace persona: %w", err)
	}
	_ = os.Remove(backup)
	return readPersona(target, name, scope)
}

func (s *Service) writableDir(scope Scope) (string, error) {
	switch scope {
	case ScopeUser:
		if s.home == "" {
			return "", errors.New("GROK_HOME or a user home is required")
		}
		return filepath.Join(s.home, "personas"), nil
	case ScopeProject:
		return filepath.Join(s.workspace, ".grok", "personas"), nil
	default:
		return "", fmt.Errorf("persona scope %q is not writable", scope)
	}
}

func (s *Service) validatePath(path string, writable bool) (string, Scope, error) {
	clean, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", "", fmt.Errorf("resolve persona path: %w", err)
	}
	info, err := os.Stat(clean)
	if err != nil || !info.Mode().IsRegular() || filepath.Ext(clean) != ".toml" {
		return "", "", errors.New("persona path is not a TOML file")
	}
	var candidates []source
	if s.home != "" {
		candidates = append(candidates, source{filepath.Join(s.home, "bundled", "personas"), ScopeBundled})
	}
	candidates = append(candidates, source{filepath.Join(s.workspace, ".grok", "personas"), ScopeProject})
	if s.home != "" {
		candidates = append(candidates, source{filepath.Join(s.home, "personas"), ScopeUser})
	}
	for _, candidate := range candidates {
		dir, dirErr := s.resolveSourceDir(candidate, false)
		if dirErr == nil && pathWithin(dir, clean) {
			if writable && candidate.scope == ScopeBundled {
				return "", candidate.scope, errors.New("cannot delete bundled personas")
			}
			return clean, candidate.scope, nil
		}
	}
	return "", "", errors.New("persona file is outside known persona directories")
}

func (s *Service) resolveSourceDir(candidate source, create bool) (string, error) {
	root := s.home
	if candidate.scope == ScopeProject {
		root = s.workspace
	}
	if root == "" {
		return "", errors.New("persona root is unavailable")
	}
	if create {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", fmt.Errorf("create persona root: %w", err)
		}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve persona root: %w", err)
	}
	ancestor := candidate.dir
	for {
		if _, err = os.Lstat(ancestor); err == nil {
			break
		}
		if !os.IsNotExist(err) || ancestor == filepath.Dir(ancestor) {
			return "", fmt.Errorf("inspect personas directory: %w", err)
		}
		ancestor = filepath.Dir(ancestor)
	}
	resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil || !pathWithin(resolvedRoot, resolvedAncestor) {
		return "", errors.New("personas directory is outside its configured root")
	}
	if create {
		if err := os.MkdirAll(candidate.dir, 0o700); err != nil {
			return "", fmt.Errorf("create personas directory: %w", err)
		}
	}
	resolvedDir, err := filepath.EvalSymlinks(candidate.dir)
	if err != nil || !pathWithin(resolvedRoot, resolvedDir) {
		return "", errors.New("personas directory is outside its configured root")
	}
	if candidate.scope != ScopeBundled && s.home != "" {
		bundled, bundledErr := filepath.EvalSymlinks(filepath.Join(s.home, "bundled", "personas"))
		if bundledErr == nil && pathWithin(bundled, resolvedDir) {
			return "", errors.New("writable personas directory resolves into bundled personas")
		}
	}
	return resolvedDir, nil
}

func readPersona(path, fallbackName string, scope Scope) (Persona, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Persona{}, err
	}
	var value struct {
		Name             string           `toml:"name"`
		Description      string           `toml:"description"`
		Instructions     string           `toml:"instructions"`
		Model            string           `toml:"model"`
		ReasoningEffort  string           `toml:"reasoning_effort"`
		DefaultIsolation string           `toml:"default_isolation"`
		Inputs           []map[string]any `toml:"inputs"`
		Outputs          []map[string]any `toml:"outputs"`
	}
	if err := toml.Unmarshal(data, &value); err != nil {
		return Persona{}, err
	}
	name := strings.TrimSpace(value.Name)
	if name == "" {
		name = fallbackName
	}
	description := strings.TrimSpace(value.Description)
	if description == "" {
		description = firstParagraph(value.Instructions)
	}
	return Persona{
		Name: name, Description: description, Instructions: value.Instructions,
		Model: value.Model, ReasoningEffort: value.ReasoningEffort, DefaultIsolation: value.DefaultIsolation,
		Path: path, Scope: scope, HasInputs: len(value.Inputs) > 0, HasOutputs: len(value.Outputs) > 0,
	}, nil
}

func sanitizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	var result strings.Builder
	valid := false
	for _, character := range name {
		if unicode.IsLetter(character) || unicode.IsNumber(character) || character == '-' || character == '_' {
			result.WriteRune(character)
			valid = valid || unicode.IsLetter(character) || unicode.IsNumber(character)
		} else {
			result.WriteByte('-')
		}
	}
	if !valid {
		return "", errors.New("persona name must contain a letter or number")
	}
	return result.String(), nil
}

func firstParagraph(value string) string {
	for _, paragraph := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n\n") {
		if paragraph = strings.TrimSpace(paragraph); paragraph != "" {
			return strings.Join(strings.Fields(paragraph), " ")
		}
	}
	return ""
}

func scopeRank(scope Scope) int {
	switch scope {
	case ScopeBundled:
		return 0
	case ScopeProject:
		return 1
	default:
		return 2
	}
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
