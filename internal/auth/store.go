package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var storeMu sync.Mutex

func Load(path, scope string) (Credential, error) {
	storeMu.Lock()
	defer storeMu.Unlock()
	store, err := readStore(path)
	if err != nil {
		return Credential{}, err
	}
	raw, ok := store[scope]
	if !ok {
		return Credential{}, os.ErrNotExist
	}
	var credential Credential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return Credential{}, fmt.Errorf("decode OAuth credential: %w", err)
	}
	if credential.Key == "" {
		return Credential{}, os.ErrNotExist
	}
	return credential, nil
}

func Save(path, scope string, credential Credential) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lock, err := acquireFileLock(ctx, path)
	if err != nil {
		return err
	}
	defer lock.release()
	return saveCredential(path, scope, credential)
}

func Remove(path, scope string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lock, err := acquireFileLock(ctx, path)
	if err != nil {
		return err
	}
	defer lock.release()

	storeMu.Lock()
	defer storeMu.Unlock()
	store, err := readStore(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, ok := store[scope]; !ok {
		return nil
	}
	delete(store, scope)
	if len(store) == 0 {
		return os.Remove(path)
	}
	return writeStore(path, store)
}

func saveCredential(path, scope string, credential Credential) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	store, err := readStore(path)
	if errors.Is(err, os.ErrNotExist) {
		store = make(map[string]json.RawMessage)
	} else if err != nil {
		if backupErr := backupCorrupt(path); backupErr != nil {
			return err
		}
		store = make(map[string]json.RawMessage)
	}
	raw, err := mergeCredential(store[scope], credential)
	if err != nil {
		return err
	}
	store[scope] = raw
	return writeStore(path, store)
}

func readStore(path string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return make(map[string]json.RawMessage), nil
	}
	var store map[string]json.RawMessage
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("decode auth store: %w", err)
	}
	if store == nil {
		store = make(map[string]json.RawMessage)
	}
	return store, nil
}

func mergeCredential(existing json.RawMessage, credential Credential) (json.RawMessage, error) {
	fields := make(map[string]json.RawMessage)
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &fields)
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	data, err := json.Marshal(credential)
	if err != nil {
		return nil, err
	}
	var updates map[string]json.RawMessage
	if err := json.Unmarshal(data, &updates); err != nil {
		return nil, err
	}
	for _, name := range []string{"email", "refresh_token", "expires_at", "oidc_issuer", "oidc_client_id"} {
		if _, ok := updates[name]; !ok {
			delete(fields, name)
		}
	}
	for name, value := range updates {
		fields[name] = value
	}
	return json.Marshal(fields)
}

func writeStore(path string, store map[string]json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.json")
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
	return os.Chmod(path, 0o600)
}

func backupCorrupt(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return os.Rename(path, fmt.Sprintf("%s.corrupt.%d", path, time.Now().UnixMilli()))
}
