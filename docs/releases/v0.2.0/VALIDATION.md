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

Five iterations per benchmark on Linux amd64, AMD EPYC 9V74, Go 1.27.4 and `GOMAXPROCS=4`. Allocations are total bytes allocated per operation and do not represent peak memory or production capacity.

| Operation | Fixture | Time per operation | Bytes allocated |
| --- | --- | --- | --- |
| Collection only | Loopback API with 250 Pods over two pages, 28 HTTP requests | 18.75 ms | 3,894,446 |
| Core diagnostic engine | 1,000 synthetic Pods | 3.89 ms | 2,257,769 |
| Verified bundle replay | 1,000 synthetic Pods | 102.44 ms | 35,328,532 |
| Build an investigation | 500 Pods, 50 Services, 500 events | 15.38 ms | 3,155,406 |
| Compare investigations | Two synthetic investigation inputs | 3.39 ms | 2,142,454 |
| Render standalone HTML | Two findings, one incident, two historical samples | 0.25 ms | 58,665 |

```sh
GOMAXPROCS=4 go test -run '^$' -bench 'Benchmark(Collection250Pods|SyntheticCluster1000Pods|Replay1000Pods|InvestigationBuild|InvestigationCompare|RenderInvestigation)$' -benchtime=5x -benchmem ./internal/collect ./internal/engine ./internal/bundle ./internal/investigation ./internal/htmlreport
```
