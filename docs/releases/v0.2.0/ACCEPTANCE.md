# Release acceptance evidence

The [release charter](RELEASE_CHARTER.md) defines mandatory outcomes. Evidence entries must identify the checked revision and actual command, job or review result. A configured but unexecuted check remains pending.

| Outcome | Verification surface |
| --- | --- |
| 1. Healthy fixture has no actionable false incident | `scripts/integration.sh` healthy scans and pinned live matrix |
| 2. Config/rollout failure forms a coherent investigation | `scripts/integration.sh` connected checkout Deployment, Pod, ConfigMap and Service fixture |
| 3. Captured evidence replays equivalently | Exact-analyzer replay contract tests and normalized JSON equality in the connected lab |
| 4. Replay runs with networking disabled and unusable kubeconfig | Connected lab Docker network-none pass, invalid configuration inputs and inert helper marker |
| 5. Verified repair appears as recovery | Connected lab wait for ready Deployment and unchanged primary workload/Pod identities before comparison |
| 6. Loss of visibility or compatibility never masquerades as recovery | Scoped RBAC loss in the lab and comparison fixtures covering absent APIs, stale/truncated sources, changed rules and scope |
| 7. Replacement and normal replica churn are distinguished | Deterministic comparison object and workload fixtures |
| 8. Offline HTML agrees with shared diagnosis | Connected lab exports, independent browser filtering/layout/print/privacy checks |
| 9. Historical statistics and suppressions are reproducible | Historical calculator fixtures and real Prometheus integration with seeded synthetic series |
| 10. Prometheus failure preserves core diagnosis | Historical source failure and capture tests, explicit source status |
| 11. HPA and sizing interaction is explained | Metrics sizing fixtures with utilization HPA and resource request links |
| 12. Expanded rules handle expected, broken, recovered and insufficient cases | Live diagnostic expansion scenarios, relationship and unsafe-claim unit fixtures |
| 13. Sensitive strings remain out of public evidence | Transport, bundle, renderer, integration and MCP sentinel tests |
| 14. Real client enforces MCP boundaries | SDK subprocess tools, startup scopes, size, cancellation, concurrency, and connected authorized bundle check |
| 15. Existing commands, schema and thresholds work | Legacy CLI/JSON and exit tests plus original live integration checks |
| 16. Connected workflow exercises failure, evidence, offline replay, HTML, repair, comparison and MCP | `scripts/integration.sh` connected checkout incident |
| Compatibility | Live matrix in [lab](LAB.md), platform cross-builds and any measured platform runs |
| Static, dynamic and safety validation | Formatting, dependency checks, vet, race, reachable vulnerability scan, parser robustness and fuzz tests |
| Performance | Reproducible collection, diagnosis, replay, compare and render benchmarks with fixture sizes |
| External release gates | Submitted review of current implementation, approval and branch rules, verified merge, default-branch runs and unpublished draft artifacts |

## Run record

The selected workspace serves the existing repository and contains a rootless Docker 29.9.2 daemon. A fresh v0.2 kind probe failed before node creation because that environment lacks cgroup delegation. Hosted CI runs the live matrix.

Local verification in the implementation workspace based on `bca2c25` passed strict structural validation for all 45 Kubernetes and RBAC resources using both 1.35.8 and 1.36.4 schemas, workflow lint, and Bash syntax. The separately compiled CLI and real MCP SDK agreed on before/after investigations and recovery from synthetic API responses. A network-none container with a positive isolation and marker control replayed the same evidence twice while the challenge kubeconfigs remained unusable. See the [implementation review](IMPLEMENTATION_REVIEW.md) and [validation log](VALIDATION.md) for the distinction between local observations and current-head release gates.

The first hosted run reached the expanded diagnostics in all three matrix jobs and exposed a false stale judgment for stock unversioned HPA conditions. Those jobs failed and did not reach the connected workflow or specialized DNS/CNI scenarios. The fixture now records the two generation fields, exact condition and Warning evidence needed to validate the corrected interpretation. [First hosted run](https://github.com/DheerajShaik/kubedoctor/actions/runs/45155159850)
