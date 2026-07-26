package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const credentialsFilename = "mcp_credentials.json"

var credentialsMu sync.Mutex

// TokenResponse is the OAuth token payload stored under token_response.
// Shape matches rmcp / Rust mcp_credentials.json fixtures.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// StoredCredentials is one MCP server entry in mcp_credentials.json.
type StoredCredentials struct {
	ClientID        string         `json:"client_id"`
	TokenResponse   *TokenResponse `json:"token_response"`
	GrantedScopes   []string       `json:"granted_scopes,omitempty"`
	TokenReceivedAt *int64         `json:"token_received_at,omitempty"`
}

// CredentialStore is the on-disk map keyed by "{server_name}:{server_url}".
type CredentialStore struct {
	entries map[string]StoredCredentials
}

// CredentialKey builds the composite store key used by the Rust reference.
func CredentialKey(serverName, serverURL string) string {
	return strings.TrimSpace(serverName) + ":" + strings.TrimSpace(serverURL)
}

// DefaultCredentialsPath returns $GROK_HOME/mcp_credentials.json or ~/.grok/….
func DefaultCredentialsPath() (string, error) {
	if home := strings.TrimSpace(os.Getenv("GROK_HOME")); home != "" {
		return filepath.Join(home, credentialsFilename), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".grok", credentialsFilename), nil
}

// LoadCredentialStore loads credentials from path. Missing files yield an empty store.
func LoadCredentialStore(path string) (CredentialStore, error) {
	credentialsMu.Lock()
	defer credentialsMu.Unlock()
	return loadCredentialStoreLocked(path)
}

func loadCredentialStoreLocked(path string) (CredentialStore, error) {
	store := CredentialStore{entries: make(map[string]StoredCredentials)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return CredentialStore{}, err
	}
	_ = ensureOwnerOnly(path)
	if len(strings.TrimSpace(string(data))) == 0 {
		return store, nil
	}
	if err := json.Unmarshal(data, &store.entries); err != nil {
		return CredentialStore{}, fmt.Errorf("decode MCP credentials: %w", err)
	}
	if store.entries == nil {
		store.entries = make(map[string]StoredCredentials)
	}
	return store, nil
}

// SaveCredentialStore writes the store atomically with owner-only permissions.
func SaveCredentialStore(path string, store CredentialStore) error {
	credentialsMu.Lock()
	defer credentialsMu.Unlock()
	return saveCredentialStoreLocked(path, store)
}

func saveCredentialStoreLocked(path string, store CredentialStore) error {
	if store.entries == nil {
		store.entries = make(map[string]StoredCredentials)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store.entries, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".mcp-credentials-*.json")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	return ensureOwnerOnly(path)
}

// Get returns credentials for name+URL, if present.
func (s CredentialStore) Get(serverName, serverURL string) (StoredCredentials, bool) {
	if s.entries == nil {
		return StoredCredentials{}, false
	}
	creds, ok := s.entries[CredentialKey(serverName, serverURL)]
	return creds, ok
}

// AccessToken returns a non-empty access token when present.
func (c StoredCredentials) AccessToken() string {
	if c.TokenResponse == nil {
		return ""
	}
	return strings.TrimSpace(c.TokenResponse.AccessToken)
}

// InsertAndSave merges one entry under a cross-process lock and reloads from disk.
func InsertAndSave(ctx context.Context, path, serverName, serverURL string, creds StoredCredentials) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("MCP credentials path is required")
	}
	lock, err := acquireCredentialsLock(ctx, path)
	if err != nil {
		return err
	}
	defer lock.release()

	credentialsMu.Lock()
	defer credentialsMu.Unlock()
	store, err := loadCredentialStoreLocked(path)
	if err != nil {
		return err
	}
	store.entries[CredentialKey(serverName, serverURL)] = creds
	return saveCredentialStoreLocked(path, store)
}

// RemoveCredentials deletes the entry for name+URL when present.
func RemoveCredentials(ctx context.Context, path, serverName, serverURL string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("MCP credentials path is required")
	}
	lock, err := acquireCredentialsLock(ctx, path)
	if err != nil {
		return err
	}
	defer lock.release()

	credentialsMu.Lock()
	defer credentialsMu.Unlock()
	store, err := loadCredentialStoreLocked(path)
	if err != nil {
		return err
	}
	delete(store.entries, CredentialKey(serverName, serverURL))
	if len(store.entries) == 0 {
		_ = os.Remove(path)
		return nil
	}
	return saveCredentialStoreLocked(path, store)
}

// ApplyCredentialHeaders sets Authorization from the store when no static
// Authorization header is already present. Missing stores are ignored.
func ApplyCredentialHeaders(headers map[string]string, serverName, serverURL, credentialsPath string) map[string]string {
	if headers == nil {
		headers = make(map[string]string)
	} else {
		headers = cloneHeaders(headers)
	}
	if hasAuthorizationHeader(headers) {
		return headers
	}
	path := strings.TrimSpace(credentialsPath)
	if path == "" {
		var err error
		path, err = DefaultCredentialsPath()
		if err != nil {
			return headers
		}
	}
	store, err := LoadCredentialStore(path)
	if err != nil {
		return headers
	}
	creds, ok := store.Get(serverName, serverURL)
	if !ok {
		return headers
	}
	if token := creds.AccessToken(); token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}

func hasAuthorizationHeader(headers map[string]string) bool {
	for key, value := range headers {
		if strings.EqualFold(key, "Authorization") && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// RefreshStoredCredentials exchanges a refresh_token at tokenURL and persists
// the new token_response. clientSecret is optional (public clients).
func RefreshStoredCredentials(ctx context.Context, path, serverName, serverURL, tokenURL, clientSecret string, client *http.Client) (StoredCredentials, error) {
	tokenURL = strings.TrimSpace(tokenURL)
	if tokenURL == "" {
		return StoredCredentials{}, errors.New("MCP OAuth token URL is required for refresh")
	}
	if client == nil {
		client = http.DefaultClient
	}
	lock, err := acquireCredentialsLock(ctx, path)
	if err != nil {
		return StoredCredentials{}, err
	}
	defer lock.release()

	credentialsMu.Lock()
	store, err := loadCredentialStoreLocked(path)
	credentialsMu.Unlock()
	if err != nil {
		return StoredCredentials{}, err
	}
	creds, ok := store.Get(serverName, serverURL)
	if !ok || creds.TokenResponse == nil || strings.TrimSpace(creds.TokenResponse.RefreshToken) == "" {
		return StoredCredentials{}, errors.New("MCP credentials have no refresh token")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", creds.TokenResponse.RefreshToken)
	form.Set("client_id", creds.ClientID)
	if strings.TrimSpace(clientSecret) != "" {
		form.Set("client_secret", clientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return StoredCredentials{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return StoredCredentials{}, fmt.Errorf("MCP OAuth refresh: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return StoredCredentials{}, fmt.Errorf("MCP OAuth refresh returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var token TokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return StoredCredentials{}, fmt.Errorf("decode MCP OAuth refresh: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return StoredCredentials{}, errors.New("MCP OAuth refresh omitted access_token")
	}
	if strings.TrimSpace(token.RefreshToken) == "" {
		token.RefreshToken = creds.TokenResponse.RefreshToken
	}
	if strings.TrimSpace(token.TokenType) == "" {
		token.TokenType = "bearer"
	}
	now := time.Now().Unix()
	updated := StoredCredentials{
		ClientID:        creds.ClientID,
		TokenResponse:   &token,
		GrantedScopes:   append([]string(nil), creds.GrantedScopes...),
		TokenReceivedAt: &now,
	}

	credentialsMu.Lock()
	defer credentialsMu.Unlock()
	store, err = loadCredentialStoreLocked(path)
	if err != nil {
		return StoredCredentials{}, err
	}
	store.entries[CredentialKey(serverName, serverURL)] = updated
	if err := saveCredentialStoreLocked(path, store); err != nil {
		return StoredCredentials{}, err
	}
	return updated, nil
}

type credentialsLock struct {
	path  string
	token string
}

func acquireCredentialsLock(ctx context.Context, credentialsPath string) (*credentialsLock, error) {
	if err := os.MkdirAll(filepath.Dir(credentialsPath), 0o700); err != nil {
		return nil, err
	}
	lockPath := credentialsPath + ".lock"
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	token := fmt.Sprintf("%d:%s", os.Getpid(), hex.EncodeToString(tokenBytes))
	const stale = 2 * time.Minute
	for {
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, err = file.WriteString(token); err == nil {
				err = file.Close()
			} else {
				file.Close()
			}
			if err != nil {
				_ = os.Remove(lockPath)
				return nil, err
			}
			return &credentialsLock{path: lockPath, token: token}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > stale {
			_ = os.Remove(lockPath)
			continue
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *credentialsLock) release() {
	if l == nil {
		return
	}
	data, err := os.ReadFile(l.path)
	if err == nil && string(data) == l.token {
		_ = os.Remove(l.path)
	}
}

func ensureOwnerOnly(path string) error {
	return os.Chmod(path, 0o600)
}
