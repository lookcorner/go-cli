package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Workspace struct {
	root       string
	extraRoots []string
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

// WithExtraRoot returns an isolated workspace view that also permits absolute
// paths inside root. Relative paths continue to resolve against the workspace.
func (w *Workspace) WithExtraRoot(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve extra workspace root: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve extra workspace root symlinks: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, fmt.Errorf("stat extra workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("extra workspace root %q is not a directory", real)
	}
	result := &Workspace{root: w.root, extraRoots: append([]string(nil), w.extraRoots...)}
	for _, existing := range append([]string{w.root}, result.extraRoots...) {
		if existing == real {
			return result, nil
		}
	}
	result.extraRoots = append(result.extraRoots, filepath.Clean(real))
	return result, nil
}

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

	if !w.contains(resolved) {
		return "", fmt.Errorf("path %q escapes workspace %q", path, w.root)
	}
	return resolved, nil
}

// ResolveEntry confines a path while preserving the final directory entry.
// Parent symlinks are resolved, but a final symlink is returned for safe lstat.
func (w *Workspace) ResolveEntry(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(w.root, candidate)
	}
	candidate = filepath.Clean(candidate)
	if candidate == w.root {
		return candidate, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil {
		return "", fmt.Errorf("resolve parent of %q: %w", path, err)
	}
	resolved := filepath.Join(parent, filepath.Base(candidate))
	if !w.contains(resolved) {
		return "", fmt.Errorf("path %q escapes workspace %q", path, w.root)
	}
	return resolved, nil
}

func (w *Workspace) contains(path string) bool {
	for _, root := range append([]string{w.root}, w.extraRoots...) {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (w *Workspace) Relative(path string) string {
	rel, err := filepath.Rel(w.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.ToSlash(rel)
}
