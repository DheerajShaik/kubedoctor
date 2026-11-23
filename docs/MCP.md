# Read-only MCP server

The installed `kubectl-doctor` executable exposes the Kubernetes diagnostic engine and investigation model over local stdio. Have an MCP client launch the executable as a subprocess. The interoperability tests exercise MCP protocol revisions `2026-07-28` and `2025-11-25`. [Installation](../README.md#install) [MCP stdio specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/stdio)

## Configure a client

For Codex CLI, the desktop app, or the IDE extension, put this table in the local `~/.codex/config.toml`. Substitute absolute paths and select a trusted kubeconfig with read-only access for the intended scope. Set the client's tool timeout above the server's operation timeout so it can receive the result. [Kubernetes permissions](../rbac/README.md) [Codex MCP configuration](https://learn.chatgpt.com/docs/extend/mcp#configure-with-configtoml)

```toml
[mcp_servers.kubedoctor]
command = "/absolute/path/to/kubectl-doctor"
args = ["mcp", "serve", "--stdio", "--kubeconfig", "/absolute/path/to/kubeconfig", "--context", "staging", "--allow-namespace", "payments", "--timeout", "30s"]
tool_timeout_sec = 45
```

A Windows command path uses `kubectl-doctor.exe`. Other stdio clients can use the same command and arguments. The kubectl plugin accepts the same settings.

```sh
kubectl doctor mcp serve --stdio --context staging --allow-namespace payments
```

Repeat `--allow-namespace` for each namespace the client may scan or inspect. An empty allowlist authorizes no namespace. Both cluster tools and cluster-scoped bundle imports require `--enable-cluster`. The operator selects an existing kubeconfig and context at launch, and the server keeps that selection for the session.

## Capture and investigate

| Tool | Arguments | Result |
| --- | --- | --- |
| `diagnose_namespace` | `{"namespace":"payments"}` | Version 1 diagnostic report for an allowed namespace and its documented dependencies. |
| `diagnose_cluster` | `{}` | Version 1 diagnostic report for an expressly enabled cluster scope. |
| `list_checks` | `{}` | Check inventory. |
| `capture_namespace` | `{"namespace":"payments"}` | Session artifact ID for a new capture in an allowed namespace. |
| `capture_cluster` | `{}` | Session artifact ID for a new capture in an expressly enabled cluster scope. |
| `list_artifacts` | `{}` | Authorized artifact IDs, scopes, evaluation times and observed provenance, in registration order. |
| `inspect_bundle` | `{"artifact_id":"…"}` | Verified inventory, source coverage, versions, timings and integrity digest. |
| `analyze_bundle` | `{"artifact_id":"…"}` | Replayed investigation with the same findings, relationships and timeline as CLI replay. |
| `get_evidence` | `{"artifact_id":"…"}` | Replayed findings, individual evidence, checks and collection gaps. |
| `get_relationships` | `{"artifact_id":"…"}` | The investigation's relationships, provenance and graph limits. |
| `compare_captures` | `{"before_id":"…","after_id":"…"}` | Shared comparison of two authorized artifacts, including comparability and coverage. |
| `historical_findings` | `{"artifact_id":"…"}` | Scoped historical resource findings, or `status: unavailable` when the capture lacks historical evidence. |

Callers supply a namespace or IDs obtained from `capture_namespace`, `capture_cluster`, or `list_artifacts`. Tool schemas reject extra keys, duplicate keys and invalid argument types. Requests for metadata and saved evidence use the registered artifact data alone. `analyze_bundle` and `get_evidence` use matching analyzer rules to replay evidence. Use the CLI for explicit reevaluation or viewing a stored report.

For live historical evidence, add `--history-config /absolute/path/to/history.json` to the startup arguments. The configured provider applies its own exact target policy inside the capture's authorized scope. The capture can retain typed provider gaps alongside Kubernetes observations. Historical tools analyze the saved samples and statuses, including when the provider is absent at replay. [Historical metrics](releases/v0.2.0/HISTORY.md)

## Import existing captures

The operator can delegate specific files in a local directory when starting the server. For example, use the same selected context that produced two payments namespace captures.

```sh
kubectl doctor mcp serve --stdio --context staging --allow-namespace payments \
  --artifact-dir /absolute/path/to/captures \
  --allow-bundle before.json --allow-bundle after.json
```

`--allow-bundle` takes a plain file basename using ASCII letters, digits, periods, underscores or hyphens. Imports are opened relative to the authorized directory and restricted to regular files. Symlinks and Windows reparse points are rejected. Bundle decoding validates normalized evidence and the scope of supported dependencies before registration. The artifact context must match the startup context, and the scope must be authorized. A failed requested import prevents startup. [Traversal-resistant file APIs](https://go.dev/blog/osroot)

For an additional provenance constraint, pass `--artifact-cluster-id DIGEST` using the lowercase SHA-256 value reported by `kubectl doctor inspect`. It applies to both imported and new session artifacts. Namespace captures can carry a digest bound to that namespace identity. Cluster captures require the cluster provenance method when using a digest pin. Context labels and unkeyed integrity digests describe evidence and do not authenticate its producer. Use a trusted artifact directory and delegate only captures the client should see.

Artifacts use opaque, cryptographically random identifiers that expire when the server process exits. The server keeps the verified evidence in memory, reads named local imports only during startup, and creates session captures in memory. It allows eight artifacts, up to 32 MiB each and 64 MiB in aggregate. A full session requires a new server process to release its artifacts. Both captures in a comparison must be authorized, and the shared comparison also checks scope, analyzer version, provenance and observation intervals.

## Limits and verification

The server applies a 64 KiB inbound frame limit and a 256 KiB limit on the serialized result before MCP wrapping. Larger results return an explicit size error. Try a single section, a narrower scope, or CLI output for the complete artifact. A diagnostic or investigation carries its normal coverage and partial findings through MCP structured content with a JSON text fallback. Tool errors represent rejected requests or operation limits.

The server allows one scan, capture or artifact analysis at a time and rejects another operation while that slot is occupied. `list_checks` and `list_artifacts` remain available. `--timeout` defaults to 30 seconds and accepts positive durations up to five minutes. Client cancellation and server shutdown propagate to active work. Stdout carries protocol frames, and command diagnostics use stderr.

```sh
go test ./internal/mcp -count=1 -v
```

The subprocess tests use the actual binary and official SDK client. A disposable local HTTP fixture supplies Kubernetes observations for a broken and repaired workload. The tests compare CLI output with MCP investigations and verify offline operation, stale handles, rejection of unauthorized scopes and arguments, cancellation, concurrency, and result limits.
