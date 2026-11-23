# Release verification record

The development baseline is v0.1.0 at `2f31b9381446458d1ada7c12162bc234660ae448`. The [release acceptance matrix](ACCEPTANCE.md) maps individual outcomes to fixtures. The [PR](https://github.com/DheerajShaik/kubedoctor/pull/2) and its checks are the authoritative record of each pushed head.

## First integrated revision

At `bca2c250b4c8493d8d2d00b1ad1cf58fc8d3f4c7`, a detached Linux amd64 checkout with Go 1.27.4 passed `go test -count=1 ./...`, `go test -race -count=1 ./...`, and `go vet ./...`. The local reachable-symbol govulncheck run against the official advisory snapshot already staged for v0.1 reported no vulnerabilities. The hosted workflow also runs govulncheck against the published database. [Initial hosted run](https://github.com/DheerajShaik/kubedoctor/actions/runs/45155159850)

The compiled plugin passed the healthy and failing namespace loopback API fixture. Original report findings matched capture. Offline replay produced identical JSON, stored mode returned the captured v1 report, and HTML rendered with coverage. Across the fixture runs 104 read-only API calls were observed. The offline operations made zero API requests. The seeded event/header or workload-literal markers were absent from committed reports and process output.

The native Prometheus integration used a checksum-pinned v3.16.0 server. It tested seeded samples from a disposable data directory. Thirty-one original scrapes were distinguished from 61 query steps; adequate and sparse series produced the expected statistics and suppression. The [historical contract](HISTORY.md) documents the fixture and calculations.

The managed desktop's Chromium policy blocks navigation to local file URLs. Local preview loaded the generated HTML into an offline Chromium document and exercised filters, keyboard disclosure, safe inert markup, navigation, mobile width, static content and print text. Hosted CI separately opens a real local file with network disabled and also inspects the comparison. The [browser job](https://github.com/DheerajShaik/kubedoctor/actions/runs/45155159850/job/137626111059) passed at the first integrated revision.

The workspace's rootless runtime lacks the cgroup delegation required by kind. Hosted integration jobs perform the live three-configuration matrix. Kubernetes fixture and RBAC manifests passed strict kubeconform validation at both target patch versions.

The three initial live jobs reached the expanded diagnostic fixture. Each detected the StatefulSet, Job, CronJob, probe, PVC and named-port findings. Each marked the HPA inconclusive when the controller omitted its optional observed generation. The Kubernetes controller can omit both generation fields. The revised rule records the available generation evidence, qualifies recent unversioned conditions and uses UID-matched controller events where available. The fixture explicitly waits for the controller condition and warning. The rerun gates the change.

## Reproducible synthetic timings

Measured on detached PR revision `2c399fe31acb120e37b78088960723b3ee242521` with five iterations per benchmark on Linux amd64, AMD EPYC 9V74, Go 1.27.4 and `GOMAXPROCS=4`. Allocations are total bytes allocated per operation and do not represent peak memory or production capacity.

| Operation | Fixture | Time per operation | Bytes allocated |
| --- | --- | --- | --- |
| Collection only | Loopback API with 250 Pods over two pages, 28 HTTP requests | 25.75 ms | 3,934,102 |
| Core diagnostic engine | 1,000 synthetic Pods | 4.08 ms | 2,257,542 |
| Verified bundle replay | 1,000 synthetic Pods | 50.50 ms | 35,328,977 |
| Build an investigation | 500 Pods, 50 Services, 500 events | 9.41 ms | 3,155,377 |
| Compare investigations | Two synthetic investigation inputs | 4.44 ms | 2,142,508 |
| Render standalone HTML | Two findings, one incident, two historical samples | 0.23 ms | 58,633 |

```sh
GOMAXPROCS=4 go test -run '^$' -bench 'Benchmark(Collection250Pods|SyntheticCluster1000Pods|Replay1000Pods|InvestigationBuild|InvestigationCompare|RenderInvestigation)$' -benchtime=5x -benchmem ./internal/collect ./internal/engine ./internal/bundle ./internal/investigation ./internal/htmlreport
```


## Integrated HPA correction

At `2c399fe31acb120e37b78088960723b3ee242521`, the detached checkout passed the full unit and race suites, vet, workflow lint, byte-for-byte example regeneration, and the local govulncheck scan. A 20-second, two-worker parser fuzz run passed 137,406 executions. The Chromium preview passed comparison and investigation JSON/HTML parity, filter, mobile, keyboard, offline, and printing checks. The corrected HPA fixture exercises same-UID controller warning evidence on capture and offline replay. [Hosted verification for this revision](https://github.com/DheerajShaik/kubedoctor/actions/runs/45156572042)

The second hosted run reached the repaired HPA assertion on all three clusters. The 1.36.4 NodeLocal job verified daemon availability and a disposable workload lookup. The Cilium job verified allowed and denied fixture traffic with positive controls. [Cilium job log](https://github.com/DheerajShaik/kubedoctor/actions/runs/45156572042/job/137630936104) After the missing ConfigMap was created, the recovery harness read the previously recorded rollout deadline before the kubelet and controller reconciled. The updated harness waits for the original Pod UID to become Ready and verifies available replicas and an affirmative Deployment progress condition before requesting rollout status. [NodeLocal job log](https://github.com/DheerajShaik/kubedoctor/actions/runs/45156572042/job/137630936065)

External Codex review of `2c399fe` identified lost standalone ReplicaSet status and an incorrect repair classification when admission or scheduling events age out. The follow-up retains controller generation/readiness through collection and replay and distinguishes event expiry from verified recovery. Review receipts and dispositions remain attached to the PR. [External review](https://github.com/DheerajShaik/kubedoctor/pull/2#pullrequestreview-6020045003)
