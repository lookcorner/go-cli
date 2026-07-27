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
| 3 | **Landlock + seccomp** | OS sandbox | Hardens existing bubblewrap/Seatbelt profiles | done |
| 4 | **Cloud ACP / conversations** | ACP | Large protocol surface; defer until local ACP stays green | XL |
| 5 | **Remote relay / workspace hub** | Session / workspace | Depends on product decisions | XL |
| 6 | **Vector memory** | Memory | Research + storage + retrieval | in progress (schema+config slice) |
| 7 | **Fullscreen media / inline video** | Markdown/media | Terminal capability + UX heavy | XL |

Deferred unless explicitly requested: Cursor CLI session discovery, WezTerm SSH XTVERSION probes.

---

## Phase 5 — Remote relay / workspace hub (blocked)

Product-coupled (`computer-hub.grok.com`, OIDC hub auth, preview supervisor).
Go keeps fail-closed headless + `workspace_exposure: false`. Do not dial hub
without an explicit product unlock. Shipped adjacent S item: sandbox.toml
profile-conflict doctor.

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

## Phase 3 — Landlock + seccomp (done)

### Done

- [x] Per-child seccomp namespace lockdown inside Linux bwrap
      (`__GROK_SECCOMP_NS__` helper; BPF unit-tested).
- [x] Child network seccomp for read-only/strict (`__GROK_SECCOMP_NS_NET__`,
      stacked with namespace lockdown alongside `--unshare-net`).
- [x] Parent-process Landlock FS allowlists on Linux for built-in profiles
      (`ApplyParentLandlock`, BestEffort warn+continue; no parent net restrict).
- [x] Parent bwrap re-exec / hook write-deny for built-in profiles
      (`EnsureParentBwrapHookWriteDeny`, fail-closed; `__GROK_INSIDE_BWRAP=1`).
- [x] Custom `sandbox.toml` profile Landlock parity (`LoadSandboxTOML` /
      `ResolveSandboxProfile`; parent Landlock from base + extras; Linux
      fail-closed for custom when Landlock cannot apply; child wrap uses
      extends base + `restrict_network`).
- [x] Custom `sandbox.toml` `deny` → Linux parent bwrap bind-over
      (mode-000 placeholders; exact + launch-time glob expand; fail-closed).

### Still open

- [x] sandbox.toml profile-conflict doctor (`SandboxProfileConflicts` →
      `gork doctor` / `/doctor` finding; global wins, project cannot redefine)

## Phase 4+ — cloud / memory

Separate kickoff docs. Do not start in parallel without an explicit request.

## Phase 5 note

Remote relay / workspace hub remains planned/XL (product-blocked). See queue #5.

## Phase 6 — Vector memory (in progress)

### Done

- [x] `[memory.embedding]` + search `vector_weight`/`text_weight` config (Rust defaults;
      unset `model` keeps text-only).
- [x] Durable workspace `index.sqlite` schema (`meta`/`chunks`/`chunks_fts`, no
      `chunks_vec` yet); `OpenIndex` / meta helpers.
- [x] FTS5 BM25 + Markdown reindex APIs (`ReindexFile` / `SearchFTS` on
      `IndexDB`).
- [x] Wire FTS into `Store.Search` as candidate narrowing (rankChunks keeps
      decay/weights/MMR; ephemeral and index errors fail open).
- [x] Hybrid score merge helpers + flush `semantic_dedup_threshold` config /
      fail-open hook.
- [x] Portable `chunks_embedding` BLOB store + brute-force L2 KNN,
      `EmbeddingProvider` / `HashEmbeddingProvider`, `EmbedMissing`, and live
      semantic flush dedup when `Runner.MemoryEmbedder` is set.
- [x] OpenAI-compatible `APIEmbeddingProvider` + hybrid FTS/KNN merge in
      `Store.Search` when an embedder is configured (fail open otherwise).
- [x] Session startup wiring: CLI/ACP runners attach `APIEmbeddingProvider`
      from base URL/API key when `[memory.embedding].model` is set.

### Still open

- [ ] Optional sqlite-vec acceleration (Go currently uses brute-force KNN)

---

## How to run this plan

```text
# Start Phase 1
Tell the agent: 「按 PLAN-COMPAT-PHASE2.md 做 Phase 1 slice 1」

# Or jump
「先做 Linux cgroups」
```

Update this file’s checkboxes as slices merge.
