# COMPAT Phase 2 — thematic backlog

One-round doctor/wrap surface closed at `5c6557d`. Remaining gaps need
**one topic per branch**, multi-PR slices, and explicit done criteria.

## Working rules

- No 3-minute “ship anything” loop.
- One theme at a time; open `compat/<theme>` from `main`.
- Each PR: implement + tests + `go test ./...` + COMPAT/README update.
- Prefer Linux-first when OS-specific; document Windows/macOS as follow-ups.
- Align to Rust under `/tmp/gork-build/crates/codegen` when present; otherwise pin behavior in the PR description.

## Priority queue

| # | Theme | COMPAT area | Why now | Rough size |
|---|--------|-------------|---------|------------|
| 1 | **MCP OAuth enrollment** | MCP | Unlocks authenticated remote MCP servers; user-visible | L (multi-PR) |
| 2 | **Linux cgroups for shell** | Shell execution | Completes shell isolation story after wrap | L |
| 3 | **Landlock + seccomp** | OS sandbox | Hardens existing bubblewrap/Seatbelt profiles | XL (ns lockdown landed) |
| 4 | **Cloud ACP / conversations** | ACP | Large protocol surface; defer until local ACP stays green | XL |
| 5 | **Remote relay / workspace hub** | Session / workspace | Depends on product decisions | XL |
| 6 | **Vector memory** | Memory | Research + storage + retrieval | XL |
| 7 | **Fullscreen media / inline video** | Markdown/media | Terminal capability + UX heavy | XL |

Deferred unless explicitly requested: Cursor CLI session discovery, WezTerm kitty XTVERSION probes, Wayland data-control, sandbox.toml profile-conflict doctor.

---

## Phase 1 — MCP OAuth (active plan)

### Goal

Bring Go MCP auth from “local-only / unsupported trigger” to reference-shaped
OAuth enrollment for HTTP/SSE servers, with durable tokens and ACP/TUI status.

### Slice plan

1. **Discovery / inventory** — done (see Phase 1 notes below).

2. **Token store + config** — done (store + atomic 0600 writes; TOML oauth_*
   fields and inspect/doctor redaction cells still open).

3. **Enrollment flow** — done: discovery + DCR + PKCE loopback
   (`AuthenticateMCPServer`). Pasted-callback / cross-process dedup still open.

4. **Runtime use** — done for attach + 401 refresh (TokenURL or rediscovery).
   Doctor auth-state cell still open.

5. **ACP / TUI** — done for `auth_status` / `auth_trigger` / TUI `I`.

### Done when

- [x] Authenticated HTTP MCP server can be added, enrolled, and called in tests.
- [x] Token refresh covered by unit/integration tests.
- [x] COMPAT MCP row mentions OAuth enrollment (no longer “remain”).
- [x] README documents MCP auth UX briefly.

### Phase 1 notes

- Rust store: `$GROK_HOME/mcp_credentials.json`, key `{name}:{url}`, flattened
  map of `StoredCredentials` (`client_id`, `token_response`, `granted_scopes`,
  `token_received_at`). Loopback+PKCE+DCR enrollment; no device-code for MCP.
- Go now mirrors the on-disk shape and attaches/refreshes tokens in
  `internal/mcp` (`credentials.go`, `http_auth.go`). Enrollment, ACP/TUI
  trigger, discovery-derived token URLs, and doctor auth cells remain.

### Out of scope for Phase 1

Managed connectors, agent-level ACP MCP pools, plugin marketplace OAuth.

---

## Phase 2 — Linux cgroups (active)

### Goal

Apply cgroup v2 limits to model-started shell / background tasks on Linux
(memory.high/max as reference; cpu controller enabled but no cpu.max), with
clear no-op on unsupported hosts.

### Done when

- [x] Detect cgroup v2; skip/warn otherwise.
- [x] Foreground + background shell paths covered by tests (or integration with fake fs).
- [x] COMPAT Shell row drops “cgroups remain”.

### Notes

- Shared ProcessManager cgroup for FG/BG/monitor; legacy `shell` tool uses a
  per-invocation guard. ACP terminals and inotify OOM-137 signaling deferred.

---

## Phase 3 — Landlock + seccomp (in progress)

### Done

- [x] Per-child seccomp namespace lockdown inside Linux bwrap
      (`__GROK_SECCOMP_NS__` helper; BPF unit-tested).

### Still open

- [ ] Child network seccomp (defense-in-depth alongside `--unshare-net`)
- [ ] Parent-process Landlock (architecture change; careful with MCP/API)

## Phase 4+ — cloud / memory

Separate kickoff docs. Do not start in parallel without an explicit request.

---

## How to run this plan

```text
# Start Phase 1
Tell the agent: 「按 PLAN-COMPAT-PHASE2.md 做 Phase 1 slice 1」

# Or jump
「先做 Linux cgroups」
```

Update this file’s checkboxes as slices merge.
