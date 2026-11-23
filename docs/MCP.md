# Local MCP server

KubeDoctor exposes its diagnostic engine as three tools over stdio. Install the binary using the [installation instructions](../README.md#install), then have an MCP client launch it as a subprocess. The server uses the official Go SDK. The interoperability suite exercises protocol revisions `2026-07-28` and `2025-11-25`. [MCP stdio specification](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/stdio)

## Configure a client

For the Codex CLI, desktop app, or IDE extension, add this table to your local `~/.codex/config.toml`. Substitute absolute paths and use a trusted kubeconfig with the [read-only permissions](../rbac/README.md) needed for the intended scope. The client timeout should exceed the server's per-scan timeout so the client can receive any partial report. [Codex MCP configuration](https://learn.chatgpt.com/docs/extend/mcp#configure-with-configtoml)

```toml
[mcp_servers.kubedoctor]
command = "/absolute/path/to/kubectl-doctor"
args = ["mcp", "serve", "--stdio", "--kubeconfig", "/absolute/path/to/kubeconfig", "--context", "staging", "--allow-namespace", "payments", "--timeout", "30s"]
tool_timeout_sec = 45
```

Other stdio clients can use the same executable and argument list. A Windows command path names `kubectl-doctor.exe`. The server can also be launched through the plugin.

```sh
kubectl doctor mcp serve --stdio --context staging --allow-namespace payments
```

The process resolves kubeconfig and context once at startup. Pass `--kubeconfig` to choose a file and `--context` to select a context. Repeat `--allow-namespace` for each namespace the client may scan. An empty allowlist permits no namespace scans. Add `--enable-cluster` only when the client should be able to scan the whole cluster. `--timeout` defaults to 30 seconds and accepts positive durations up to five minutes.

## Tools and results

| Tool | Arguments | Startup policy |
| --- | --- | --- |
| `diagnose_namespace` | `{"namespace":"payments"}` | The namespace must be a Kubernetes DNS label present in the startup allowlist. |
| `diagnose_cluster` | `{}` | Requires `--enable-cluster`. This is a cluster-wide scan. |
| `list_checks` | `{}` | Available after startup and reads the check inventory without contacting Kubernetes. |

The tools enforce their input schemas, including rejection of extra argument keys. The server uses the Kubernetes credentials and context selected by the operator at startup. Namespace and cluster scopes use the same API transport restrictions as the CLI.

A diagnostic returns the [versioned report](ARCHITECTURE.md#rule-and-report-behavior) as MCP structured content, with a JSON text fallback. Check `complete`, `collection_errors`, and `checks` to interpret a scan with gaps. Available findings are preserved in a partial report. Use MCP tool errors to distinguish bad arguments, denied scopes, an active scan, or the report exceeding its output budget.

The server processes one diagnostic scan at a time. Overlapping scans receive a busy result, and `list_checks` remains available. Cancellation from the client or server interrupts the scan. Inbound JSON-RPC frames are limited to 64 KiB and diagnostic reports to 256 KiB before MCP wrapping. If the report exceeds its limit, rerun with the CLI's `--output json` or with a narrower namespace scope. Stdout carries protocol messages. Startup and session errors go to stderr.

## Verify the transport

```sh
go test ./internal/mcp -count=1 -v
```

The test suite compiles the actual executable, launches it through the SDK client, and runs both specified protocol revisions against a disposable local HTTP Kubernetes API fixture. It also exercises oversized input, invalid arguments, denied scope, scan and session cancellation, timeouts, overlapping calls, and oversized reports.
