package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lookcorner/go-cli/internal/workspace"
)

const (
	maxSimilarPaths      = 3
	minSimilarLeafLength = 2
	minReverseStemLength = 4
)

type pathNotFoundError struct {
	message string
}

func (e *pathNotFoundError) Error() string { return e.message }
func (e *pathNotFoundError) Unwrap() error { return os.ErrNotExist }

func resolveToolPath(ws *workspace.Workspace, displayPath string, hints bool) (string, error) {
	resolved, err := ws.Resolve(displayPath)
	if err == nil || !hints {
		return resolved, err
	}
	if filepath.IsAbs(displayPath) {
		requested := canonicalMissingPath(displayPath)
		if _, statErr := os.Stat(requested); errors.Is(statErr, os.ErrNotExist) {
			if suggestion := suggestUnderWorkspace(requested, ws.Root()); suggestion != "" {
				return "", newPathNotFoundError(displayPath, requested, ws.Root(), suggestion)
			}
		}
		return "", err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	requested := filepath.Join(ws.Root(), filepath.Clean(displayPath))
	if !pathWithin(ws.Root(), requested) {
		if suggestion := suggestUnderWorkspace(requested, ws.Root()); suggestion != "" {
			return "", newPathNotFoundError(displayPath, requested, ws.Root(), suggestion)
		}
		return "", err
	}
	if _, statErr := os.Stat(requested); !errors.Is(statErr, os.ErrNotExist) {
		return "", err
	}
	return "", newPathNotFoundError(displayPath, requested, ws.Root(), "")
}

func canonicalMissingPath(path string) string {
	path = filepath.Clean(path)
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return path
	}
	return filepath.Join(parent, filepath.Base(path))
}

func enrichPathNotFound(displayPath, resolvedPath string, ws *workspace.Workspace, err error, hints bool) error {
	if !hints || !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return newPathNotFoundError(displayPath, resolvedPath, ws.Root(), "")
}

func newPathNotFoundError(displayPath, resolvedPath, cwd, suggestion string) error {
	message := fmt.Sprintf("Error: %s does not exist.", displayPath)
	if suggestion != "" {
		message += " Did you mean " + filepath.ToSlash(suggestion) + "?"
	} else if similar := findSimilarPaths(resolvedPath); len(similar) > 0 {
		message += "\nSimilar entries in parent directory: " + strings.Join(similar, ", ")
	}
	message += "\nNote: your current working directory is " + filepath.ToSlash(cwd)
	return &pathNotFoundError{message: message}
}

func suggestUnderWorkspace(path, cwd string) string {
	if !filepath.IsAbs(path) || pathWithin(cwd, path) {
		return ""
	}
	parent := filepath.Dir(cwd)
	relative, err := filepath.Rel(parent, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ""
	}
	first := strings.Split(relative, string(filepath.Separator))[0]
	sibling := filepath.Join(parent, first)
	if sibling != cwd {
		if _, err := os.Stat(sibling); err == nil {
			return ""
		}
	}
	candidate := filepath.Join(cwd, relative)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func findSimilarPaths(path string) []string {
	leaf := filepath.Base(path)
	if len(leaf) < minSimilarLeafLength {
		return nil
	}
	base := strings.ToLower(leaf)
	baseStem := strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base)))
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil
	}
	similar := make([]string, 0, maxSimilarPaths)
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if name == base {
			continue
		}
		stem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
		forward := strings.Contains(stem, baseStem)
		reverse := !forward && len(stem) >= minReverseStemLength && strings.Contains(baseStem, stem)
		if forward || reverse {
			similar = append(similar, entry.Name())
			if len(similar) == maxSimilarPaths {
				break
			}
		}
	}
	return similar
}
