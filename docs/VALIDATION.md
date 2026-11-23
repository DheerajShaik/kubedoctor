# Validation

See the [v0.2.0 verification record](releases/v0.2.0/VALIDATION.md), [acceptance matrix](releases/v0.2.0/ACCEPTANCE.md), and [lab](releases/v0.2.0/LAB.md) for investigation validation. The initial-release evidence is retained below.

Run from the repository root with the version in go.mod.

```sh
go test ./...
go vet ./...
go test -race ./...
go build ./cmd/kubectl-doctor
```

Record exact commands, revision, results, and limitations below as checks run.

| Check | Observed result |
| --- | --- |
| Frozen charter | Attachment and `PROJECT_CHARTER.md` matched byte for byte |
| Repository | Owner and main branch verified. The v0.2 baseline check confirmed public visibility. |
| Runtime | Linux amd64, Go 1.27.4 verified by official SHA-256, rootless Docker server 29.9.2 reachable |
| Hosted release gates | Tracked on [PR #1](https://github.com/DheerajShaik/kubedoctor/pull/1) for its current head |

## Transport smoke with the real executable

Use this test with a kubectl binary on the path passed as the second argument. It installs the built plugin to a temporary directory and serves synthetic Kubernetes API responses on loopback.

```sh
plugin_dir=$(mktemp -d)
go build -o "$plugin_dir/kubectl-doctor" ./cmd/kubectl-doctor
PATH="$plugin_dir:$PATH" kubectl plugin list
PATH="$plugin_dir:$PATH" kubectl doctor version
python3 test/transport/smoke.py "$plugin_dir" "$(dirname "$(command -v kubectl)")"
```

## Live suite

Run from Linux amd64 with a Docker runtime that supports kind.

```sh
tools_dir=$(mktemp -d)
scripts/integration-tools.sh 1.36.4 "$tools_dir"
PATH="$tools_dir:$PATH" scripts/integration.sh 1.36.4
```

Repeat with `1.35.8`. The harness verifies the exact Kubernetes server and kubectl versions, installs the plugin to a temporary PATH and provisions a uniquely named disposable cluster. It deletes its cluster and kubeconfig on exit. GitHub CI runs the two versions in separate jobs.

## Observed checks

- `go mod verify`, formatting, `go vet ./...`, `go test -count=1 ./...`, and `go test -race -count=1 ./...` passed after the independent review fixes and dependency update.
- The executable passed real kubectl discovery and namespace invocations against the loopback API fixture. The healthy case returned exit 0 and no findings. The broken image case returned exit 1 and two findings. The fixture saw 38 reads and its seeded Warning header appeared in neither process stream.
- The process-level [MCP tests](MCP.md#verify-the-transport) passed with the pinned SDK client under protocols `2026-07-28` and `2025-11-25`.
- `govulncheck` 1.10.0 reported no reachable vulnerabilities after updating `golang.org/x/net` to v0.63.0. The local scan used Go's official advisory source at `golang/vulndb@08d4efaf08a5f66964db742ab4c3b4bacabb274e`, generated with its `cmd/gendb` because the cloud shell's network policy denied the published database endpoint. The hosted scan uses the published database.
- The [first hosted run](https://github.com/DheerajShaik/kubedoctor/actions/runs/45128531260) built all six targets. Its integration jobs discovered KubeDoctor but failed the plugin-list command because the runner had duplicate installations of `kubectl-convert`. Discovery is now confined to the temporary test path. The first scan's HTTP/2 dependency advisories were reproduced locally and fixed. The current run on PR #1 is the release gate.
- Local live kind runs could not start a cluster in managed rootless Docker because cgroup delegation was unavailable. The integration fixtures passed strict schema validation on Kubernetes 1.35.8 and 1.36.4. Live tests run on hosted CI.

## Synthetic benchmark

```sh
go test ./internal/engine -run '^$' -bench BenchmarkSyntheticCluster1000Pods -benchmem -count=3
```

On the cloud Linux amd64 AMD EPYC 9V74 host with Go 1.27.4 and GOMAXPROCS 4, three samples measured 2.53–4.09 ms per scan and about 2.04 MB allocated. Each scan analyzed 1,000 synthetic pods across 20 namespaces with every tenth pod waiting in CrashLoopBackOff. The benchmark includes rule evaluation and report construction. Kubernetes API I/O and rendering are outside the measurement.

## Package binaries

On Linux with GNU tar, gzip, zip and sha256sum, use the pinned Go toolchain.

```sh
scripts/release.sh v0.1.0
(cd dist && sha256sum -c SHA256SUMS)
```

The six archives cover amd64 and arm64 for Linux, macOS and Windows. Cross-compilation verifies builds on those targets. Linux amd64 is the runtime under test here.
