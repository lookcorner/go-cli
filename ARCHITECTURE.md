# Architecture

Gork Go uses lightweight domain-driven boundaries. The goal is to keep domain
rules out of protocols without creating layers that have no second
implementation.

## Bounded contexts

- `internal/session` owns session identity, JSONL events, transcript recovery,
  metadata, and session forking.
- `internal/worktree` owns worktree identity, lifecycle, Git state transfer,
  conflict rules, registry maintenance, and historical code restoration.
- `internal/agent` owns the model/tool turn loop and context compaction.
- `internal/compat` owns resolved vendor compatibility values shared by
  configuration, instruction discovery, and skill discovery.

## Adapters

- `internal/acp` translates ACP JSON-RPC requests and responses.
- `cmd/gork` wires configuration, domains, and interactive or ACP transports.
- `internal/api`, `internal/mcp`, and `internal/lsp` adapt external protocols.
- `internal/tools` adapts domain-safe workspace operations to model tools.

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
