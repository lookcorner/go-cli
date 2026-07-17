# Gork Go

Gork Go is a Go reimplementation of [Gork Build](https://github.com/thedavidweng/gork-build),
the privacy-oriented community build of the Grok Build coding agent.

The project is under active compatibility development. The current runtime is
a usable headless coding agent with a Responses-compatible streaming client,
function-tool loop, workspace confinement, explicit mutation approval and
local JSONL session records. See [COMPATIBILITY.md](COMPATIBILITY.md) for the
feature-by-feature status.

## Build

Go 1.24 or newer is required.

```sh
go test ./...
go build -o gork ./cmd/gork
```

The runtime currently uses only the Go standard library.

## Configure

Use environment variables for credentials:

```sh
export XAI_API_KEY="..."
export GORK_MODEL="a-responses-compatible-model"
```

The default API base URL is `https://api.x.ai/v1`. Override it with
`GORK_BASE_URL`, `--base-url`, or a JSON config file. The default config file
is `$XDG_CONFIG_HOME/gork-go/config.json` on Unix-like systems and the
corresponding user config directory on other platforms.

```json
{
  "base_url": "https://api.x.ai/v1",
  "model": "YOUR_RESPONSES_API_MODEL",
  "max_steps": 20,
  "http_timeout": "10m"
}
```

API keys may be put in the config for compatibility, but environment variables
are preferred so secrets are not stored in a plain-text file.

## Run

```sh
./gork --workspace /path/to/project "inspect this repository and run its tests"
```

Prompts can also be piped through stdin:

```sh
printf '%s\n' 'explain the failing test' | ./gork --workspace .
```

Local mutations require confirmation by default:

- `--approval prompt`: ask before every file mutation and shell command.
- `--approval deny`: allow only read-only tools.
- `--approval auto`: approve all available local tools. Use only in a trusted
  workspace and environment.

The built-in tools are `read_file`, `list_files`, `search_files`, `write_file`,
`edit_file`, and `shell`. File operations resolve symlinks and reject paths
outside the selected workspace. Shell commands start in the workspace, but
they are not yet kernel-sandboxed; approval remains a security boundary.

Each run is recorded as a mode-0600 JSONL event log under the user cache
directory. `--session-dir` selects another location.

## Privacy

Gork Go does not include product analytics, research trace uploads, repository
packaging uploads, or vendor auto-update code. Prompts and tool results used by
the agent are sent to the configured model endpoint because remote inference
requires them. Session records stay local unless the user moves or uploads
them.

## License and attribution

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
