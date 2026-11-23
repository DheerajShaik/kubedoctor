#!/usr/bin/env bash
# Sourced by integration.sh after it defines the isolated kubeconfig and guard.

fixture_probe_image='docker.io/library/busybox:1.37.0@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec'

wait_service_reconciliation() {
  local namespace=$1 name=$2 service_age
  service_age=$(kube -n "$namespace" get service "$name" -o json | jq 'now - (.metadata.creationTimestamp | fromdateiso8601) | floor')
  if ((service_age < 65)); then
    sleep "$((65 - service_age))"
  fi
}

run_deep_diagnostics() {
  local status namespace
  sed "s|__COREDNS_IMAGE__|$image_ready|g" test/integration/expansion.yaml | mutate apply -f -
  kube -n doctor-rules wait --for=condition=Failed job/fixture-failed --timeout=120s
  kube -n doctor-rules wait --for=create --for=jsonpath='{.status.containerStatuses[0].state.waiting.reason}'=ImagePullBackOff pod/stateful-image-0 --timeout=120s
  kube -n doctor-rules wait --for=condition=ScalingActive=false hpa/stateful-image --timeout=120s
  kube -n doctor-rules wait --for=jsonpath='{.status.conditions[?(@.type=="Progressing")].reason}'=ProgressDeadlineExceeded deployment/probe-trip --timeout=120s
  kube -n doctor-quota wait --for=condition=ReplicaFailure deployment/quota-blocked --timeout=120s
  kube -n doctor-limits wait --for=condition=ReplicaFailure deployment/limits-blocked --timeout=120s
  kube -n doctor-rules wait --for=create event --field-selector involvedObject.name=missing-storage,reason=ProvisioningFailed --timeout=120s

  status=0
  kubectl doctor namespace doctor-rules --context "$context" --timeout 45s -o json >"$test_dir/expansion.json" || status=$?
  if [[ "$status" != 1 ]]; then
    printf 'Expanded fixture should reach its diagnostic failure threshold. Exit %s.\n' "$status" >&2
    exit 1
  fi
  assert_report "$test_dir/expansion.json" '
    .complete and
    any(.findings[]; .rule_id == "statefulset-rollout" and .resource.name == "stateful-image") and
    any(.findings[]; .rule_id == "job-execution" and .resource.name == "fixture-failed") and
    any(.findings[]; .rule_id == "cronjob-scheduling" and .resource.name == "fixture-suspended" and .severity == "info") and
    any(.findings[]; .rule_id == "hpa-scaling" and .resource.name == "stateful-image") and
    any(.findings[]; .rule_id == "rollout-probes" and (.resource.name | startswith("probe-trip-"))) and
    any(.findings[]; .rule_id == "pvc-binding" and .resource.name == "missing-storage") and
    any(.findings[]; .rule_id == "networkpolicy-named-ports")'
  for namespace in doctor-quota doctor-limits; do
    kubectl doctor namespace "$namespace" --context "$context" --timeout 45s -o json --fail-on none >"$test_dir/$namespace.json"
    assert_report "$test_dir/$namespace.json" '
      .complete and any(.findings[]; .rule_id == "namespace-admission")'
  done
}

create_probe_job() {
  local name=$1 role=$2 command=$3 arg=$4
  kube -n doctor-healthy create job "$name" --image="$fixture_probe_image" --dry-run=client -o json \
    -- /bin/sh -ec "$command" fixture "$arg" |
    jq --arg role "$role" '.spec.activeDeadlineSeconds = 60 | .spec.backoffLimit = 0 |
      .spec.template.metadata.labels["kubedoctor.test/probe-role"] = $role' |
    mutate -n doctor-healthy apply -f -
  kube -n doctor-healthy wait --for=condition=Complete "job/$name" --timeout=90s
}

run_nodelocal_fixture() {
  local manifest="$test_dir/nodelocaldns.yaml" service_ip
  curl --fail --silent --show-error --location --retry 2 --max-time 60 -o "$manifest" \
    https://raw.githubusercontent.com/kubernetes/kubernetes/v1.36.4/cluster/addons/dns/nodelocaldns/nodelocaldns.yaml
  printf '%s  %s\n' 5b60715d707954e0346ea2641ef4eda5bfb810672f473acce46b624508b546d5 "$manifest" | sha256sum --check
  service_ip=$(kube -n kube-system get service kube-dns -o jsonpath='{.spec.clusterIP}')
  [[ "$service_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]
  sed -e 's/__PILLAR__LOCAL__DNS__/169.254.20.10/g' \
    -e 's/__PILLAR__DNS__DOMAIN__/cluster.local/g' \
    -e "s/__PILLAR__DNS__SERVER__/$service_ip/g" \
    -e 's|registry.k8s.io/dns/k8s-dns-node-cache:1.26.7|registry.k8s.io/dns/k8s-dns-node-cache:1.26.7@sha256:8b9a78d101a1845e08521b33856b1bd26021233c6a1fad994f60c967a519d201|g' \
    "$manifest" | mutate apply -f -
  kube -n kube-system rollout status daemonset/node-local-dns --timeout=180s
  create_probe_job nodelocal-query dns '
    for i in 1 2 3 4 5 6 7 8; do
      nslookup kubernetes.default.svc.cluster.local "$1" >/dev/null 2>&1 && exit 0
      sleep 2
    done
    exit 1
  ' '169.254.20.10'
  kubectl doctor namespace doctor-healthy --context "$context" --timeout 45s -o json >"$test_dir/nodelocal.json"
  assert_report "$test_dir/nodelocal.json" '
    .complete and any(.checks[]; .id == "node-local-dns" and .status == "checked") and
    all(.findings[]; .rule_id != "node-local-dns" or .severity == "info")'
  printf 'Verified NodeLocal DNSCache rollout and fixture lookup. Diagnostic observations remain read-only.\n'
}

run_policy_fixture() {
  local url service_ip reachable blocked
  service_ip=$(kube -n doctor-healthy get service ready-app -o jsonpath='{.spec.clusterIP}')
  [[ "$service_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]
  url="http://$service_ip/health"
  reachable='
    for i in 1 2 3 4 5 6 7 8; do
      wget -q -T 2 -O /dev/null "$1" && exit 0
      sleep 1
    done
    exit 1
  '
  blocked='
    for i in 1 2 3 4 5 6 7 8; do
      wget -q -T 2 -O /dev/null "$1" || exit 0
      sleep 1
    done
    exit 1
  '
  create_probe_job policy-baseline blocked "$reachable" "$url"
  mutate apply -f - <<'YAML'
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: fixture-allow-probe
  namespace: doctor-healthy
spec:
  podSelector:
    matchLabels: {app: ready-app}
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector:
            matchLabels: {kubedoctor.test/probe-role: allowed}
YAML
  create_probe_job policy-denied blocked "$blocked" "$url"
  create_probe_job policy-allowed allowed "$reachable" "$url"
  kubectl doctor namespace doctor-healthy --context "$context" --timeout 45s -o json >"$test_dir/policy.json"
  assert_report "$test_dir/policy.json" '
    .complete and any(.checks[]; .id == "networkpolicy.isolation" and .status == "checked")'
  printf 'Verified Cilium denied and allowed fixture traffic with positive controls. The diagnostic used static evidence.\n'
}

run_offline_replay() {
  local evidence_dir=$1
  # Replay gets a copy of only the baseline bundle and two challenge configs.
  # The first intentionally cannot parse. The second points to no usable API and
  # has an inert marker helper to expose anyone trying live authentication.
  mkdir -p "$test_dir/offline/input" "$test_dir/offline/proof"
  install -m 0600 "$evidence_dir/before.bundle.json" "$test_dir/offline/input/before.bundle.json"
  printf '%s\n' 'broken: [kubeconfig' >"$test_dir/offline/input/malformed"
  cat >"$test_dir/offline/input/helper" <<'EOF_CONFIG'
apiVersion: v1
kind: Config
current-context: unavailable
clusters:
  - name: unavailable
    cluster:
      server: https://127.0.0.1:9
contexts:
  - name: unavailable
    context:
      cluster: unavailable
      user: marker
users:
  - name: marker
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1
        interactiveMode: Never
        command: /bin/sh
        args: ['-c', 'printf invoked > /proof/helper-ran']
EOF_CONFIG
  local config
  for config in malformed helper; do
    docker run --rm --pull=never --network none --read-only --cap-drop ALL \
      --security-opt no-new-privileges --user "$(id -u):$(id -g)" \
      --mount "type=bind,src=$test_dir/bin/kubectl-doctor,dst=/doctor,readonly" \
      --mount "type=bind,src=$test_dir/offline/input,dst=/input,readonly" \
      --mount "type=bind,src=$test_dir/offline/proof,dst=/proof" \
      -e "KUBECONFIG=/input/$config" --entrypoint /doctor "$image" \
      replay /input/before.bundle.json -o json >"$test_dir/offline-$config.json"
    diff -u <(jq -S . "$evidence_dir/before.json") <(jq -S . "$test_dir/offline-$config.json")
  done
  if [[ -e "$test_dir/offline/proof/helper-ran" ]]; then
    printf 'Offline replay unexpectedly executed the credential helper fixture.\n' >&2
    exit 1
  fi
}

run_restricted_comparison() {
  local evidence_dir=$1 limited="$test_dir/kubeconfig-restricted" status=0
  mutate apply -f test/integration/restricted.yaml
  kube config view --raw --flatten --minify -o json |
    jq '.users[0].user.as = "system:serviceaccount:doctor-incident:incomplete-reader"' >"$limited"
  chmod 0600 "$limited"
  kubectl doctor capture namespace doctor-incident --context "$context" --kubeconfig "$limited" --timeout 45s \
    --bundle "$test_dir/restricted.bundle.json" -o json >"$test_dir/restricted.json" || status=$?
  if [[ "$status" != 2 ]]; then
    printf 'Expected a captured partial report with exit 2. Got %s.\n' "$status" >&2
    exit 1
  fi
  assert_report "$test_dir/restricted.json" '
    (.report.complete | not) and any(.report.sources[]; .source == "configmaps" and .status == "permission_denied") and
    any(.report.sources[]; .source == "events" and .status == "permission_denied")'
  status=0
  kubectl doctor compare "$evidence_dir/before.bundle.json" "$test_dir/restricted.bundle.json" -o json >"$test_dir/restricted-comparison.json" || status=$?
  [[ "$status" == 2 ]]
  assert_report "$test_dir/restricted-comparison.json" '
    .compatible and (.complete | not) and
    any(.coverage_changes[]; .source == "configmaps" and .after == "permission_denied") and
    any(.resource_changes[]; .rule_id == "configuration-references" and .classification == "no_longer_observable") and
    all(.resource_changes[]; .rule_id != "configuration-references" or .classification != "resolved")'

  kubectl doctor capture namespace doctor-healthy --context "$context" --timeout 45s \
    --bundle "$test_dir/scope-change.bundle.json" >/dev/null
  status=0
  kubectl doctor compare "$evidence_dir/before.bundle.json" "$test_dir/scope-change.bundle.json" -o json >"$test_dir/scope-change.json" || status=$?
  [[ "$status" == 2 ]]
  assert_report "$test_dir/scope-change.json" '
    (.compatible | not) and all(.resource_changes[]; .classification != "resolved")'
}

run_connected_incident() {
  local evidence_dir="$test_dir/evidence" deployment_uid pod_uid
  mkdir "$evidence_dir"
  sed "s|__COREDNS_IMAGE__|$image_ready|g" test/integration/incident.yaml | mutate apply -f -
  kube -n doctor-incident wait --for=create --for=jsonpath='{.status.containerStatuses[0].state.waiting.reason}'=CreateContainerConfigError pod \
    -l app=checkout --timeout=120s
  kube -n doctor-incident wait --for=jsonpath='{.status.conditions[?(@.type=="Progressing")].reason}'=ProgressDeadlineExceeded deployment/checkout --timeout=120s
  wait_service_reconciliation doctor-incident checkout
  deployment_uid=$(kube -n doctor-incident get deployment checkout -o jsonpath='{.metadata.uid}')
  pod_uid=$(kube -n doctor-incident get pod -l app=checkout -o json | jq -er '.items | if length == 1 then .[0].metadata.uid else error("expected one Pod") end')
  kubectl doctor capture namespace doctor-incident --context "$context" --timeout 45s \
    --bundle "$evidence_dir/before.bundle.json" -o json >"$evidence_dir/before.json"
  kubectl doctor inspect "$evidence_dir/before.bundle.json" >"$test_dir/bundle-inventory.json"
  kubectl doctor investigate "$evidence_dir/before.bundle.json" -o text >"$test_dir/incident.txt"
  kubectl doctor replay "$evidence_dir/before.bundle.json" -o html --file "$evidence_dir/incident.html"
  assert_report "$evidence_dir/before.json" '
    .schema_version == "1" and .report.schema_version == "1" and .report.complete and
    (.cluster_id | length > 0) and (.source_timings | length > 0) and
    any(.report.findings[]; .rule_id == "configuration-references" and
      any(.evidence[]; .field == "reference_name" and .value == "checkout-dependency")) and
    any(.report.findings[]; .rule_id == "deployment-rollout" and .resource.name == "checkout") and
    any(.report.findings[]; .rule_id == "service.backends" and .resource.name == "checkout") and
    any(.relationships[]; .kind == "pod_configuration" and .to.name == "checkout-dependency" and (.target_observed | not)) and
    (.report.findings as $findings |
      any(.incidents[]; .anchor.kind == "Deployment" and .anchor.name == "checkout" and
        ([.finding_indexes[] | $findings[.].rule_id] |
          index("configuration-references") != null and index("deployment-rollout") != null)))'
  grep -Fq 'checkout-dependency' "$test_dir/incident.txt"
  grep -Fq 'checkout-dependency' "$evidence_dir/incident.html"
  grep -Fq 'Collection coverage' "$evidence_dir/incident.html"
  run_offline_replay "$evidence_dir"

  mutate -n doctor-incident create configmap checkout-dependency \
    --from-literal=token=KUBEDOCTOR_INTEGRATION_CREDENTIAL_SENTINEL
  kube -n doctor-incident rollout status deployment/checkout --timeout=180s
  kube -n doctor-incident wait --for=jsonpath='{.endpoints[0].conditions.ready}'=true endpointslice \
    -l kubernetes.io/service-name=checkout --timeout=120s
  [[ "$(kube -n doctor-incident get deployment checkout -o jsonpath='{.metadata.uid}')" == "$deployment_uid" ]]
  [[ "$(kube -n doctor-incident get pod -l app=checkout -o json | jq -er '.items | if length == 1 then .[0].metadata.uid else error("expected one Pod") end')" == "$pod_uid" ]]
  kubectl doctor capture namespace doctor-incident --context "$context" --timeout 45s \
    --bundle "$evidence_dir/after.bundle.json" -o json >"$evidence_dir/after.json"
  kubectl doctor compare "$evidence_dir/before.bundle.json" "$evidence_dir/after.bundle.json" \
    -o json --file "$evidence_dir/comparison.json"
  kubectl doctor compare "$evidence_dir/before.bundle.json" "$evidence_dir/after.bundle.json" \
    -o html --file "$evidence_dir/comparison.html"
  assert_report "$evidence_dir/comparison.json" '
    .compatible and .complete and
    any(.resource_changes[]; .rule_id == "configuration-references" and .classification == "resolved") and
    any(.workload_changes[]; .subject.kind == "Deployment" and .subject.name == "checkout" and
      .rule_id == "deployment-rollout" and .classification == "resolved")'
  run_restricted_comparison "$evidence_dir"
  go run ./test/integration/mcp "$test_dir/bin/kubectl-doctor" "$KUBECONFIG" "$context" "$evidence_dir"
  if grep -Fq 'KUBEDOCTOR_INTEGRATION_CREDENTIAL_SENTINEL' "$evidence_dir/"* "$test_dir/incident.txt" "$test_dir/restricted.json"; then
    printf 'The connected lab exported a seeded literal secret.\n' >&2
    exit 1
  fi
  if [[ -n "${KUBEDOCTOR_EXAMPLE_DIR:-}" ]]; then
    mkdir -p "$KUBEDOCTOR_EXAMPLE_DIR"
    install -m 0600 "$evidence_dir/"{before.bundle.json,after.bundle.json,incident.html,comparison.html,comparison.json} \
      "$KUBEDOCTOR_EXAMPLE_DIR/"
  fi
  printf 'Connected investigation passed live capture, offline replay, HTML, verified repair, comparison and MCP.\n'
}
