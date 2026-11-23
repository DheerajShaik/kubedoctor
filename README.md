# KubeDoctor

KubeDoctor inspects Kubernetes workload, scheduling, configuration, storage, networking and disruption symptoms with read-only API calls. It reports what it observed, the confidence of each finding and where permissions or missing data limited the scan.

```sh
kubectl doctor namespace payments
kubectl doctor namespace payments --output json --context staging
kubectl doctor cluster --timeout 60s --fail-on critical
kubectl doctor checks
kubectl doctor version
```

## Investigate an incident

```sh
kubectl doctor capture namespace payments --bundle before.json
kubectl doctor inspect before.json
kubectl doctor investigate before.json -o html --file incident.html
kubectl doctor replay before.json -o json
kubectl doctor capture namespace payments --bundle after.json
kubectl doctor compare before.json after.json --fail-on warning
```

Capture uses the selected Kubernetes context. Replay, comparison and report generation work from local bundles. An investigation connects findings to resources and supporting evidence. Collection gaps remain visible when a repair cannot be verified. Follow the [offline example and upgrade instructions](docs/releases/v0.2.0/MIGRATION.md), [historical provider configuration](docs/releases/v0.2.0/HISTORY.md), and [diagnostic scope](docs/releases/v0.2.0/DIAGNOSTICS.md).

## Install

Download the archive and `SHA256SUMS` for your platform from the [releases](https://github.com/DheerajShaik/kubedoctor/releases), check the archive's SHA-256, then put `kubectl-doctor` (or `kubectl-doctor.exe` on Windows) on your PATH. The executable also works directly. Run `kubectl plugin list` to check discovery.

Build from the checkout with the Go version in `go.mod`. On macOS or Linux:

```sh
mkdir -p "$HOME/.local/bin"
go build -o "$HOME/.local/bin/kubectl-doctor" ./cmd/kubectl-doctor
export PATH="$HOME/.local/bin:$PATH"
kubectl doctor namespace payments
```

On Windows, build with `go build -o kubectl-doctor.exe ./cmd/kubectl-doctor` and move the executable to a directory on PATH.

## Scope and results

`namespace [name]` uses the positional name or `--namespace`. If both are present, they must agree. If omitted, the selected kubeconfig context supplies the default namespace. `cluster` explicitly requests all namespaces. `--kubeconfig` and `--context` select the existing kubeconfig and context for the invocation. The default scan timeout is 30 seconds and `--timeout` accepts a positive duration up to five minutes.

A scan exits with **0** when required collection is complete and findings stay below the threshold, **1** when required collection is complete and at least one finding meets the threshold, or **2** when required collection is incomplete or the invocation fails. A partial report preserves available findings. `--fail-on warning` is the default. Other choices are `info`, `critical` and `none`. `--min-severity` filters displayed findings independently of exit status.

JSON schema version `1` includes scope, collection time, required completeness, findings, checks and source status. Optional source losses and inconclusive checks remain visible even when exit status is 0. Terminal output groups correlated findings by resource or controller. Use `kubectl doctor checks` for the current rule list.

KubeDoctor supports Kubernetes 1.35 and 1.36 at the depth described in [requirements](docs/REQUIREMENTS.md). Secret-key diagnosis relies on pertinent evidence from events. Application logs and Secret bodies are excluded. The Metrics API supplies current samples. An explicitly configured Prometheus source supplies historical observations for authorized targets. Static policy and DNS component observations cannot establish packet delivery or workload name resolution. Scans can return partial reports when request or inventory limits are reached. See the [access examples](rbac/README.md) and [architecture limits](docs/ARCHITECTURE.md).

## MCP

Start the stdio adapter with `kubectl doctor mcp serve --stdio --allow-namespace payments`. See [MCP configuration](docs/MCP.md) for client setup and tool policies.

## Development

- [Contributing](CONTRIBUTING.md), [security](SECURITY.md) and [changelog](CHANGELOG.md)
- [Frozen charter](PROJECT_CHARTER.md), [decisions](docs/DECISIONS.md) and [research](docs/RESEARCH.md)
- [Project status](docs/PROJECT_STATUS.md), [validation evidence](docs/VALIDATION.md) and [roadmap](docs/ROADMAP.md)
