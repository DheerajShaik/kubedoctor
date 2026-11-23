# Incident bundles v1

An incident bundle keeps the reviewed observations needed to repeat KubeDoctor's analysis. It also includes the original versioned diagnostic report for viewing. Bundle schema 1, report JSON v1, the analyzer version and the producing binary version are independent identifiers. The checked-in [release charter](RELEASE_CHARTER.md) describes the compatibility and gate requirements.

```sh
kubectl doctor capture namespace payments --bundle payments-before.json -o json
kubectl doctor inspect payments-before.json
kubectl doctor replay payments-before.json -o json
kubectl doctor replay payments-before.json --mode stored -o json
kubectl doctor replay payments-before.json --mode reevaluate -o json
```

- **Capture** uses the selected Kubernetes credentials. `inspect` and all replay modes open only the supplied local evidence file. They require no kubeconfig, credential helper, cluster, Prometheus, or network access.
- **Replay** evaluates the captured observations at the original evaluation time with the captured analyzer version. The installed analyzer must have that version, and the newly evaluated v1 report must match the stored one. A mismatch is an operational error.
- **Stored** shows the captured diagnostic report v1 with its original producer interpretation. It leaves original results readable after rule changes. This mode has no regenerated relationships, timeline or historical calculations.
- **Reevaluate** applies the installed rules to the same observations and evaluation time. Outputs identify both analyzer versions so changed interpretation is distinguishable from cluster changes. The program never downloads or starts another analyzer.

See the [incident lab](LAB.md) for a before/after workflow and the [reproduction steps](REPRODUCE.md) for a synthetic, cluster-free example.

## Format and bounds

One UTF-8 JSON object, uncompressed, contains `schema_version`, `sha256`, and `payload`. SHA-256 covers the exact UTF-8 bytes of the JSON payload value. A tool that rewrites payload whitespace or member order changes those bytes. Inspection verifies the digest, which detects changes without authenticating its author. The owner should share bundles through a trusted channel when provenance matters.

The payload includes the scope and observed context label, capture start and end, fixed evaluation time, optional comparable provenance, producer and analyzer versions, source outcomes and timing intervals, normalized observations and the original v1 report. Timing spans correspond to individual named/discovery reads or a paginated source. Provider observations carry their own time window and request interval. Captures consist of separate API requests and may contain reconciliation changes occurring between reads.

A namespace capture hashes the selected Namespace UID. A cluster capture may hash the conventional `kube-system` Namespace UID. The digest includes the observed namespace name, and a separate field identifies the provenance method. Comparable provenance requires matching scope and method as well as the digest. Denied/unknown provenance stays unknown and prevents strong comparisons. Context labels remain descriptive.

File and parsing budgets are 32 MiB, 24 levels of JSON nesting, 2 million tokens, 50,000 members per JSON array and at most 50,000 total top-level observation rows. Object and string sizes are bounded. The parser rejects mismatched integrity, unknown fields, trailing values, conflicting duplicate keys, conflicting object identities, malformed classifications and out-of-scope observations. V1 has no compression, container entries, external includes or executable fields.

A new bundle is saved with restrictive permissions where supported (0600 on Unix). Publication uses a private temporary file and a same-directory atomic no-overwrite hard link. An existing path, including a symlink, is preserved. A filesystem without hard-link support reports an explicit error. Use a trusted local directory. Local reads accept regular files and refuse a final symbolic link.

## Retained observations and disclosures

| Evidence | Retained |
| --- | --- |
| Identities and relationships | Observed Kubernetes names, kinds, UIDs, relevant ownership, namespace, lifecycle/generation metadata, hosting nodes, storage references, Service/backends, typed Ingress references, and controller/disruption/autoscaling evidence. |
| Workloads | Relevant status, conditions, recognized reasons, timestamps, restart/termination state, port declarations, compute requests and limits, referenced PVCs, configuration object names and referenced keys, and coarse probe classifications. |
| Events | A reviewed classification, involved resource identity, observation time and time basis, count and optional structured reference or admission/probe evidence. |
| Networking | Service port and EndpointSlice conditions and target identities, peer/port rule shape, named policy ports, conventional DNS readiness and the presence of the conventional configuration key. |
| Labels | Keys used in supported matching and only corresponding values. Values use consistent domain-separated fingerprints to preserve selector equality. The conventional DNS selector uses its well-known literal. |
| Configuration | Identities of referenced ConfigMaps, membership of explicitly referenced text and binary keys, and typed references from Pods. |
| Metrics | Timestamped current quantities and optional bounded historical samples, identity-linked workloads, provider coverage, and an explicit credential-free Grafana dashboard origin/UID/slug. |

Excluded data includes Secret objects, logs, raw Kubernetes resources, message bodies, commands, arguments, runtime container IDs, raw IP addresses, unrelated annotations, literal environment/configuration values, and unreferenced ConfigMap identities and keys. Pod DNS supplements are restricted to conventional DNS evidence and namespace-specific node-local dependencies. ConfigMap binary-vs-text membership stays distinct because rules rely on it.

Bundles contain internal topology, resource identities, relevant key names, and a possible configured dashboard identity. Deterministic fingerprints preserve relationships and can reveal equality or guessable labels. Handle a bundle as potentially sensitive. Never publish production evidence in an issue or review.
