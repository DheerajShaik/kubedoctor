# Architecture

KubeDoctor runs one bounded scan against the Kubernetes API and evaluates a minimized snapshot in memory. The CLI and stdio MCP server share the collector, rules, and versioned report. The client needs a kubeconfig and access to the resources relevant to the scan.

## Package boundaries

| Package | Responsibility |
| --- | --- |
| `cmd/kubectl-doctor`, `internal/cli` | Parse inputs, resolve scope and kubeconfig overrides, render a report, and select the process exit status. |
| `internal/engine` | Coordinate collection and analysis. Apply stable ordering, filtering, and report completeness policy. |
| `internal/collect` | Build read-only client-go clients, retrieve supported resource observations, classify API failures and event evidence, and track collection coverage. |
| `internal/model` | Define the snapshot, resource identity, scope, finding, check result, and versioned report. |
| `internal/analyze` | Evaluate observations with deterministic rules. Publish rule identifiers and metadata for both entry points. |
| `internal/mcp` | Register typed read-only tools and enforce the startup scope policy, request budgets, and stdio protocol. |

The CLI and MCP adapter call the engine. The engine depends on a small collector interface and the rule package. Rules take snapshot and time inputs and have no I/O. New rules add metadata, state the observations and visibility they require, and reuse the report schema. A new source also needs a bounded collection plan and documented RBAC.

## Collection and visibility

Resolve the requested context without updating kubeconfig. Use the chosen context, namespace, and scan time consistently across collection and analysis. A namespace request enumerates namespaced resources only in its selected namespace. It can GET nodes hosting its pods and storage classes named by its PVCs. DNS collection reads the conventional DNS Service and label-selected workloads in the system namespace. Report unresolved dependencies and permission failures. The cluster command explicitly allows all-namespace resource collection and node inventory.

Use typed API clients with a scan deadline, bounded concurrent work, client-side rate limits, and pagination. Bound the number of pages, retained observations, dependent lookups, and retry attempts. Discovery differentiates unavailable API resources from access failures when evidence permits it. Treat exact missing-object responses separately from missing API versions. Prefer EndpointSlices across the matching service identity. Use Endpoints as a compatibility path only when discovery or a confirmed API absence warrants it.

The snapshot tracks whether each source or required lookup was complete, inaccessible, unavailable, failed, truncated, or outside the scan. Attach resource identity and the data needed for the rules. Discard application values, raw messages, annotations, and unrelated object fields during collection. Rules can report evidence present in partial results. Inferring absence requires verified coverage of the relevant source. A skipped, inconclusive, denied, or unavailable check remains visible in the report.

## Rule and report behavior

Rules attach the stable identifier and resource kind, namespace, name, and UID when available. Findings carry severity, the observed symptom, structured evidence, confidence, and an actionable next step. Evidence states what was seen and when. A probable cause requires corroborating observations and carries an uncertainty label where appropriate.

Prefer Kubernetes conditions and structured status. Correlate relationships by UID where the API provides it and identify by scope and name only where resource references are intentionally name-based. Classify recent events at collection, then correlate them to existing resource identities. Recognized events corroborate their related rule. The general event rule covers relevant warnings without duplicating symptoms already described by a more specific rule. Use snapshot time for all age decisions.

The report identifies its schema version, scope, and collection time, and carries check outcomes, findings, and structured collection errors. A stable sort covers findings, checks, sources, and ordered evidence fields. Check outcomes and collection errors survive severity filtering. Exit classification uses the required-coverage result first, then the unfiltered finding threshold. A partial report remains renderable even when a deadline or access failure prevents complete coverage. Optional-source limitations remain visible on successful command exits.

## Trust boundaries

- Startup kubeconfig and its configured authentication mechanism are local operator inputs. Kubernetes objects and remote API error content are untrusted observations. MCP tool arguments are also untrusted.
- The Kubernetes transport permits read methods and the collector uses explicit resource endpoints. Diagnostic requests exclude Secret objects, workload logs, proxy, port-forward, exec and attach subresources, and mutations. A seeded transport fixture records HTTP operations and fails on forbidden requests.
- Emit curated evidence fields and fixed error/status descriptions. Never emit raw API messages, event text, Kubernetes object dumps, container environment values, credentials, or logs. Keep observed Kubernetes identifiers bounded according to their documented shape.
- Use read-only RBAC examples tailored to namespace inventory, referenced dependencies, DNS inspection, and explicit cluster scope. Document optional access and the resulting coverage if it is omitted.

## MCP boundary

The stdio server captures kubeconfig, context, timeout, and authorized scopes at startup. The namespace tool accepts a namespace only within the startup allowlist. Cluster scope has a separate explicit enablement. Tool schemas permit scan inputs only. Filesystem locations, API hosts, auth, commands, and mutation fields are outside the schema.

Apply the scan deadline, propagate cancellation, cap concurrent calls and serialized responses, and return explicit errors when resource or response budgets prevent normal completion. Keep protocol frames on stdout and operational diagnostics on stderr. The tool response reuses the CLI report schema. Read-only annotations describe the tools, and the executable enforces its endpoint and scope policies.

## Compatibility and extension

The compatibility matrix distinguishes actual Kubernetes integration runs from cross-compiled binaries. Diagnostic checks are limited to available APIs and the evidence they expose. Static NetworkPolicy inspection identifies isolation candidates, DNS inspection shows component and backend state, and Metrics API readings represent current samples only. See [requirements](REQUIREMENTS.md), [validation](VALIDATION.md), and [roadmap](ROADMAP.md).
