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

## Phase 2 — Linux cgroups

### Goal

Apply cgroup v2 limits to model-started shell / background tasks on Linux
(CPU/memory as reference), with clear no-op on unsupported hosts.

### Done when

- [ ] Detect cgroup v2; skip/warn otherwise.
- [ ] Foreground + background shell paths covered by tests (or integration with fake fs).
- [ ] COMPAT Shell row drops “cgroups remain”.

---

## Phase 4 — Cloud conversations (active)

### Goal

Merge grok.com cloud chat conversations into ACP `x.ai/session/list`.

### Slice plan

1. **Read-only list + partial status** — done on `compat/cloud-conversations-list`:
   `internal/remote` ConversationsClient, opt-in lane
   (`GROK_SESSION_LIST_CONVERSATIONS` / `GROK_CHAT_MODE`), merge into session
   list, `no_oauth`/`timeout`/`error` partial meta.

2. **Rename / soft-delete** — done: `kind: "chat"` on
   `x.ai/session/rename` / `x.ai/session/delete` maps to PUT + soft DELETE.

2b. **Starred facet + star update** — done on
    `compat/cloud-conversations-starred-facet`: chat rows expose `starred`
    facets, list honors `x.ai/facetFilters.starred`, and rename accepts an
    optional `starred` bool for PUT updates.

2c. **Workspace facet + pushdown** — done on
    `compat/cloud-conversations-workspace-facet`: parse conversation
    `workspaces`, expose `workspace` facets/filters, and push a single
    workspace filter to `workspaceId` on the list API.

3. **Thin chat load/resume + modes catalog** — done: `_meta.x.ai/session.kind=chat`
   on `session/load`/`resume` opens a resident chat session without local JSONL;
   `POST /rest/modes` supplies the model picker when available. Cloud transcript
   replay remains deferred (reference release also hard-offs full chat kind).

4. **Process `--chat` / chat mode** — done: CLI `--chat` sets `GROK_CHAT_MODE`,
   forces session list to chat, opens `session/new` as chat, refuses local Build
   loads, advertises `chatMode` + modes `modelState` on initialize, and rejects
   `--fork-session` / leader conflicts.

### Done when

- [x] Opt-in list merges `kind=chat` rows with httptest coverage.
- [x] Degraded lane reports `_meta["x.ai/partial"]` reasons.
- [x] Rename/soft-delete parity for chat kind.
- [x] Thin chat load/resume + `/rest/modes` picker.
- [x] Process `--chat` / chat-mode ACP defaults.
- [ ] Full cloud transcript replay (follow-up).

### Out of scope for slices 1–4

Fetching and replaying grok.com message history into the local runner.

---

## Phase 3+ — sandbox / memory / relay

Separate kickoff docs when prior themes land. Do not start in parallel without
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
