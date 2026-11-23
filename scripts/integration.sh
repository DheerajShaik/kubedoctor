#!/usr/bin/env bash
set -euo pipefail

kubernetes_version=${1:-1.36.4}
variant=${2:-default}
case "$kubernetes_version" in
  1.35.8) image=kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0 ;;
  1.36.4) image=kindest/node:v1.36.4@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed ;;
  *)
    printf 'Usage: %s <1.35.8|1.36.4>\n' "$0" >&2
    exit 2
    ;;
esac

case "$kubernetes_version/$variant" in
  1.35.8/default|1.36.4/default|1.36.4/nodelocal|1.36.4/cilium) ;;
  *)
    printf 'Unsupported integration variant %s for Kubernetes %s.\n' "$variant" "$kubernetes_version" >&2
    exit 2
    ;;
esac

for dependency in docker go jq kind kubectl; do
  command -v "$dependency" >/dev/null || { printf 'Missing command: %s\n' "$dependency" >&2; exit 2; }
done

cd "$(dirname "${BASH_SOURCE[0]}")/.."
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/kubedoctor-integration.XXXXXXXX")
cluster="kubedoctor-$(printf '%s' "${test_dir##*.}" | tr '[:upper:]' '[:lower:]')"
context="kind-$cluster"
export KUBECONFIG="$test_dir/kubeconfig"
delete_cluster=false
control_plane_id=
cleanup() {
  local status=$?
  if "$delete_cluster"; then
    local current_id
    current_id=$(docker inspect --format '{{.Id}}' "$cluster-control-plane" 2>/dev/null || true)
    if [[ -n "$control_plane_id" && -n "$current_id" && "$current_id" != "$control_plane_id" ]]; then
      printf 'Disposable node identity changed. Refusing teardown of an unverified cluster.\n' >&2
      status=1
    else
      kind delete cluster --name "$cluster" --kubeconfig "$KUBECONFIG" || status=1
    fi
  fi
  rm -rf -- "$test_dir"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

kube() {
  kubectl --kubeconfig "$KUBECONFIG" --context "$context" --request-timeout=130s "$@"
}

assert_disposable_cluster() {
  local identity endpoint
  identity=$(docker inspect --format '{{.Id}} {{index .Config.Labels "io.x-k8s.kind.cluster"}} {{index .Config.Labels "io.x-k8s.kind.role"}}' "$cluster-control-plane")
  if [[ "$identity" != "$control_plane_id $cluster control-plane" || -z "$control_plane_id" ]]; then
    printf 'The control-plane container no longer matches this disposable run.\n' >&2
    exit 1
  fi
  endpoint=$(docker port "$cluster-control-plane" 6443/tcp)
  if [[ ! "$endpoint" =~ ^127\.0\.0\.1:[0-9]+$ ]] || ! kube config view --minify -o json |
    jq -e --arg ctx "$context" --arg server "https://$endpoint" \
      '.["current-context"] == $ctx and .clusters[0].cluster.server == $server' >/dev/null; then
    printf 'The isolated kubeconfig endpoint does not match the disposable kind node.\n' >&2
    exit 1
  fi
}

mutate() {
  assert_disposable_cluster
  kube "$@"
}

# shellcheck source=scripts/integration-scenarios.sh
source scripts/integration-scenarios.sh

assert_report() {
  local report=$1
  local predicate=$2
  if ! jq -e "$predicate" "$report" >/dev/null; then
    jq -c '
      if has("resource_changes") then
        {compatible, complete, limitations, coverage_changes,
         resource_changes: [.resource_changes[] | {rule_id, subject, classification}]}
      else
        (.report // .) | {scope, complete, collection_errors, checks,
          findings: [(.findings // [])[] | {rule_id, resource, severity, evidence}]}
      end' "$report" >&2
    printf 'Report assertion failed: %s\n' "$predicate" >&2
    exit 1
  fi
}

mkdir "$test_dir/bin"
CGO_ENABLED=0 go build -trimpath -o "$test_dir/bin/kubectl-doctor" ./cmd/kubectl-doctor
kubectl_path=$(command -v kubectl)
export PATH="$test_dir/bin:$PATH"
# Isolate discovery from unrelated kubectl plugins installed on the runner.
PATH="$test_dir/bin" "$kubectl_path" plugin list | grep -Fx "$test_dir/bin/kubectl-doctor"
PATH="$test_dir/bin" "$kubectl_path" doctor version
"$test_dir/bin/kubectl-doctor" checks >/dev/null

existing_clusters=$(kind get clusters)
if grep -Fxq "$cluster" <<<"$existing_clusters"; then
  printf 'The disposable cluster name already exists: %s\n' "$cluster" >&2
  exit 1
fi
delete_cluster=true
if [[ "$variant" == cilium ]]; then
  command -v cilium >/dev/null
  cat >"$test_dir/kind.yaml" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  apiServerAddress: "127.0.0.1"
  disableDefaultCNI: true
EOF
  kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --image "$image" --config "$test_dir/kind.yaml"
else
  kind create cluster --name "$cluster" --kubeconfig "$KUBECONFIG" --image "$image" --wait 180s
fi
control_plane_id=$(docker inspect --format '{{.Id}}' "$cluster-control-plane")
assert_disposable_cluster
if [[ "$variant" == cilium ]]; then
  cilium --kubeconfig "$KUBECONFIG" --context "$context" install --version 1.20.5 \
    --set ipam.mode=kubernetes --set operator.replicas=1 \
    --set image.useDigest=true --set image.digest=sha256:aa72499f075ad729947ea91b026218aceba24f1a8e4534bf36b8368c7c561526 \
    --set operator.image.useDigest=true --set operator.image.genericDigest=sha256:c6e7de6665aa9701db86c4fe46828a75a0d167afe084a9ffb5bce34b2c4fa8c4 \
    --wait --wait-duration 300s
  kube -n kube-system rollout status daemonset/cilium --timeout=120s
fi
kube version -o json | jq -e --arg version "v$kubernetes_version" \
  '.serverVersion.gitVersion == $version and .clientVersion.gitVersion == $version' >/dev/null
printf 'Kubernetes server and kubectl are both v%s.\n' "$kubernetes_version"
kubectl config set-context before-scan --cluster "$context" --user "$context" --namespace doctor-broken >/dev/null
kubectl config use-context before-scan >/dev/null
kube -n kube-system rollout status deployment/coredns --timeout=120s
image_ready=$(kube -n kube-system get deployment coredns -o jsonpath='{.spec.template.spec.containers[0].image}')
if [[ ! "$image_ready" =~ ^registry\.k8s\.io/coredns/coredns[:@][a-zA-Z0-9._:@-]+$ ]]; then
  printf 'Unexpected CoreDNS fixture image.\n' >&2
  exit 1
fi
sed "s|__COREDNS_IMAGE__|$image_ready|g" test/integration/workloads.yaml | mutate apply -f -

kube -n doctor-healthy rollout status deployment/ready-app --timeout=120s
kube -n doctor-healthy wait --for=condition=complete job/completed --timeout=120s
kube -n doctor-broken wait --for=jsonpath='{.status.containerStatuses[0].state.waiting.reason}'=CreateContainerConfigError pod/missing-config --timeout=120s
kube -n doctor-broken wait --for=jsonpath='{.status.containerStatuses[0].state.waiting.reason}'=ImagePullBackOff pod/bad-image --timeout=120s
kube -n doctor-broken wait --for=condition=PodScheduled=false pod/unschedulable --timeout=120s

wait_service_reconciliation doctor-broken empty-selector
kube -n doctor-healthy wait --for=jsonpath='{.endpoints[0].conditions.ready}'=true \
  endpointslices -l kubernetes.io/service-name=ready-app --timeout=120s

kubectl doctor namespace doctor-healthy --kubeconfig "$KUBECONFIG" --context "$context" --timeout 45s --output json --fail-on warning >"$test_dir/healthy.json"
assert_report "$test_dir/healthy.json" '
  .schema_version == "1" and
  .scope.kind == "namespace" and .scope.namespace == "doctor-healthy" and
  (.scope.context | type == "string")'
assert_report "$test_dir/healthy.json" '
  .complete and (.collection_errors | type == "array") and
  any(.sources[]; .source == "pod_metrics" and .status == "unavailable" and .required == false) and
  any(.checks[]; .id == "service.backends" and .status == "checked") and
  ([.findings[] | select(.severity == "warning" or .severity == "critical")] | length == 0)'
if ! jq -e --arg context "$context" '.scope.context == $context' "$test_dir/healthy.json" >/dev/null; then
  printf 'Unexpected kubeconfig context in the healthy report.\n' >&2
  exit 1
fi

kubectl doctor namespace doctor-healthy --timeout 45s --fail-on warning >"$test_dir/healthy.txt"
grep -F 'namespace doctor-healthy' "$test_dir/healthy.txt"

broken_status=0
kubectl doctor namespace doctor-broken --context "$context" --timeout 45s --output json --fail-on warning >"$test_dir/broken.json" || broken_status=$?
assert_report "$test_dir/broken.json" '
  .complete and .scope.namespace == "doctor-broken" and
  any(.findings[]; .resource.name == "missing-config" and .rule_id == "pod-containers" and
    any(.evidence[]; .field == "waiting_reason" and .value == "CreateContainerConfigError")) and
  any(.findings[]; .resource.name == "missing-config" and .rule_id == "configuration-references") and
  any(.findings[]; .resource.name == "bad-image" and .rule_id == "pod-containers" and
    any(.evidence[]; .field == "waiting_reason" and (.value == "ImagePullBackOff" or .value == "ErrImagePull"))) and
  any(.findings[]; .resource.name == "unschedulable" and .rule_id == "pod-scheduling") and
  any(.findings[]; .resource.name == "empty-selector" and .rule_id == "service.backends") and
  any(.findings[]; .resource.name == "missing-backend" and .rule_id == "ingress.backends") and
  all(.findings[]; .resource.namespace != "doctor-healthy")'
if ((broken_status != 1)); then
  printf 'Expected a complete failing scan to exit 1, got %s.\n' "$broken_status" >&2
  exit 1
fi

cluster_status=0
kubectl doctor cluster --context "$context" --timeout 60s --output json >"$test_dir/cluster.json" || cluster_status=$?
assert_report "$test_dir/cluster.json" '
  .complete and .scope.kind == "cluster" and
  any(.findings[]; .resource.namespace == "doctor-broken" and .resource.name == "missing-backend")'
if ((cluster_status != 0 && cluster_status != 1)); then
  printf 'Unexpected cluster-scan exit status: %s\n' "$cluster_status" >&2
  exit 1
fi

missing_status=0
kubectl doctor namespace doctor-not-created --context "$context" --timeout 45s --output json >"$test_dir/missing.json" || missing_status=$?
assert_report "$test_dir/missing.json" '
  (.complete | not) and .scope.namespace == "doctor-not-created" and
  any(.collection_errors[]; .source == "namespace" and .required == true)'
if ((missing_status != 2)); then
  printf 'Expected an incomplete scan to exit 2, got %s.\n' "$missing_status" >&2
  exit 1
fi

if [[ "$(kubectl config current-context)" != before-scan ]]; then
  printf 'A scan changed the current kubeconfig context.\n' >&2
  exit 1
fi

if grep -Fq 'KUBEDOCTOR_INTEGRATION_CREDENTIAL_SENTINEL' "$test_dir/"{healthy,broken,cluster,missing}.json "$test_dir/healthy.txt"; then
  printf 'A report exposed a seeded fixture credential.\n' >&2
  exit 1
fi

run_deep_diagnostics
case "$variant" in
  nodelocal) run_nodelocal_fixture ;;
  cilium) run_policy_fixture ;;
esac
run_connected_incident

printf 'Passed live Kubernetes %s integration (%s).\n' "$kubernetes_version" "$variant"
