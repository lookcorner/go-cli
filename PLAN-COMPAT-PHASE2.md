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
| 3 | **Landlock + seccomp** | OS sandbox | Hardens existing bubblewrap/Seatbelt profiles | XL |
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
   fields parsed; `gork inspect` MCP auth presence cells done — no secrets).

3. **Enrollment flow** — done: discovery + DCR + PKCE loopback
   (`AuthenticateMCPServer`) plus in-process single-flight and Unix
   `mcp_auth_*.lock` cross-process dedup with credential-store poll while
   waiting on the loopback callback. Pasted-callback UX still open.

4. **Runtime use** — done for attach + 401 refresh (TokenURL or rediscovery).
   Doctor auth-state cell done (`gork mcp doctor` `oauth credentials` check).

5. **ACP / TUI** — done for `auth_status` / `auth_trigger` / TUI `I`.

### Done when

- [x] Authenticated HTTP MCP server can be added, enrolled, and called in tests.
- [x] Token refresh covered by unit/integration tests.
- [x] COMPAT MCP row mentions OAuth enrollment (no longer “remain”).
- [x] README documents MCP auth UX briefly.
- [x] `gork mcp doctor` reports an `oauth credentials` check for HTTP/SSE
      (static bearer / env / store; fail-closed with enroll hint).
- [x] `gork inspect` MCP cells expose auth=static|env|stored|oauth_byo|none
      plus env-var names only (never tokens/headers/secrets).
- [x] Concurrent enrollments share one browser flow (in-process + Unix flock
      + store poll).

### Phase 1 notes

- Rust store: `$GROK_HOME/mcp_credentials.json`, key `{name}:{url}`, flattened
  map of `StoredCredentials` (`client_id`, `token_response`, `granted_scopes`,
  `token_received_at`). Loopback+PKCE+DCR enrollment; no device-code for MCP.
- Go now mirrors the on-disk shape and attaches/refreshes tokens in
  `internal/mcp` (`credentials.go`, `http_auth.go`). Optional pasted-callback
  UX for headless enroll remains.

### Out of scope for Phase 1

Managed connectors, agent-level ACP MCP pools, plugin marketplace OAuth.

---

## Phase 2 — Linux cgroups (next)

### Goal

Apply cgroup v2 limits to model-started shell / background tasks on Linux
(CPU/memory as reference), with clear no-op on unsupported hosts.

### Done when

- [ ] Detect cgroup v2; skip/warn otherwise.
- [ ] Foreground + background shell paths covered by tests (or integration with fake fs).
- [ ] COMPAT Shell row drops “cgroups remain”.

---

## Phase 3+ — sandbox / cloud / memory

Separate kickoff docs when Phase 1–2 land. Do not start in parallel without
an explicit request.

---

## How to run this plan

```text
# Start Phase 1
Tell the agent: 「按 PLAN-COMPAT-PHASE2.md 做 Phase 1 slice 1」

# Or jump
「先做 Linux cgroups」
```

Update this file’s checkboxes as slices merge.
