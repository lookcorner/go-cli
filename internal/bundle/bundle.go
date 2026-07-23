package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/lookcorner/go-cli/internal/version"
	"github.com/pelletier/go-toml/v2"
)

const (
	maxArchiveBytes       = 50 << 20
	maxArchiveEntries     = 1000
	maxArchiveEntryBytes  = 1 << 20
	maxManifestBytes      = 4 << 20
	defaultSyncFreshness  = time.Hour
	missingCredentialsErr = "bundle sync requires either an authenticated cli-chat-proxy session or a deployment key"
)

type Manifest struct {
	Version   string            `json:"version"`
	Checksums map[string]string `json:"checksums"`
}

type Payload struct {
	Version  string            `json:"version"`
	Personas map[string]string `json:"personas"`
	Roles    map[string]string `json:"roles"`
	Agents   map[string]string `json:"agents"`
	Skills   map[string]string `json:"skills"`
}

type Credentials struct {
	Token      string
	UserID     string
	Email      string
	Deployment bool
}

type SyncResult struct {
	Updated       bool   `json:"updated"`
	Version       string `json:"version"`
	PersonasCount int    `json:"personasCount"`
	RolesCount    int    `json:"rolesCount"`
	AgentsCount   int    `json:"agentsCount"`
	SkillsCount   int    `json:"skillsCount"`
}

type PersonaDetail struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
	HasInputs   bool    `json:"hasInputs"`
	HasOutputs  bool    `json:"hasOutputs"`
}

type RoleDetail struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Status struct {
	HasCache       bool            `json:"hasCache"`
	Version        *string         `json:"version,omitempty"`
	Personas       []string        `json:"personas"`
	Roles          []string        `json:"roles"`
	Agents         []string        `json:"agents"`
	Skills         []string        `json:"skills"`
	PersonaDetails []PersonaDetail `json:"personaDetails,omitempty"`
	RoleDetails    []RoleDetail    `json:"roleDetails,omitempty"`
}

type Entry struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

type Service struct {
	Root        string
	BaseURL     string
	HTTP        *http.Client
	Credentials func(context.Context, string) (Credentials, error)
	mu          sync.Mutex
}

func Root() (string, error) {
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		return filepath.Join(home, "bundled"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".grok", "bundled"), nil
}

func (s *Service) Status() (Status, error) {
	manifest, err := readManifest(s.Root)
	if err != nil {
		return Status{}, err
	}
	status := Status{Personas: []string{}, Roles: []string{}, Agents: []string{}, Skills: []string{}}
	if manifest == nil {
		return status, nil
	}
	status.HasCache, status.Version = true, &manifest.Version
	status.Personas = listedEntries(s.Root, manifest, "personas", ".toml")
	status.Roles = listedEntries(s.Root, manifest, "roles", ".toml")
	status.Agents = listedEntries(s.Root, manifest, "agents", ".md")
	status.Skills = listedSkills(s.Root, manifest)
	for _, name := range status.Personas {
		if detail, ok := personaDetail(s.Root, name); ok {
			status.PersonaDetails = append(status.PersonaDetails, detail)
		}
	}
	for _, name := range status.Roles {
		if detail, ok := roleDetail(s.Root, name); ok {
			status.RoleDetails = append(status.RoleDetails, detail)
		}
	}
	return status, nil
}

func (s *Service) Entry(kind, name string) (Entry, error) {
	if !validEntryName(name) {
		return Entry{}, fmt.Errorf("invalid entry name: %s", name)
	}
	dir, ext := "", ""
	switch kind {
	case "persona":
		dir, ext = "personas", ".toml"
	case "role":
		dir, ext = "roles", ".toml"
	case "agent":
		dir, ext = "agents", ".md"
	default:
		return Entry{}, fmt.Errorf("unknown entry kind: %s", kind)
	}
	content, err := os.ReadFile(filepath.Join(s.Root, dir, name+ext))
	if err != nil {
		return Entry{}, fmt.Errorf("%s %q not found in bundle cache: %w", kind, name, err)
	}
	return Entry{Kind: kind, Name: name, Content: string(content)}, nil
}

func (s *Service) MaybeSync(ctx context.Context) (*SyncResult, error) {
	if s.Credentials == nil {
		return nil, nil
	}
	credentials, err := s.Credentials(ctx, "")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(credentials.Token) == "" {
		return nil, nil
	}
	if freshManifest(s.Root, defaultSyncFreshness) {
		return nil, nil
	}
	if !s.mu.TryLock() {
		return nil, nil
	}
	defer s.mu.Unlock()
	result, err := s.fetchAndStore(ctx, credentials)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Service) Sync(ctx context.Context, _ bool) (SyncResult, error) {
	if s.Credentials == nil {
		return SyncResult{}, errors.New(missingCredentialsErr)
	}
	credentials, err := s.Credentials(ctx, "")
	if err != nil {
		return SyncResult{}, err
	}
	if strings.TrimSpace(credentials.Token) == "" {
		return SyncResult{}, errors.New(missingCredentialsErr)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.fetchAndStore(ctx, credentials)
	if err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

func (s *Service) fetchAndStore(ctx context.Context, credentials Credentials) (SyncResult, error) {
	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	archiveCtx, cancelArchive := context.WithTimeout(ctx, 30*time.Second)
	archiveResponse, credentials, err := s.authenticatedRequest(archiveCtx, client, "/bundle/archive", credentials)
	if err != nil {
		cancelArchive()
		return SyncResult{}, err
	}
	if archiveResponse.StatusCode >= 200 && archiveResponse.StatusCode < 300 {
		data, readErr := readLimited(archiveResponse.Body, maxArchiveBytes)
		archiveResponse.Body.Close()
		cancelArchive()
		if readErr != nil {
			return SyncResult{}, readErr
		}
		manifest, err := extractArchive(s.Root, data)
		return syncResult(manifest), err
	}
	archiveStatus := archiveResponse.StatusCode
	io.Copy(io.Discard, io.LimitReader(archiveResponse.Body, 4096))
	archiveResponse.Body.Close()
	cancelArchive()
	if archiveStatus == http.StatusUnauthorized {
		return SyncResult{}, fmt.Errorf("bundle archive request failed with status %d", archiveStatus)
	}
	legacyCtx, cancelLegacy := context.WithTimeout(ctx, 10*time.Second)
	defer cancelLegacy()
	legacyResponse, _, err := s.authenticatedRequest(legacyCtx, client, "/subagents/bundle", credentials)
	if err != nil {
		return SyncResult{}, err
	}
	defer legacyResponse.Body.Close()
	if legacyResponse.StatusCode < 200 || legacyResponse.StatusCode >= 300 {
		return SyncResult{}, fmt.Errorf("bundle request failed with status %d", legacyResponse.StatusCode)
	}
	data, err := readLimited(legacyResponse.Body, maxArchiveBytes)
	if err != nil {
		return SyncResult{}, err
	}
	var payload Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		return SyncResult{}, fmt.Errorf("decode bundle: %w", err)
	}
	if _, err := writePayload(s.Root, payload); err != nil {
		return SyncResult{}, err
	}
	return SyncResult{
		Updated: true, Version: payload.Version, PersonasCount: len(payload.Personas), RolesCount: len(payload.Roles),
		AgentsCount: len(payload.Agents), SkillsCount: len(payload.Skills),
	}, nil
}

func (s *Service) authenticatedRequest(ctx context.Context, client *http.Client, suffix string, credentials Credentials) (*http.Response, Credentials, error) {
	response, err := s.request(ctx, client, suffix, credentials)
	if err != nil || response.StatusCode != http.StatusUnauthorized || credentials.Deployment || s.Credentials == nil {
		return response, credentials, err
	}
	io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	response.Body.Close()
	refreshed, err := s.Credentials(ctx, credentials.Token)
	if err != nil {
		return nil, credentials, err
	}
	if strings.TrimSpace(refreshed.Token) == "" {
		return nil, credentials, errors.New(missingCredentialsErr)
	}
	response, err = s.request(ctx, client, suffix, refreshed)
	return response, refreshed, err
}

func (s *Service) request(ctx context.Context, client *http.Client, suffix string, credentials Credentials) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.BaseURL, "/")+suffix, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+credentials.Token)
	request.Header.Set("x-grok-client-version", version.Current)
	request.Header.Set("x-grok-client-identifier", "gork-go")
	request.Header.Set("x-grok-client-mode", "interactive")
	if !credentials.Deployment {
		request.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
		if credentials.UserID != "" {
			request.Header.Set("x-userid", credentials.UserID)
		}
		if credentials.Email != "" {
			request.Header.Set("x-email", credentials.Email)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch bundle: %w", err)
	}
	return response, nil
}

func writePayload(root string, payload Payload) (Manifest, error) {
	files := make(map[string][]byte)
	for kind, entries := range map[string]map[string]string{"personas": payload.Personas, "roles": payload.Roles, "agents": payload.Agents, "skills": payload.Skills} {
		for name, content := range entries {
			if !validBundleName(name) {
				return Manifest{}, fmt.Errorf("invalid bundled %s name: %q", strings.TrimSuffix(kind, "s"), name)
			}
			path := filepath.ToSlash(filepath.Join(kind, name+map[string]string{"personas": ".toml", "roles": ".toml", "agents": ".md"}[kind]))
			if kind == "skills" {
				path = "skills/" + name + "/SKILL.md"
			}
			files[path] = []byte(content)
		}
	}
	return updateCache(root, payload.Version, files)
}

func extractArchive(root string, data []byte) (Manifest, error) {
	decoder, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return Manifest{}, fmt.Errorf("open bundle archive: %w", err)
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	files := make(map[string][]byte)
	versionValue, entries, total := "", 0, int64(0)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("read bundle archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		entries++
		if entries > maxArchiveEntries {
			return Manifest{}, fmt.Errorf("archive exceeds maximum entry count (%d)", maxArchiveEntries)
		}
		if header.Size < 0 || header.Size > maxArchiveEntryBytes {
			return Manifest{}, fmt.Errorf("archive entry exceeds maximum size (%d bytes)", maxArchiveEntryBytes)
		}
		total += header.Size
		if total > maxArchiveBytes {
			return Manifest{}, fmt.Errorf("archive exceeds maximum decompressed size (%d bytes)", maxArchiveBytes)
		}
		content, err := io.ReadAll(io.LimitReader(reader, maxArchiveEntryBytes+1))
		if err != nil || int64(len(content)) != header.Size {
			return Manifest{}, fmt.Errorf("read bundle archive entry %q", header.Name)
		}
		name := strings.TrimPrefix(filepath.ToSlash(header.Name), "./")
		if name == "bundle.json" {
			var metadata struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(content, &metadata) != nil || metadata.Version == "" {
				return Manifest{}, errors.New("failed to parse bundle.json")
			}
			versionValue = metadata.Version
			continue
		}
		if mapped, ok := archivePath(name); ok {
			if _, exists := files[mapped]; !exists {
				files[mapped] = content
			}
		}
	}
	if versionValue == "" {
		return Manifest{}, errors.New("archive missing bundle.json with version field")
	}
	return updateCache(root, versionValue, files)
}

func updateCache(root, bundleVersion string, files map[string][]byte) (Manifest, error) {
	old, err := readManifest(root)
	if err != nil {
		return Manifest{}, err
	}
	if err := ensureDirs(root); err != nil {
		return Manifest{}, err
	}
	next := make(map[string]string)
	for relative, content := range files {
		previous := ""
		if old != nil {
			previous = old.Checksums[relative]
		}
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := ensureCacheParent(root, relative); err != nil {
			return Manifest{}, err
		}
		state, err := fileState(path, previous)
		if err != nil {
			return Manifest{}, err
		}
		if state == "absent" || state == "managed" {
			if err := atomicWrite(path, content, 0o600); err != nil {
				return Manifest{}, err
			}
			next[relative] = checksum(content)
		} else if previous != "" {
			next[relative] = previous
		}
	}
	if old != nil {
		for relative, previous := range old.Checksums {
			if _, retained := next[relative]; retained {
				continue
			}
			path := filepath.Join(root, filepath.FromSlash(relative))
			if err := ensureCacheParent(root, relative); err != nil {
				return Manifest{}, err
			}
			state, err := fileState(path, previous)
			if err != nil {
				return Manifest{}, err
			}
			if state == "managed" {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return Manifest{}, err
				}
			} else if state == "modified" {
				next[relative] = previous
			}
		}
	}
	manifest := Manifest{Version: bundleVersion, Checksums: next}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err := atomicWrite(filepath.Join(root, "manifest.json"), encoded, 0o600); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func readManifest(root string) (*Manifest, error) {
	file, err := os.Open(filepath.Join(root, "manifest.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := readLimited(file, maxManifestBytes)
	if err != nil {
		return nil, fmt.Errorf("read bundle manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse bundle manifest: %w", err)
	}
	clean := make(map[string]string)
	for path, value := range manifest.Checksums {
		if normalized, ok := validCachePath(path); ok {
			clean[normalized] = value
		}
	}
	manifest.Checksums = clean
	return &manifest, nil
}

func freshManifest(root string, ttl time.Duration) bool {
	info, err := os.Stat(filepath.Join(root, "manifest.json"))
	age := time.Duration(0)
	if err == nil {
		age = time.Since(info.ModTime())
	}
	if err != nil || age < 0 || age >= ttl {
		return false
	}
	manifest, err := readManifest(root)
	return err == nil && manifest != nil
}

func archivePath(path string) (string, bool) {
	if strings.HasPrefix(path, "subagents/") {
		path = strings.TrimPrefix(path, "subagents/")
	} else if !strings.HasPrefix(path, "skills/") {
		return "", false
	}
	return validCachePath(path)
}

func validCachePath(path string) (string, bool) {
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return "", false
	}
	parts := strings.Split(path, "/")
	if len(parts) == 2 && (parts[0] == "personas" || parts[0] == "roles" || parts[0] == "agents") {
		ext := map[string]string{"personas": ".toml", "roles": ".toml", "agents": ".md"}[parts[0]]
		name := strings.TrimSuffix(parts[1], ext)
		return path, strings.HasSuffix(parts[1], ext) && validBundleName(name)
	}
	if len(parts) >= 3 && parts[0] == "skills" && validBundleName(parts[1]) {
		for _, part := range parts[2:] {
			if part == "" || part == "." || part == ".." || strings.IndexFunc(part, unicode.IsControl) >= 0 {
				return "", false
			}
		}
		return path, true
	}
	return "", false
}

func validBundleName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\") && strings.IndexFunc(name, unicode.IsControl) < 0
}

func validEntryName(name string) bool {
	return validBundleName(name) && !strings.Contains(name, "..")
}

func ensureDirs(root string) error {
	for _, dir := range []string{root, filepath.Join(root, "personas"), filepath.Join(root, "roles"), filepath.Join(root, "agents"), filepath.Join(root, "skills")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle cache path is not a safe directory: %s", dir)
		}
	}
	return nil
}

func ensureCacheParent(root, relative string) error {
	current := root
	parts := strings.Split(filepath.FromSlash(relative), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle cache path is not a safe directory: %s", current)
		}
	}
	return nil
}

func fileState(path, previous string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("bundle cache entry is not a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if previous != "" && checksum(data) == previous {
		return "managed", nil
	}
	return "modified", nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".gork-bundle-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func checksum(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("bundle response exceeds %d bytes", limit)
	}
	return data, nil
}

func listedEntries(root string, manifest *Manifest, dir, ext string) []string {
	result := []string{}
	for relative := range manifest.Checksums {
		parts := strings.Split(relative, "/")
		if len(parts) == 2 && parts[0] == dir && strings.HasSuffix(parts[1], ext) {
			if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err == nil && info.Mode().IsRegular() {
				result = append(result, strings.TrimSuffix(parts[1], ext))
			}
		}
	}
	sort.Strings(result)
	return result
}

func listedSkills(root string, manifest *Manifest) []string {
	result := []string{}
	for relative := range manifest.Checksums {
		parts := strings.Split(relative, "/")
		if len(parts) == 3 && parts[0] == "skills" && parts[2] == "SKILL.md" {
			if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err == nil && info.Mode().IsRegular() {
				result = append(result, parts[1])
			}
		}
	}
	sort.Strings(result)
	return result
}

func personaDetail(root, name string) (PersonaDetail, bool) {
	data, err := os.ReadFile(filepath.Join(root, "personas", name+".toml"))
	if err != nil {
		return PersonaDetail{}, false
	}
	var value struct {
		Description  string           `toml:"description"`
		Instructions string           `toml:"instructions"`
		Inputs       []map[string]any `toml:"inputs"`
		Outputs      []map[string]any `toml:"outputs"`
	}
	if toml.Unmarshal(data, &value) != nil {
		return PersonaDetail{}, false
	}
	description := strings.TrimSpace(value.Description)
	if description == "" {
		description = firstParagraph(value.Instructions)
	}
	var pointer *string
	if description != "" {
		pointer = &description
	}
	return PersonaDetail{Name: name, Description: pointer, HasInputs: len(value.Inputs) > 0, HasOutputs: len(value.Outputs) > 0}, true
}

func roleDetail(root, name string) (RoleDetail, bool) {
	data, err := os.ReadFile(filepath.Join(root, "roles", name+".toml"))
	if err != nil {
		return RoleDetail{}, false
	}
	var value struct {
		Description string `toml:"description"`
	}
	if toml.Unmarshal(data, &value) != nil {
		return RoleDetail{}, false
	}
	return RoleDetail{Name: name, Description: value.Description}, true
}

func firstParagraph(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	paragraph := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			break
		}
		paragraph = append(paragraph, strings.TrimSpace(line))
	}
	return strings.Join(paragraph, " ")
}

func syncResult(manifest Manifest) SyncResult {
	result := SyncResult{Updated: true, Version: manifest.Version}
	for path := range manifest.Checksums {
		switch {
		case strings.HasPrefix(path, "personas/"):
			result.PersonasCount++
		case strings.HasPrefix(path, "roles/"):
			result.RolesCount++
		case strings.HasPrefix(path, "agents/"):
			result.AgentsCount++
		case strings.HasPrefix(path, "skills/"):
			result.SkillsCount++
		}
	}
	return result
}
