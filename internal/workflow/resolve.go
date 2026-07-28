package workflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolved is one workflow script ready for validation or launch.
type Resolved struct {
	Name        string
	Description string
	Source      string
	Path        string // empty for builtin until script is embedded
	Script      string // source text when available
}

// ResolveByName finds a listed workflow by name for the given cwd.
func ResolveByName(cwd, name string) (Resolved, error) {
	name = strings.TrimSpace(name)
	if !validWorkflowName(name) {
		return Resolved{}, fmt.Errorf("invalid workflow name %q", name)
	}
	for _, item := range List(cwd) {
		if item.Name != name {
			continue
		}
		resolved := Resolved{
			Name:        item.Name,
			Description: item.Description,
			Source:      item.Source,
		}
		if item.Path != nil {
			resolved.Path = *item.Path
			data, err := os.ReadFile(*item.Path)
			if err != nil {
				return Resolved{}, err
			}
			if int64(len(data)) > maxWorkflowSourceBytes {
				return Resolved{}, errors.New("workflow script exceeds size limit")
			}
			resolved.Script = string(data)
		}
		return resolved, nil
	}
	return Resolved{}, fmt.Errorf("workflow %q not found", name)
}

// ResolvePath loads a .rhai file (must match stem meta name).
func ResolvePath(path string) (Resolved, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return Resolved{}, errors.New("empty workflow path")
	}
	listing, ok := loadListing(path, "path")
	if !ok {
		return Resolved{}, fmt.Errorf("invalid workflow script at %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Name:        listing.Name,
		Description: listing.Description,
		Source:      listing.Source,
		Path:        path,
		Script:      string(data),
	}, nil
}

// ValidateScript checks meta + lightweight structure without a Rhai VM.
func ValidateScript(script string) error {
	if strings.TrimSpace(script) == "" {
		return errors.New("empty workflow script")
	}
	if len(script) > maxWorkflowSourceBytes {
		return errors.New("workflow script exceeds size limit")
	}
	meta, ok := parseMeta(script)
	if !ok {
		return errors.New("workflow meta map missing or invalid")
	}
	if !validWorkflowName(meta.name) {
		return fmt.Errorf("invalid workflow name %q", meta.name)
	}
	if strings.Count(script, "{") != strings.Count(script, "}") {
		return errors.New("unbalanced braces in workflow script")
	}
	return nil
}

// ValidateResolved validates a resolved workflow (builtins are meta-only OK).
func ValidateResolved(resolved Resolved) error {
	if resolved.Source == "builtin" && resolved.Script == "" {
		if !validWorkflowName(resolved.Name) || strings.TrimSpace(resolved.Description) == "" {
			return errors.New("invalid builtin workflow meta")
		}
		return nil
	}
	return ValidateScript(resolved.Script)
}
