# Prometheus history and sizing

History is an opt-in source for incident capture. Set `--history-config` to a local JSON file at the capture command or configure it once at MCP startup. Legacy Kubernetes scans keep their existing required coverage and exit classification. A capture preserves its Kubernetes report if Prometheus times out, refuses access, lacks matching metrics, or returns incomplete evidence. The historical result exposes that state independently. A historical request is complete when all selected CPU and memory series meet the evidence policy. Advice may still be withheld because of a Kubernetes or sizing constraint.

```sh
kubectl doctor capture namespace payments --history-config history.json --bundle payments.kdb --output json
kubectl doctor replay payments.kdb --output html --file payments.html
```

An explicit capture with incomplete history saves the available evidence and exits 2. A malformed startup configuration returns a configuration error before collection.

## Configuration

```json
{
  "url": "https://prometheus.example.net",
  "bearer_token_env": "KUBEDOCTOR_PROMETHEUS_TOKEN",
  "ca_file": "prometheus-trust.pem",
  "cluster": "staging-eu1",
  "cpu_counter_metric": "container_cpu_usage_seconds_total",
  "memory_working_set_metric": "container_memory_working_set_bytes",
  "window": "6h",
  "step": "1m",
  "scrape_interval": "1m",
  "timeout": "20s",
  "targets": [
    {
      "namespace": "payments",
      "kind": "Deployment",
      "name": "checkout",
      "container": "app",
      "vpa_disabled": true
    }
  ],
  "grafana": {
    "origin": "https://grafana.example.net",
    "uid": "container-usage",
    "slug": "workload-resource-usage"
  }
}
```

Bearer token and CA file are optional. The token is read from the named environment variable at startup. A relative CA file is resolved from the configuration's directory. The service must be reachable directly. HTTPS validates both the trust chain and hostname. Credential-free HTTP is allowed for a literal loopback IP, which supports the test fixture. Keep credential values in the environment supplied to the operator process. The endpoint permits a trusted path prefix. Authentication remains bound to its configured origin and requests stop at redirects.

Allowlist exact namespace, resource kind, resource name, and application container. Choices are Pod, Deployment, ReplicaSet, DaemonSet, StatefulSet, and Job. Deployment matching follows the observed current Pod and ReplicaSet controller UIDs. A namespace capture intersects the requested namespace with the allowlist. A cluster capture still uses the explicit allowlist. A policy can match several current replicas, each of which gets a separate series. Empty, unobservable, overlapping, or over-budget targets produce a coverage limitation.

## Metric contract

This adapter was designed for the Prometheus HTTP range-query API. CPU must be a scalar cumulative counter in CPU seconds. Memory must be a scalar working-set gauge in bytes. Both series need exact labels `cluster`, `namespace`, `pod`, `pod_uid`, `container`, and `container_id`. The last label is the observed runtime identity, such as `containerd://` followed by 64 lowercase hexadecimal digits. Generic cAdvisor installations often lack one or more labels. Configure the telemetry pipeline to attach the identity labels at collection or select already-labeled metrics. The configured metrics must preserve actual scrape timestamps, including when reused in another monitoring system. Metric names may be overridden with Prometheus identifiers. The source discovers availability by querying exact observed identities.

The queries never accept PromQL from a diagnostic caller. Fixed templates ask for the value and `timestamp()` of the matching series in the same range request. The engine pairs the two by evaluation time, retains only genuine distinct scrape timestamps within the window, and discards stale lookback points. Provider labels outside the contract, response warnings, mismatched samples and duplicate series lead to visible reduced coverage. The data supports one Pod UID and one container lifetime at a time. It produces no population percentile across replicas or revisions. The two metric reads have separate evaluation moments within the recorded source interval. [Prometheus HTTP API](https://github.com/prometheus/prometheus/blob/v3.16.0/docs/querying/api.md) · [PromQL functions](https://github.com/prometheus/prometheus/blob/v3.16.0/docs/querying/functions.md#timestamp)

Window ends at the captured Kubernetes evaluation time, rounded down to a second. Valid windows run from 15 minutes through seven days. The step is at least five seconds, divides the window, and is no greater than the declared scrape interval. Configuration permits at most 2,048 evaluations and requires at least 25 expected scrapes. The default is six hours at a one-minute step and scrape interval. The snapshot preserves the requested sample window plus actual provider request start and finish. The collector uses at most eight observed targets, two requests, 17 returned series per request, and 2 MiB per response. The query deadline is five seconds, the HTTP request deadline is six seconds, and the capture timeout defaults to 20 seconds with a maximum of 30 seconds.

The evidence bundle stores these times and bounds, normalized CPU counter and memory working-set points, target identities, and status codes. Grafana origins and dashboards are retained only when configured. The provider endpoint, token, CA location, runtime container ID, PromQL, arbitrary labels, warnings, and raw API responses remain in the live collection boundary. Offline replay recalculates the same statistics from the normalized captured points.

## Advice and limits

The series must have at least 25 independent scrapes, at least 90% of the expected count, and at least 90% of the requested span. It must have fresh observations near both ends and adjacent scrapes spaced between half and twice the declared interval. Gaps remain missing. CPU rates come from differences between adjacent valid counter values over the elapsed scrape interval and are reported in millicores. A detected counter reset suppresses CPU advice. Memory statistics use full working-set samples in bytes. Percentiles use nearest rank over observations within this single container lifetime. Statistics for incomplete series remain visible with their status and available chart points.

- The CPU request candidate is 125% of observed P95, rounded up to 10 millicores, with a 10 millicore floor. An existing CPU limit may censor demand. A candidate that reduces or introduces a request while a CPU limit is present is withheld.
- The memory request candidate is the greater of 130% of observed P95 and 110% of observed peak, rounded up to a MiB. A working-set peak at or above 80% of an existing memory limit suppresses advice.
- Both candidates are withheld when they exceed a configured limit, when the observed container lifetime does not cover the window, or when recent or unplaced termination evidence prevents a defensible recommendation.
- Unknown HPA inventory and truncated metric observations on a matching HPA suppress candidates. A matching utilization-based HPA suppresses the candidate for that resource because the request is the denominator. A container-specific target affects only the named container. Pod-level requests or limits suppress the candidate for that resource because the policy does not model the shared budget. Advice requires `vpa_disabled: true` as an explicit operator assertion that VPA does not manage the workload.

These conservative formulas provide advisory requests for the observed container and window. Memory limits, placement, scheduling headroom, performance objectives, throttling, and future load require separate evidence. The formulas do not estimate savings. [Kubernetes HPA algorithm](https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/#algorithm-details) · [Kubernetes VPA](https://kubernetes.io/docs/concepts/workloads/autoscaling/vertical-pod-autoscale/) · [Container resource management](https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/)

## Grafana navigation

Configure a bare HTTPS origin, dashboard UID, and slug. The link carries absolute millisecond time bounds and the variables `namespace`, `workload`, `pod`, and `container`. Configure the dashboard variables to match. HTML renders the link as visit-on-click navigation and keeps charts and evidence self-contained. [Grafana URL variables](https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/create-dashboard-url-variables/)

## Verification

`go test ./internal/history` exercises deterministic sizing, gaps, restarts, permission and truncation errors, TLS verification, cancellation, redirect containment, and hostile inputs. `./scripts/history-integration.sh` verifies and starts pinned upstream Prometheus v3.16.0 and seeds an isolated temporary TSDB with synthetic fixture observations. It checks the actual range-query adapter, including deduplication when evaluation occurs twice as often as simulated scrapes, suppression against sparse and ambiguously duplicated series, and omission of unrelated response labels. The lab measures adapter behavior with seeded data.

The linked provider and autoscaling contracts were checked on 23 November 2026.
