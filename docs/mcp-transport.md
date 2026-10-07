# MCP Transport A3

A3 turns MCP from a protocol label in the runtime SDK into an executable transport boundary.

The design goal is not "whatever the MCP server exposes is automatically available." The runtime first discovers a capability surface, canonicalizes it, binds it to a digest, and only then permits calls against that admitted snapshot.

## Flow

```text
MCP server
   |
   v
connect + negotiate protocol
   |
   v
tools/list (all pages)
   |
   v
canonical capability snapshot
   |  endpoint
   |  negotiated protocol version
   |  server identity
   |  tool names
   |  input/output schemas
   |  descriptive server hints
   v
SHA-256 snapshot digest
   |
   +--> ToolDescriptor.snapshot_digest
   |       |
   |       v
   |    RunRequest fingerprint
   |
   v
invoke only tools contained in that snapshot
```

## Official SDK and protocol

A3 uses the official `github.com/modelcontextprotocol/go-sdk` v1.8.0. The SDK supports the MCP `2026-07-28` protocol revision and negotiates down to mutually supported older revisions when needed.

The default connector uses Streamable HTTP. `Connector.Connect` also accepts any official `mcp.Transport`, so command/stdio, in-memory tests, or future transports do not change the runtime contract.

## Snapshot contract

A snapshot digest covers:

- endpoint;
- negotiated MCP protocol version;
- observed server name/version;
- canonical tool ordering;
- tool name, title and description;
- canonical input schema;
- canonical output schema;
- the server-declared read-only hint as descriptive metadata.

The local `Generation` counter is not part of the digest. It records list-change notifications observed by the current session.

The digest is copied into `sdk.ToolDescriptor.SnapshotDigest`, which means the existing `RunRequest.Fingerprint()` now binds execution identity to the exact MCP surface admitted at run construction time.

## No silent authority expansion

A3 installs a `tools/list_changed` handler. When the server signals a tool-list change, the generation counter advances. Any invocation bound to an older generation returns `ErrSnapshotStale`.

The caller must discover again and explicitly bind the new snapshot. Newly exposed tools are never inherited by an already-bound run.

If a list-change notification races with discovery, discovery fails with `ErrSnapshotChangedDiscovery` rather than returning an ambiguous snapshot.

## Tool annotations are not authority

MCP `ToolAnnotations` are server-provided hints. A3 retains `readOnlyHint` in `ToolCapability.DeclaredReadOnly` for inspection, but it does **not** set runtime `ToolDescriptor.ReadOnly=true` from that hint.

A remote server cannot grant itself a stronger runtime policy classification merely by describing a tool as read-only. A later policy/effect classifier may promote a tool only from locally trusted evidence.

## Invocation boundary

Every invocation requires:

- a known snapshot digest;
- a tool name present in that snapshot;
- a snapshot generation that has not been invalidated by a list-change notification.

Arguments are sent through the official SDK. Results are normalized into SDK-neutral JSON:

- unstructured content is preserved as raw JSON content items;
- structured content is canonicalized;
- MCP tool-level errors remain `IsError=true` result data;
- protocol/transport failures are Go errors;
- multi-round-trip input requirements are exposed as `NeedsInput`, `InputRequests`, and `RequestState` rather than guessed away.

## Capability boundary

### What A3 can prove

- the exact MCP tool surface observed at discovery time;
- a deterministic digest for that surface;
- that a requested tool existed in the admitted snapshot;
- that the session has not observed a `tools/list_changed` event since that snapshot;
- that a runtime run fingerprint changes when its bound MCP snapshot changes.

### What A3 cannot prove

- that a remote server's implementation behind an unchanged tool name/schema has not changed;
- that a server which does not emit list-change notifications is immutable;
- that `readOnlyHint`, `destructiveHint`, or other annotations are truthful;
- that an MCP endpoint is trustworthy merely because discovery/connect succeeded;
- exactly-once external effects from a remote tool;
- authorization correctness beyond credentials supplied by the caller's HTTP/transport configuration.

Those limitations are intentional. Capability discovery is evidence of an observed interface, not proof of remote implementation behavior.

## Next slice

A4 builds the Agent Server above A1-A3:

```text
REST / WebSocket / event stream
        |
        v
Agent + Conversation + Workspace
        |
        v
bound RunRequest
   |             |
   v             v
Temporal A2   MCP A3
        \       /
         \     /
          runtime events
```

A4 will expose conversations, runs, event streaming, MCP snapshot admission, and human/input-required continuation without moving engine-specific business logic into the runtime core.
