package workspace

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const envrcBash = `
set -e
cd -- "$1"
source_up_if_exists() { :; }
source_up() { :; }
source_env_if_exists() { if [ -f "$1" ]; then . "$1"; fi; }
source_env() { if [ -f "$1" ]; then . "$1"; fi; }
PATH_add() { export PATH="$PWD/$1:$PATH"; }
path_add() { PATH_add "$@"; }
layout() { :; }
use() { :; }
watch_file() { :; }
. "$2"
env -0
`

// LoadEnvrc executes only root/.envrc after folder trust has been granted.
// Failures are ignored so optional workspace environment cannot block startup.
func LoadEnvrc(root string, trusted bool) map[string]string {
	if !trusted {
		return map[string]string{}
	}
	envrc := filepath.Join(root, ".envrc")
	if info, err := os.Stat(envrc); err != nil || !info.Mode().IsRegular() {
		return map[string]string{}
	}
	if path, err := exec.LookPath("direnv"); err == nil {
		command := exec.Command(path, "export", "json")
		command.Dir = root
		if output, err := command.Output(); err == nil {
			var values map[string]*string
			if json.Unmarshal(output, &values) == nil {
				return changedJSONEnvironment(values)
			}
		}
	}
	return loadEnvrcWithBash(root, envrc)
}

func changedJSONEnvironment(values map[string]*string) map[string]string {
	result := make(map[string]string)
	for key, value := range values {
		if value != nil && !ignoredEnvrcKey(key) {
			result[key] = *value
		}
	}
	return result
}

func loadEnvrcWithBash(root, envrc string) map[string]string {
	baseline := environmentMap(os.Environ())
	command := exec.Command("/bin/bash", "-c", envrcBash, "gork-envrc", root, envrc)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return map[string]string{}
	}
	result := make(map[string]string)
	for _, entry := range strings.Split(string(output), "\x00") {
		key, value, ok := strings.Cut(entry, "=")
		baselineValue, exists := baseline[key]
		if !ok || ignoredEnvrcKey(key) || exists && baselineValue == value {
			continue
		}
		result[key] = value
	}
	return result
}

func environmentMap(entries []string) map[string]string {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

func ignoredEnvrcKey(key string) bool {
	switch key {
	case "_", "SHLVL", "PWD", "OLDPWD":
		return true
	default:
		return false
	}
}
