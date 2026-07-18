package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

var skillsUpdateMu sync.Mutex

func UpdateSkills(path string, update func(*SkillsConfig)) error {
	if update == nil {
		return errors.New("skills update is required")
	}
	if path == "" {
		var err error
		path, err = discoverDefaultPath()
		if err != nil {
			return err
		}
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return fmt.Errorf("resolve config %q: %w", path, resolveErr)
		}
		path = resolved
	}
	skillsUpdateMu.Lock()
	defer skillsUpdateMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read config %q: %w", path, err)
	}
	root := make(map[string]any)
	jsonConfig := filepath.Ext(path) == ".json"
	if len(data) > 0 {
		if jsonConfig {
			err = json.Unmarshal(data, &root)
		} else {
			err = toml.Unmarshal(data, &root)
		}
		if err != nil {
			return fmt.Errorf("parse config %q: %w", path, err)
		}
	}
	var settings SkillsConfig
	if raw, ok := root["skills"]; ok {
		encoded, marshalErr := json.Marshal(raw)
		if marshalErr != nil || json.Unmarshal(encoded, &settings) != nil {
			return fmt.Errorf("parse config %q: invalid skills table", path)
		}
	}
	update(&settings)
	if len(settings.Paths) == 0 && len(settings.Ignore) == 0 && len(settings.Disabled) == 0 {
		delete(root, "skills")
	} else {
		root["skills"] = map[string]any{
			"paths": settings.Paths, "ignore": settings.Ignore, "disabled": settings.Disabled,
		}
	}
	if jsonConfig {
		data, err = json.MarshalIndent(root, "", "  ")
		data = append(data, '\n')
	} else {
		data, err = toml.Marshal(root)
	}
	if err != nil {
		return fmt.Errorf("encode config %q: %w", path, err)
	}
	return writeConfigAtomic(path, data)
}

func writeConfigAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".gork-config-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
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
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace config %q: %w", path, err)
	}
	return nil
}
