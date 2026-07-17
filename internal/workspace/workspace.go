package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Workspace struct {
	root string
}

func Open(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace symlinks: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, fmt.Errorf("stat workspace: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace %q is not a directory", real)
	}
	return &Workspace{root: filepath.Clean(real)}, nil
}

func (w *Workspace) Root() string { return w.root }

// Resolve confines a relative or absolute path to the workspace. Existing
// symlinks are resolved to prevent reads and writes from escaping the root.
func (w *Workspace) Resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(w.root, candidate)
	}
	candidate = filepath.Clean(candidate)

	resolved := candidate
	if real, err := filepath.EvalSymlinks(candidate); err == nil {
		resolved = real
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	} else {
		parent := filepath.Dir(candidate)
		realParent, parentErr := filepath.EvalSymlinks(parent)
		if parentErr != nil {
			return "", fmt.Errorf("resolve parent of %q: %w", path, parentErr)
		}
		resolved = filepath.Join(realParent, filepath.Base(candidate))
	}

	rel, err := filepath.Rel(w.root, resolved)
	if err != nil {
		return "", fmt.Errorf("check workspace boundary: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes workspace %q", path, w.root)
	}
	return resolved, nil
}

func (w *Workspace) Relative(path string) string {
	rel, err := filepath.Rel(w.root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}
