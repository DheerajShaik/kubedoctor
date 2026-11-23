# Investigation and comparison contracts

An investigation combines the preserved diagnostic report v1, capture provenance and times, an evidence graph, incidents, and a timeline. Investigation JSON and comparison JSON each start at schema `1`. The analyzer version changes independently of both schemas and the executable version. JSON, terminal, HTML, and stdio MCP render or return this same model.

## Relationships and incidents

Edges point from the declaring or referring resource to its target. Each has a source, a provenance basis, the field or structured observation supporting the association, and `target_observed`. For example, an EndpointSlice can declare an owning Service UID or imply a Service through its name label. Its Pod target reference is a separate edge with published endpoint conditions. Name-based Ingress, configuration, claim, node, and autoscaling references remain visible when their targets are absent or uninspected. Secret target existence stays uninspected. Service and PDB selector matches are inferred within the namespace. [Kubernetes object names and IDs](https://kubernetes.io/docs/concepts/overview/working-with-objects/names/)

Pod, ReplicaSet, and other supported workload findings share an incident only through an observed controller chain with consistent kinds, names, namespaces, and UIDs. A same-name replacement cannot satisfy an earlier UID reference. Incidents cite `finding_indexes` in `report.findings`, and link indexes in the relationship and timeline lists. They retain individual observations, corroboration, candidate explanations, alternatives, missing evidence, next steps and suggested recovery checks. Associations provide guidance for investigation. Causal hypotheses and network reachability require independent verification. [Kubernetes owners and dependents](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/) · [EndpointSlice conditions](https://kubernetes.io/docs/concepts/services-networking/endpoint-slices/)

Timeline rows distinguish reported occurrences and transitions from observations and collection markers. Event rows use an approved timestamp basis when available. A series row records its latest observed time and aggregate count. It cannot reconstruct individual occurrences. Where available, the resource UID and creation time constrain event correlation. An older-style timestamp with unknown basis or an unanchored occurrence remains uncertain. The recent-event window is one hour at the stored evaluation time. Provider times more than five minutes ahead of the evaluation clock are excluded with a visible limit. A later historical provider request can finish after the Kubernetes evaluation and extends the capture interval without advancing evaluation time. [Kubernetes Event API](https://kubernetes.io/docs/reference/kubernetes-api/events/event-v1/)

The graph retains at most 16,000 relationships and evaluates at most 200,000 selector/Pod pairs. The timeline keeps capture markers and up to 2,000 total entries. Incident navigation has a 64,000-reference budget. The report retains underlying findings and evidence when graph or timeline work reaches a cap. The investigation reports truncation and comparison refuses to rely on incomplete associations for recovery.

## Comparing observations

Compare two independently verified bundles captured in chronological order with nonoverlapping intervals. Both must have the same scope, compatible investigation and analyzer versions, and matching strong provenance. Namespace capture binds the namespace UID to the scoped digest. Cluster capture uses the `kube-system` namespace UID digest. A recreated namespace and a mismatched provenance method are incompatible. A renamed kubeconfig context can still compare when provenance matches. Bundle integrity does not authenticate provenance supplied by a third party.

The resource view keys each finding by rule, resource kind, scope, name, UID, and a rule-specific condition discriminator. The workload view uses the highest supported anchor reached through verified controller owners and keeps the constituent resource occurrences. A replacement Pod can produce a new object occurrence while the same linked workload condition persists. The workload gate uses the highest severity in each set of occurrences. A greater count, changed evidence, or changed confidence remains visible when severity stays the same.

| Classification | Evidence |
| --- | --- |
| New | The later finding is observed with comparable baseline check and source visibility. |
| Persistent | The same condition is witnessed in both captures. A different set of replica UIDs can keep a workload condition persistent. |
| Changed | The condition remains present with different severity, confidence, symptom, evidence, or a different number of occurrences. |
| Resolved | Both relevant checks and sources were inspected within their capture intervals. The same observed UID has fresh current status. A Pod-level symptom also requires active Ready Pods, and a workload comparison requires sufficient linked Ready replicas for the observed desired count. |
| No longer observable | The previous observation could not be verified in the later capture. Examples include UID replacement, stale status, an aging event, and lost permissions. |
| Not comparable | Identity, analysis version, capture order, scope or observation coverage cannot establish a valid comparison. |

A health transition requires positive continuity evidence as well as the disappearance of a finding. A controller scaled to zero cannot establish restored availability. Service backend recovery requires equivalent backend and publication semantics, a ready nonterminating EndpointSlice address, and continuity of any affected declared port. A historical termination, restart count or metric sample can disappear without establishing a repair. Distinct Job runs are retained. Success from a later run does not repair a previous object occurrence. Namespace-level findings with unavailable resource UIDs remain incomparable. Per-source coverage changes stay visible.

If either capture contains configured historical evidence, comparison reports provider status and comparable, missing, or added history targets. The same Pod UID, workload UID, and container name need adequate CPU and memory coverage on both sides. History lost between captures, unavailable providers, and replacement targets make the requested comparison incomplete. Appropriately covered windows still describe separate observations and support advisory sizing. Severity regressions use Kubernetes diagnostics. Core-only comparisons continue to work when neither capture requested history.

## Regression gate

Use `--fail-on info`, `warning`, `critical` or `none`. The default is `warning`. A qualifying regression is a new workload finding or a persistent condition whose severity worsened to at least the threshold. Evidence, confidence, and replica-only changes remain visible and do not trigger a severity regression. The gate returns `2` for incompatible captures, incomplete required coverage, truncated analysis, or a material workload change with unverified status. An expired historical observation with otherwise comparable coverage does not itself fail the gate. The gate returns `1` for a qualifying regression and otherwise returns `0`. `none` disables the findings threshold and preserves incomplete exit `2`. Legacy scan exits retain their original meaning.

Use two timed observations to gate an already authorized deployment pipeline. These commands read an existing scoped workload and create local evidence. The example assumes a trusted kubeconfig and a workspace with fresh file paths.

```sh
set -eu
kubectl doctor capture namespace demo --bundle baseline.kdb -o json > baseline.json
# Run a separately authorized change in your normal deployment job.
kubectl doctor capture namespace demo --bundle subsequent.kdb -o json > subsequent.json
kubectl doctor compare baseline.kdb subsequent.kdb -o json --fail-on warning
```

Use `kubectl doctor compare baseline.kdb subsequent.kdb -o html --file comparison.html` for the same comparison offline. A passing gate covers the observed diagnostics within its documented scope and time interval. See [the disposable incident lab](LAB.md) for a reproducible before-and-after example.

## Verification

`go test ./internal/investigation` exercises controller and endpoint identities, incident grouping, references, timeline budgets and redaction, exact scoped provenance, compatible history timing, source loss, stale named evidence, rules and scope changes, positive recovery, separate replica occurrences, and gate priority.

The synthetic benchmarks use 500 Pods, 50 Services, roughly 500 classified events, and 103 findings with a shared Deployment owner chain. Run `go test ./internal/investigation -run '^$' -bench BenchmarkInvestigation -benchmem -benchtime=300ms -count=1`. In one Codex Cloud Linux amd64 run with an AMD EPYC 9V74 and `GOMAXPROCS=4`, investigation construction measured 5.27 ms/op and 3.16 MB/op. Comparison of two already-built investigations measured 2.11 ms/op and 2.14 MB/op. The test exercises deterministic CPU and memory work from synthetic inputs.
