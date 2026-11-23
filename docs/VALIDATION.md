# Validation

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
| Repository | Verified private, correct owner and main branch via GitHub connector |
| Runtime | Linux amd64, Go 1.27.4 verified by official SHA-256, rootless Docker server 29.9.2 reachable |
| Release gates | Awaiting implementation |

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
