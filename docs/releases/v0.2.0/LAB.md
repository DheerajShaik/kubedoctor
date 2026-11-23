# Incident lab and compatibility

The lab creates and deletes its own kind cluster and kubeconfig. It exercises the installed `kubectl doctor` executable with disposable synthetic workload data. It never consumes the operator's current kubeconfig. It verifies that the selected kind node and published loopback API endpoint belong to the current invocation before fixture mutations. Keep real credentials, clusters and artifacts outside this harness.

## Live matrix

| Kubernetes | Networking and DNS | Main evidence |
| --- | --- | --- |
| 1.35.8 | kind default networking and CoreDNS | Preserved v0.1 read-only behavior and v0.2 connected incident |
| 1.36.4 | kind default networking and CoreDNS, then NodeLocal DNSCache | Existing compatibility plus a distinct supported DNS arrangement |
| 1.36.4 | Cilium 1.20.5 and CoreDNS | A policy-enforcing CNI plus bounded, controlled allow/deny requests |

The kernel and Docker job needs Linux with functioning cgroup delegation. The official kind release includes pinned node images for both Kubernetes patch releases. Cilium's published compatibility covers both minors and its standard kind guide describes disabling the default CNI. NodeLocal DNS uses the Kubernetes 1.36.4 source manifest and kind's standard kube-proxy iptables mode. This matrix evaluates these configurations and synthetic evidence scenarios. Static diagnostics cannot establish traffic delivery, traffic denial, or workload name resolution. [kind v0.33.0](https://github.com/kubernetes-sigs/kind/releases/tag/v0.33.0) [Cilium compatibility](https://docs.cilium.io/en/stable/network/kubernetes/compatibility/) [Cilium kind installation](https://docs.cilium.io/en/stable/installation/kind/) [NodeLocal DNSCache](https://v1-36.docs.kubernetes.io/docs/tasks/administer-cluster/nodelocaldns/)

## Run

Use Bash, Docker, jq, and the pinned Go version from `go.mod`. Install the exact kind and kubectl tools into a temporary directory.

```sh
tools=$(mktemp -d)
scripts/integration-tools.sh 1.36.4 "$tools"
PATH="$tools:$PATH" scripts/integration.sh 1.36.4
```

Run the 1.35.8 baseline with the corresponding pinned installer. Pass `nodelocal` as the second argument to `scripts/integration.sh` for NodeLocal DNSCache on 1.36.4. Pass `cilium` as the third argument to `scripts/integration-tools.sh` and the second argument to `scripts/integration.sh` for Cilium on 1.36.4. The hosted workflow runs the full matrix. On restricted Docker hosts, review the job results for the exact tested revision.

The controlled checkout incident exercises configuration and rollout failures, dependent Service evidence, offline replay, HTML export, same-identity repair, comparison, an RBAC visibility drop, and authorized stdio MCP output. An inert local marker detects unintended startup credential-helper use. The offline replay container gets only synthetic inputs and a writable marker directory and explicitly uses Docker's network-none mode. Test-only traffic probes and fixture writes are isolated to the disposable harness.

The separate historical integration uses a disposable Prometheus server with deterministic seeded samples. Its statistical evidence applies to the fixture. See the [acceptance matrix](ACCEPTANCE.md) for suite scope and completed run evidence.
