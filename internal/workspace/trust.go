package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lookcorner/go-cli/internal/version"
	"github.com/pelletier/go-toml/v2"
)

const trustFileName = "trusted_folders.toml"

type TrustDecision int

const (
	TrustUntrusted TrustDecision = iota
	TrustTrusted
	TrustPrompt
)

type folderTrust struct {
	Trusted   bool  `toml:"trusted"`
	DecidedAt int64 `toml:"decided_at,omitempty"`
}

type trustDocument struct {
	Folders map[string]folderTrust `toml:"folders"`
}

// ResolveFolderTrust decides whether repo-controlled execution config may run.
func ResolveFolderTrust(cwd string, enabled, interactive bool) TrustDecision {
	home := userHome()
	return resolveFolderTrust(cwd, trustStorePath(home), home, enabled, DevelopmentBuild(), interactive)
}

// GrantFolderTrust persists an explicit --trust decision. Development builds
// match the reference and keep folder trust inert, so no store is written.
func GrantFolderTrust(ctx context.Context, cwd string) error {
	if DevelopmentBuild() {
		return nil
	}
	home := userHome()
	path := trustStorePath(home)
	if path == "" {
		return errors.New("cannot persist folder trust without GROK_HOME or a user home")
	}
	key := WorkspaceTrustKey(cwd)
	if unsafeTrustRoot(key, home) {
		return nil
	}
	return recordFolderTrust(ctx, path, key, true)
}

func RevokeFolderTrust(ctx context.Context, cwd string) error {
	if DevelopmentBuild() {
		return nil
	}
	home := userHome()
	path := trustStorePath(home)
	if path == "" {
		return errors.New("cannot persist folder trust without GROK_HOME or a user home")
	}
	key := WorkspaceTrustKey(cwd)
	if unsafeTrustRoot(key, home) {
		return errors.New("refusing to change trust for a broad user directory")
	}
	return recordFolderTrust(ctx, path, key, false)
}

func DevelopmentBuild() bool {
	return version.Current == "" || strings.Contains(strings.ToLower(version.Current), "dev")
}

func resolveFolderTrust(cwd, storePath, home string, enabled, development, interactive bool) TrustDecision {
	if !enabled || development {
		return TrustTrusted
	}
	key := WorkspaceTrustKey(cwd)
	if unsafeTrustRoot(key, home) || trustedByStore(readTrustDocument(storePath), key, home) {
		return TrustTrusted
	}
	if !ProjectExecutionConfigPresent(cwd) {
		return TrustTrusted
	}
	if interactive {
		return TrustPrompt
	}
	return TrustUntrusted
}

// WorkspaceTrustKey shares trust across normal linked worktrees by collapsing
// their common .git directory onto the main checkout.
func WorkspaceTrustKey(cwd string) string {
	cwd = canonicalOrCleanTrust(cwd)
	command := exec.Command("git", "rev-parse", "--show-toplevel", "--git-common-dir")
	command.Dir = cwd
	output, err := command.Output()
	if err != nil {
		return cwd
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return cwd
	}
	root := canonicalOrCleanTrust(lines[0])
	common := strings.TrimSpace(lines[1])
	if !filepath.IsAbs(common) {
		common = filepath.Join(cwd, common)
	}
	common = canonicalOrCleanTrust(common)
	if filepath.Base(common) == ".git" {
		main := canonicalOrCleanTrust(filepath.Dir(common))
		if isDirTrust(main) {
			root = main
		}
	}
	if unsafeTrustRoot(root, userHome()) {
		return cwd
	}
	return root
}

// ProjectExecutionConfigPresent reports whether repo-controlled configuration
// can start local processes in the current implementation.
func ProjectExecutionConfigPresent(cwd string) bool {
	return len(ProjectExecutionConfigKinds(cwd)) > 0
}

// ProjectExecutionConfigKinds names the executable project configuration that
// causes an untrusted workspace to be gated.
func ProjectExecutionConfigKinds(cwd string) []string {
	cwd = canonicalOrCleanTrust(cwd)
	root := GitRoot(cwd)
	found := make(map[string]bool)
	add := func(kind string) { found[kind] = true }
	if isFileTrust(filepath.Join(cwd, ".grok", "lsp.json")) {
		add("lsp")
	}
	for _, scope := range ProjectScopes(root, cwd) {
		for _, path := range []string{
			filepath.Join(scope, ".mcp.json"),
			filepath.Join(scope, ".cursor", "mcp.json"),
		} {
			if isFileTrust(path) {
				add("mcp")
			}
		}
		for _, path := range []string{
			filepath.Join(scope, ".grok", "plugins"),
			filepath.Join(scope, ".claude", "plugins"),
		} {
			if hasSubdirectory(path) {
				add("plugins")
			}
		}
		if hasHookFile(filepath.Join(scope, ".grok", "hooks")) {
			add("hooks")
		}
		if hasAgentFile(filepath.Join(scope, ".grok", "agents")) || hasAgentFile(filepath.Join(scope, ".claude", "agents")) {
			add("agents")
		}
		for _, path := range []string{
			filepath.Join(scope, ".cursor", "hooks.json"),
			filepath.Join(scope, ".claude", "settings.json"),
			filepath.Join(scope, ".claude", "settings.local.json"),
		} {
			if hasConfiguredHooks(path) {
				add("hooks")
			}
		}
		for _, kind := range projectConfigKinds(filepath.Join(scope, ".grok", "config.toml")) {
			add(kind)
		}
	}
	result := make([]string, 0, len(found))
	for _, kind := range []string{"mcp", "plugins", "lsp", "hooks", "agents"} {
		if found[kind] {
			result = append(result, kind)
		}
	}
	return result
}

func trustedByStore(document trustDocument, key, home string) bool {
	key = canonicalOrCleanTrust(key)
	bestDepth := -1
	trusted := false
	for folder, record := range document.Folders {
		folder = canonicalOrCleanTrust(folder)
		if unsafeTrustRoot(folder, home) || !pathWithinTrust(folder, key) {
			continue
		}
		depth := len(strings.Split(filepath.Clean(folder), string(filepath.Separator)))
		if depth > bestDepth {
			bestDepth, trusted = depth, record.Trusted
		} else if depth == bestDepth {
			trusted = trusted && record.Trusted
		}
	}
	return trusted
}

func recordFolderTrust(ctx context.Context, path, key string, trusted bool) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	release, err := acquireTrustLock(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer release()
	document := readTrustDocument(path)
	if document.Folders == nil {
		document.Folders = make(map[string]folderTrust)
	}
	document.Folders[canonicalOrCleanTrust(key)] = folderTrust{Trusted: trusted, DecidedAt: time.Now().Unix()}
	data, err := toml.Marshal(document)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".trusted-folders-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return atomicReplace(temporaryPath, path)
}

func readTrustDocument(path string) trustDocument {
	if path == "" {
		return trustDocument{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return trustDocument{}
	}
	var document trustDocument
	if toml.Unmarshal(data, &document) != nil {
		return trustDocument{}
	}
	return document
}

func acquireTrustLock(ctx context.Context, path string) (func(), error) {
	token := fmt.Sprintf("%d:%d", os.Getpid(), time.Now().UnixNano())
	for {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, err = file.WriteString(token); err == nil {
				err = file.Close()
			} else {
				_ = file.Close()
			}
			if err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return func() {
				if data, readErr := os.ReadFile(path); readErr == nil && string(data) == token {
					_ = os.Remove(path)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(path)
			continue
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func projectConfigKinds(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var document map[string]any
	if toml.Unmarshal(data, &document) != nil {
		return []string{"mcp", "plugins", "lsp", "hooks"}
	}
	var result []string
	for _, item := range []struct{ key, kind string }{
		{"mcp_servers", "mcp"}, {"lsp_servers", "lsp"}, {"hooks", "hooks"},
	} {
		if value, ok := document[item.key].(map[string]any); ok && len(value) > 0 {
			result = append(result, item.kind)
		}
	}
	if plugins, ok := document["plugins"].(map[string]any); ok {
		if paths, ok := plugins["paths"].([]any); ok && len(paths) > 0 {
			result = append(result, "plugins")
		}
	}
	return result
}

func hasSubdirectory(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		candidate := filepath.Join(path, entry.Name())
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func hasHookFile(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" && !strings.HasPrefix(entry.Name(), ".") {
			return true
		}
	}
	return false
}

func hasAgentFile(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".md" && !strings.HasPrefix(entry.Name(), ".") {
			return true
		}
	}
	return false
}

func hasConfiguredHooks(path string) bool {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return false
	}
	var value struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	return json.Unmarshal(data, &value) == nil && len(value.Hooks) > 0
}

func unsafeTrustRoot(path, home string) bool {
	path = canonicalOrCleanTrust(path)
	if !filepath.IsAbs(path) || filepath.Dir(path) == path {
		return true
	}
	return home != "" && path == canonicalOrCleanTrust(home)
}

func pathWithinTrust(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func trustStorePath(home string) string {
	grokHome := os.Getenv("GROK_HOME")
	if grokHome == "" {
		if home == "" {
			return ""
		}
		grokHome = filepath.Join(home, ".grok")
	} else if !filepath.IsAbs(grokHome) {
		grokHome, _ = filepath.Abs(grokHome)
	}
	return filepath.Join(grokHome, trustFileName)
}

func userHome() string {
	home, _ := os.UserHomeDir()
	return canonicalOrCleanTrust(home)
}

func canonicalOrCleanTrust(path string) string {
	if path == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

func isDirTrust(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFileTrust(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func (d TrustDecision) String() string {
	switch d {
	case TrustTrusted:
		return "trusted"
	case TrustPrompt:
		return "prompt"
	default:
		return "untrusted"
	}
}
