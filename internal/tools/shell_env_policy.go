package tools

import (
	"path"
	"runtime"
	"strings"
)

// ShellEnvironmentInherit controls which parent env vars seed the child.
type ShellEnvironmentInherit string

const (
	ShellEnvironmentInheritAll  ShellEnvironmentInherit = "all"
	ShellEnvironmentInheritCore ShellEnvironmentInherit = "core"
	ShellEnvironmentInheritNone ShellEnvironmentInherit = "none"
)

// ShellEnvironmentPolicy reshapes agent shell/process environments.
// Order: inherit → default secret excludes → exclude → set → include_only.
type ShellEnvironmentPolicy struct {
	Inherit               ShellEnvironmentInherit
	IgnoreDefaultExcludes bool
	Exclude               []string
	Set                   map[string]string
	IncludeOnly           []string
}

// DefaultShellEnvironmentPolicy matches the reference no-op (inherit all).
func DefaultShellEnvironmentPolicy() ShellEnvironmentPolicy {
	return ShellEnvironmentPolicy{
		Inherit:               ShellEnvironmentInheritAll,
		IgnoreDefaultExcludes: true,
	}
}

// IsNoop reports whether the policy leaves the inherited environment untouched.
func (p ShellEnvironmentPolicy) IsNoop() bool {
	inherit := p.Inherit
	if inherit == "" {
		inherit = ShellEnvironmentInheritAll
	}
	return inherit == ShellEnvironmentInheritAll &&
		p.IgnoreDefaultExcludes &&
		len(p.Exclude) == 0 &&
		len(p.Set) == 0 &&
		len(p.IncludeOnly) == 0
}

// ApplyShellEnvironmentPolicy filters KEY=value entries from the process env.
// A no-op policy returns a copy of env unchanged.
func ApplyShellEnvironmentPolicy(env []string, policy ShellEnvironmentPolicy) []string {
	if policy.IsNoop() {
		out := make([]string, len(env))
		copy(out, env)
		return out
	}
	vars := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		vars[key] = value
	}
	applied := createEnvFromVars(vars, policy)
	out := make([]string, 0, len(applied))
	for key, value := range applied {
		out = append(out, key+"="+value)
	}
	return out
}

func createEnvFromVars(vars map[string]string, policy ShellEnvironmentPolicy) map[string]string {
	env := make(map[string]string)
	switch policy.Inherit {
	case ShellEnvironmentInheritNone:
		// empty base
	case ShellEnvironmentInheritCore:
		for key, value := range vars {
			if isCoreEnvVar(key) {
				env[key] = value
			}
		}
	default: // all
		for key, value := range vars {
			env[key] = value
		}
	}
	for key := range env {
		if matchesDefaultSecretExclude(policy, key) || matchesAnyGlob(policy.Exclude, key) {
			delete(env, key)
		}
	}
	for key, value := range policy.Set {
		env[key] = value
	}
	if len(policy.IncludeOnly) > 0 {
		for key := range env {
			if !matchesAnyGlob(policy.IncludeOnly, key) {
				delete(env, key)
			}
		}
	}
	if runtime.GOOS == "windows" {
		hasPathext := false
		for key := range env {
			if strings.EqualFold(key, "PATHEXT") {
				hasPathext = true
				break
			}
		}
		if !hasPathext {
			env["PATHEXT"] = ".COM;.EXE;.BAT;.CMD"
		}
	}
	return env
}

func matchesDefaultSecretExclude(policy ShellEnvironmentPolicy, name string) bool {
	if policy.IgnoreDefaultExcludes {
		return false
	}
	return matchesAnyGlob([]string{"*KEY*", "*SECRET*", "*TOKEN*"}, name)
}

func matchesAnyGlob(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if matchEnvGlob(pattern, name) {
			return true
		}
	}
	return false
}

func matchEnvGlob(pattern, name string) bool {
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(name))
	return err == nil && ok
}

func isCoreEnvVar(name string) bool {
	for _, core := range coreEnvVars() {
		if strings.EqualFold(core, name) {
			return true
		}
	}
	return false
}

func coreEnvVars() []string {
	if runtime.GOOS == "windows" {
		return []string{
			"PATH", "PATHEXT", "SHELL", "COMSPEC", "SYSTEMROOT", "SYSTEMDRIVE",
			"USERNAME", "USERDOMAIN", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
			"PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMW6432", "PROGRAMDATA",
			"LOCALAPPDATA", "APPDATA", "TEMP", "TMP", "TMPDIR", "POWERSHELL", "PWSH",
		}
	}
	return []string{
		"PATH", "SHELL", "TMPDIR", "TEMP", "TMP", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "LOGNAME", "USER",
	}
}
