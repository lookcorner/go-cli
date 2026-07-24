package docs

import "strings"

const BuildURL = "https://docs.x.ai/build/overview"

type Guide struct {
	Title       string
	Description string
	Content     string
}

var guides = []Guide{
	guide("Getting Started", "Installation, first launch, and basic interaction", "Build with `go build ./cmd/gork`, configure credentials, then run `gork --tui` for the full-screen interface or pass a prompt for one-shot mode."),
	guide("Authentication", "Browser login, API keys, OIDC, external auth providers", "Use `gork login` for browser or device authorization. API keys and external credential-provider commands are also supported; `gork logout` removes the selected stored scope."),
	guide("Keyboard Shortcuts", "Complete reference for TUI key bindings", "Use Tab to move between the prompt and scrollback, Esc to cancel or back out, Ctrl-C to cancel a running turn, Ctrl-Q to quit, and Shift-Tab to toggle Plan mode."),
	guide("Slash Commands", "Commands for sessions, models, memory, and tools", "Run `/help` for the capability-aware command list. Commands such as `/model`, `/resume`, `/rewind`, `/memory`, `/mcps`, and `/settings` complete locally without becoming model prompts."),
	guide("Configuration", "config.toml, environment variables, and file locations", "User configuration lives under `~/.grok` by default. Command-line flags override environment variables, which override local and remote configuration unless a managed requirement locks a value."),
	guide("Theming and Appearance", "Themes, timestamps, timeline, and compact display", "Use `/settings` for the supported display preferences or `/theme`, `/timestamps`, `/timeline`, `/compact-mode`, and `/vim-mode` directly. Successful changes are written atomically."),
	guide("MCP Servers", "External tool integrations through MCP", "Configure stdio, Streamable HTTP, or SSE servers in config files, plugins, or ACP session metadata. `/mcps` can inspect, add, remove, reload, and enable servers and tools."),
	guide("Skills", "Reusable prompt packages", "Skills are discovered from user and project scopes with trust and ignore rules applied. Relevant skills are added to the agent context and can provide instructions, scripts, references, and assets."),
	guide("Plugins and Marketplace", "Installing and managing plugin packages", "Use `gork plugin list`, `install`, `update`, `uninstall`, and `marketplace` for local plugin lifecycle. Plugins can contribute skills, agents, hooks, MCP servers, and LSP servers."),
	guide("Hooks", "Project lifecycle scripts for tool and session events", "Hooks run at defined lifecycle events and remain subject to source trust. ACP commands can list, reload, enable, disable, trust, and manage custom hook paths."),
	guide("Custom Models", "BYOK and compatible model endpoints", "Define model entries with provider, endpoint, context window, and reasoning metadata. `/model` switches an idle session while preserving completed text and image history when compatible."),
	guide("Project Rules (AGENTS.md)", "Per-directory instructions and precedence", "`AGENTS.md` files are loaded from the repository root toward the working directory. Deeper instructions take precedence, and ignored or untrusted paths are excluded."),
	guide("Memory", "Cross-session knowledge persistence and search", "`/memory` lists available sources, `/remember` reviews a durable note, `/flush` saves reusable session context, and `/dream` consolidates eligible logs. Memory can be disabled per session."),
	guide("Headless Mode and Scripting", "Non-interactive automation and CI", "Pass a prompt directly for one-shot execution, use `--interactive` for a line-oriented session, or use goal mode for bounded autonomous runs. Exit status reports runtime failures to scripts."),
	guide("Agent Mode and IDE Integration", "ACP stdio transport and client integration", "`--acp` starts the JSON-RPC stdio server used by editor and agent clients. It supports independent sessions, prompt streaming, permissions, questions, models, tools, and extension methods."),
	guide("Subagents and Personas", "Specialized child agents", "The `task` tool runs built-in or custom agents in foreground or background, with bounded depth, inherited policy, isolated context, optional worktrees, and durable lifecycle state."),
	guide("Session Management", "Save, load, resume, fork, rewind, and compact", "Sessions are stored as local JSONL. Use `/resume` to switch, `/fork` to branch completed history, `/rewind` to restore a turn checkpoint, `/compact` to summarize, and `/new` to start fresh."),
	guide("Sandbox Mode", "Shell process filesystem confinement", "Use `--sandbox workspace` to kernel-restrict model-started shell processes to writes under the workspace, temporary directories, and `~/.grok`, or `--sandbox read-only` to remove workspace writes. macOS uses Seatbelt and Linux uses bubblewrap; file tools retain their workspace and symlink checks."),
	guide("Plan Mode", "Read-only planning with approval", "Shift-Tab or `/plan` enters Plan mode. Workspace mutations are limited to `.grok/plan.md`; leaving the mode presents the plan for approval, revision, abandonment, or cancellation."),
	guide("Background Tasks and Monitoring", "Background commands, scheduling, and monitors", "Shell tasks can continue in the background and report completion into the session. `/tasks` shows task sources, while `/loop` expands a recurring request through the scheduler tool."),
	guide("Terminal Support and Troubleshooting", "tmux, SSH, color, clipboard, and mouse diagnostics", "Run `/terminal-setup` to inspect color, clipboard, tmux, SSH, and mouse-reporting behavior. The TUI supports OSC 52 clipboard transfer and tmux-aware OSC 8 links."),
	guide("Permissions and Safety", "Tool approval and managed policy", "Ask, auto, always-approve, and deny modes are constrained by explicit rules and managed policy. Remembered grants match exact requests and can be reset without changing the active mode."),
	guide("Hooks & Plugins Guide", "Using hooks, plugins, and marketplace", "Plugins package reusable agents, skills, hooks, and integrations. Hook execution still follows trust and permission policy; plugin-provided paths are resolved relative to the plugin root."),
	guide("Creating Custom Hooks", "Writing hook commands and matchers", "Place hook configuration in a trusted user or project source, choose the lifecycle event and matcher, and keep commands deterministic. Validate failure behavior before enabling a hook for normal sessions."),
}

func guide(title, description, body string) Guide {
	return Guide{Title: title, Description: description, Content: "# " + title + "\n\n" + body}
}

func All() []Guide {
	return append([]Guide(nil), guides...)
}

func Find(title string) (Guide, bool) {
	title = strings.TrimSpace(title)
	for _, item := range guides {
		if strings.EqualFold(item.Title, title) {
			return item, true
		}
	}
	return Guide{}, false
}
