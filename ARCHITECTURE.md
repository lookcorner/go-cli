# Architecture

Gork Go uses lightweight domain-driven boundaries. The goal is to keep domain
rules out of protocols without creating layers that have no second
implementation.

## Bounded contexts

- `internal/session` owns session identity, JSONL events (including explicit
  local user feedback), transcript recovery, metadata, artifact namespaces,
  and session forking.
- `internal/worktree` owns worktree identity, lifecycle, Git state transfer,
  conflict rules, registry maintenance, and historical code restoration.
- `internal/workspace` owns confined paths, instruction discovery, release-build
  folder trust, Gitignore-aware file indexing and watching, and durable
  before/after file checkpoints used by session rewind.
- `internal/agent` owns the model/tool turn loop and context compaction.
- `internal/suggest` owns shell-token parsing, local command/file completion,
  history ranking, safe insertion text, and suggestion aggregation.
- `internal/memory` owns workspace-scoped cross-session memory files, bounded
  context reads, exact deduplication, and atomic persistence.
- `internal/auth` owns login protocols, credential policy, scoped persistence,
  and token refresh serialization.
- `internal/config` owns managed, user, environment, requirements, signed remote policy,
  OS-enforced MDM precedence, and compatible MCP/LSP configuration source merging.
- `internal/plugin` owns local plugin manifests, discovery precedence, enablement,
  stable identity, data paths, and component-path confinement.
- `internal/hooks` owns plugin hook parsing, durable disablement, matching, safe
  command/HTTP execution, and fail-open versus explicit-deny semantics.
- `internal/agents` owns portable plugin agent-definition discovery and parsing;
  project/user/plugin precedence, and the immutable callable catalog.
- `internal/subagent` owns child-runner lifecycle, foreground/background task
  state, cancellation, resume, and tool-capability filtering coordination.
- `internal/compat` owns resolved vendor compatibility values shared by
  configuration, instruction discovery, and skill discovery.
- `internal/memory` owns workspace-isolated persistence, safe retrieval,
  Markdown chunking, and deterministic text ranking; tool adapters only format
  these domain results.

## Adapters

- `internal/acp` translates ACP JSON-RPC requests and responses.
- `cmd/gork` wires configuration, domains, and interactive or ACP transports.
- `internal/api`, `internal/mcp`, and `internal/lsp` adapt external protocols.
- `internal/tools` adapts domain-safe workspace operations to model tools. It
  also owns the small session-local aggregates that exist only through the
  tool surface: todos, goal state, background processes, and scheduled prompts.

Adapters may coordinate domain operations, but filesystem, Git, session, and
conflict decisions belong in their bounded context. Domain packages do not
depend on ACP, CLI, TUI, or provider wire types.

## Design rules

1. Prefer a concrete type and a small function over an interface with one
   implementation.
2. Add an abstraction only when it removes repeated policy or supports a real
   alternate implementation.
3. Keep protocol DTO conversion at the adapter boundary.
4. Make destructive operations fail closed and verify object identity before
   changing disk state.
5. Test domain invariants directly, then add one adapter-level contract test.

This structure is intentionally small: packages are the primary boundaries;
there is no repository layer, dependency injection container, or generic event
bus.
