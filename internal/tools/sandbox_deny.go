package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar"
)

const (
	denyGlobMaxDepth   = 64
	denyGlobMaxMatches = 4096
	denyGlobMaxEntries = 200_000
)

// RequiresReadDeny reports whether a custom sandbox.toml profile lists deny paths.
// Matches Rust requires_read_deny: keyed on the TOML deny list, not the resolved set.
func RequiresReadDeny(profile, workspace string) bool {
	parsed, err := ParseSandboxProfile(profile)
	if err != nil || IsBuiltinSandboxProfile(parsed) || parsed == SandboxOff {
		return false
	}
	cfg := LoadSandboxTOML(workspace)
	entry, ok := cfg.Profiles[string(parsed)]
	return ok && len(entry.Deny) > 0
}

// BuildReadDenyTargets resolves custom-profile deny entries into concrete paths
// for Linux bwrap bind-over. Exact paths are workspace-resolved; globs are
// expanded at launch (fail-closed when expansion exceeds Rust-aligned caps).
func BuildReadDenyTargets(profile, workspace string) ([]string, error) {
	parsed, err := ParseSandboxProfile(profile)
	if err != nil {
		return nil, err
	}
	if IsBuiltinSandboxProfile(parsed) || parsed == SandboxOff {
		return nil, nil
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "."
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	cfg := LoadSandboxTOML(workspace)
	entry, ok := cfg.Profiles[string(parsed)]
	if !ok {
		return nil, fmt.Errorf("custom sandbox profile %q not found", parsed)
	}
	exact, globs := partitionDenyEntries(entry.Deny)
	var targets []string
	seen := map[string]bool{}
	add := func(path string) {
		path = filepath.Clean(path)
		if path == "" || path == "." || seen[path] {
			return
		}
		seen[path] = true
		targets = append(targets, path)
	}
	for _, path := range exact {
		add(resolveSandboxPath(path, workspace))
	}
	if len(globs) > 0 {
		expanded, err := expandDenyGlobs(workspace, globs)
		if err != nil {
			return nil, err
		}
		for _, path := range expanded {
			add(path)
		}
	}
	return targets, nil
}

func partitionDenyEntries(deny []string) (exact, globs []string) {
	for _, entry := range deny {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if isDenyGlob(entry) {
			globs = append(globs, entry)
			continue
		}
		exact = append(exact, entry)
	}
	return exact, globs
}

func isDenyGlob(entry string) bool {
	return strings.ContainsAny(entry, "*?[")
}

func expandDenyGlobs(workspace string, globs []string) ([]string, error) {
	var matches []string
	entriesWalked := 0
	for _, pattern := range globs {
		root, tail := splitDenyGlobRoot(workspace, pattern)
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			entriesWalked++
			if entriesWalked > denyGlobMaxEntries {
				return fmt.Errorf("sandbox deny glob walked more than %d entries", denyGlobMaxEntries)
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			relSlash := filepath.ToSlash(rel)
			if relSlash == "." {
				relSlash = ""
			}
			depth := 0
			if relSlash != "" {
				depth = strings.Count(relSlash, "/") + 1
			}
			if d.IsDir() && depth >= denyGlobMaxDepth {
				return filepath.SkipDir
			}
			if relSlash == "" {
				return nil
			}
			ok, err := doublestar.Match(tail, relSlash)
			if err != nil || !ok {
				return nil
			}
			matches = append(matches, path)
			if len(matches) > denyGlobMaxMatches {
				return fmt.Errorf("sandbox deny glob matched more than %d paths", denyGlobMaxMatches)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return matches, nil
}

func splitDenyGlobRoot(workspace, glob string) (root, tail string) {
	if !strings.HasPrefix(glob, "/") {
		return workspace, filepath.ToSlash(glob)
	}
	root = "/"
	parts := strings.Split(strings.TrimPrefix(glob, "/"), "/")
	var tailParts []string
	inTail := false
	for _, part := range parts {
		if inTail {
			tailParts = append(tailParts, part)
			continue
		}
		if isDenyGlob(part) {
			inTail = true
			tailParts = append(tailParts, part)
			continue
		}
		if part != "" {
			root = filepath.Join(root, part)
		}
	}
	if len(tailParts) == 0 {
		return root, "**"
	}
	return root, strings.Join(tailParts, "/")
}
