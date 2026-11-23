# Requirements and acceptance

The release serves application developers and on-call operators who can read Kubernetes resources and need repeatable triage from a terminal or a local MCP client. The frozen source of requirements is [FROZEN-v1.0](../PROJECT_CHARTER.md). Releases use a documented support matrix with the validation evidence for each claimed Kubernetes version and build target.

## Required product behavior

- Install a standalone `kubectl-doctor` executable on PATH. Support direct invocation and kubectl plugin discovery. Provide the namespace, cluster, checks, version, and stdio MCP commands with help and examples.
- Support explicit kubeconfig and context, bounded scan time, predictable namespace selection, output format, severity filtering, and failure threshold. Require intentional cluster scope.
- Keep CLI and MCP diagnostics deterministic against an input snapshot. Version the JSON schema and identify scan scope, time, findings, collection errors, and incomplete check outcomes.
- Return exit 0 for completed required coverage below the failure threshold, 1 when completed required coverage reaches the threshold, and 2 for operational failure or incomplete required coverage. Retain collected findings when coverage fails. Use presentation filtering independently of classification and preserve check visibility.
- Minimize Kubernetes reads and emitted evidence. Provide example RBAC for supported scopes. Deliver secure stdio MCP interoperability over the same engine with startup-bound credentials, an explicit namespace allowlist, and separate cluster enablement.
- Deliver source, reproducible verification, tested binary artifacts and checksums, maintenance files, and the authorized PR/review/release workflow. Record external gates with observed evidence tied to the relevant revision.

## Diagnostic acceptance matrix

Each rule needs evidence of a finding, a healthy or exempt case, and a visibility-limited outcome where its required observations are absent. The examples below specify the regression evidence for rule and fixture tests. Live test coverage and exact outcomes are recorded in [validation](VALIDATION.md).

| Charter category | Rule behavior | Acceptance evidence |
| --- | --- | --- |
| Nodes | Report observed NotReady and active pressure conditions with transition time where available. | False and Unknown readiness are distinguished. Active pressure is reported. A healthy node is calm. A forbidden or missing hosting-node read is visible. |
| Pods and init containers | Report waiting, termination, image pull, repeated restart, observed CrashLoopBackOff, and recent OOMKilled evidence with lifecycle context. | CrashLoopBackOff and CreateContainerConfigError remain different. Exit 137 alone cannot justify OOM. Historical recovered OOM has distinct impact. Healthy and completed workloads remain calm. Missing statuses are visible where relevant. |
| Scheduling | Correlate unschedulable status with recent scheduler events. | A matching event strengthens evidence. Stale and mismatched UID events cannot corroborate a cause. Pending alone does not imply a scheduler cause. |
| Deployments and DaemonSets | Evaluate desired, ready and available state with generation and rollout timing. | Failed or unavailable rollouts are detected. Paused, zero-scale, and converging workloads avoid blanket alarms. Stale observations remain inconclusive. |
| Configuration references | Check required ConfigMap references with authorized evidence and Secret failures from classified events or corroborated status. | Detect established missing names and keys. Optional references and unspecified environment variables do not trigger. Forbidden and uninspected references stay visible. Transport fixtures prove no Secret reads. |
| PVCs | Report loss and pending binding with relevant phase, timing, and StorageClass evidence. | Lost and actionable pending claims are detected. Expected WaitForFirstConsumer is recognized. An unreadable binding mode limits the conclusion. |
| Services and backends | Correlate selectors, EndpointSlices, endpoint conditions, and Service/backend ports. | Detect selector mismatch, unavailable endpoints and port inconsistencies. Aggregate slices. Account for headless, selectorless, ExternalName, terminating endpoints and unknown readiness. Fallback requires confirmed API absence. |
| Ingress | Verify Service backends and declared Service ports for the default backend and HTTP rules. | Missing Services and invalid port references are detected with complete Service inventory. External resource backends and forbidden Service visibility remain qualified. |
| DNS | Inspect conventional DNS component readiness and backend evidence. | A diagnosed unhealthy component differs from missing visibility or unidentified providers. Healthy components provide only control-plane evidence. |
| NetworkPolicies | Identify selected workloads and plausible ingress or egress isolation risks using additive static rules. | Default-deny candidates and allowances are distinguished. Missing policy coverage remains inconclusive. Findings describe plausible risks and do not claim packet delivery or denial. |
| PDBs | Describe observed maintenance constraints and availability of allowed disruptions. | Zero allowed disruptions is an advisory for eviction planning. Positive allowance, stale status, and absent matching workloads have distinct outcomes. |
| Resource requests and limits | Report configuration advisories and optional timestamped current metrics in explicit units. | Missing settings and observed samples are explained. Missing or stale metrics remain visible. A current sample produces no historical percentile or sizing recommendation. |
| Recent events | Correlate relevant warnings to scan identities and retain bounded classified evidence. | Recent involved-UID warnings are surfaced or folded into a specific rule. Stale, unrelated, and duplicate warnings are excluded. Raw credentials and unexpected messages cannot reach output. |

## System acceptance matrix

| Requirement | Implementation area | Verification |
| --- | --- | --- |
| Namespace and context isolation | CLI, collector, RBAC | Kubeconfig selection and current-context preservation, namespace request paths, only required dependencies outside scope, explicit cluster invocation. |
| Bounded collection and truthful coverage | Collector, model, engine | Forbidden, unavailable, pagination, timeout, cancellation, concurrency or request budget, incomplete precedence, and partial-positive fixture cases. |
| Read-only and data minimization | Transport, collector, model, MCP | Capture real HTTP methods and resource paths and reject writes and command-related subresources. Seed sentinel credentials and hostile strings, then verify output exclusion. |
| Stable user and automation contracts | CLI, model, engine, rule registry | Help, scope, JSON shape and ordering, terminal representation, check catalog, severity filtering and exit-code regression tests. |
| Plugin installation | Command binary, installation docs | Build and install onto a temporary PATH. Run `kubectl plugin list` and `kubectl doctor`. |
| Representative live scans | Disposable integration suite | Assert report evidence and coverage for healthy and broken Kubernetes workloads. Record actual Kubernetes versions and runtime platform for passed runs. |
| Local MCP integration | MCP, engine | Stdio handshake from another client process, tool discovery and invocation, allowed and denied inputs, cluster opt-in, timeout/cancellation, and bounded output. |
| Maintainability and release readiness | CI and release tooling | Formatting, vet/static checks, race tests, dependency vulnerability scan, platform builds, and a representative reproducible benchmark. |
| Delivery and merge controls | Hosting service, docs | Inspect CI and terminal external review for the exact head. Merge only with satisfied protections and gates. Verify merged default branch and draft release. |

## Scope and assumptions

KubeDoctor reads observations made available to the selected Kubernetes identity. A complete scan means its required collection coverage is complete. Optional or permission-limited checks retain their own status. Exact coverage and required API permissions are documented with the CLI behavior and RBAC. A report is a time-bounded diagnostic snapshot. Use next steps and confidence to interpret operational impact.

HTML reporting, storage of historical metrics, historical sizing policy, Grafana integration, AI explanations, deeper network tracing, HTTP-hosted MCP, and additional resource types are later work. See the [roadmap](ROADMAP.md).

## Rule contracts and fixtures

| Rule IDs | Implementation and fixtures | Limits |
| --- | --- | --- |
| `node-readiness`, `node-pressure`, `pod-containers`, `pod-scheduling`, `deployment-rollout`, `daemonset-rollout`, `recent-warning-events` | [Workload rules](../internal/analyze/workloads.go) and [workload fixtures](../internal/analyze/workloads_test.go) | Check structured conditions and known reason codes. Pod status takes precedence over speculation. Selected recent events are correlated by UID. The warning window is one hour and historical OOM evidence expires after 30 minutes. Fresh controller generation and applicable rollout timing are required for definitive rollout conclusions. Repeated cumulative restarts are an advisory. |
| `configuration-references`, `pvc-binding` | [Configuration and storage rules](../internal/analyze/configuration.go) and [workload fixtures](../internal/analyze/workloads_test.go) | ConfigMap text keys and binary keys are distinguished. Secret issues require support from classified events. Missing Secret evidence and an unknown default StorageClass remain visible. Valid consumer-delayed claims and short-lived binding waits are accounted for. |
| `container-resources`, `current-pod-usage` | [Resource rules](../internal/analyze/sizing.go) and [workload fixtures](../internal/analyze/workloads_test.go) | The five-minute freshness gate applies to observed metric samples with measurement timestamps and windows. CPU is reported in cores and memory in bytes. No historical calculation is made. |
| `service.backends`, `service.ports` | [Service rules](../internal/analyze/network.go) and [Service fixtures](../internal/analyze/network_test.go) | Endpoint conditions and port evidence have a 60-second reconciliation grace. Correlated zero-scale Deployments or DaemonSets and completed workloads can explain empty backends. Selectors that match no active Pods with unverified intent, including an uninspected StatefulSet, receive an informational finding. Port checks are inconclusive when expected mappings cannot be inspected and skipped when observed workload intent explains their absence. |
| `ingress.backends`, `dns.infrastructure`, `networkpolicy.isolation`, `pdb.disruptions` | [Network and disruption rules](../internal/analyze/network_rules.go) and [fixtures](../internal/analyze/network_rules_test.go) | DNS covers conventional `kube-system/kube-dns` resources. NetworkPolicy evaluation is static and additive. PDB findings describe voluntary disruption headroom. Custom or externally managed data-plane behavior remains outside observed evidence. |
