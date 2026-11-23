#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
if [[ -n "${KUBEDOCTOR_PROMETHEUS_BIN:-}" && -n "${KUBEDOCTOR_PROMTOOL_BIN:-}" ]]; then
  go test -count=1 -v ./internal/history -run '^TestPrometheusIntegration$'
  exit
fi

if [[ "$(uname -s)/$(uname -m)" != Linux/x86_64 ]]; then
  printf 'Set KUBEDOCTOR_PROMETHEUS_BIN and KUBEDOCTOR_PROMTOOL_BIN to verified native v3.16.0 binaries.\n' >&2
  exit 2
fi

version=3.16.0
sha256=68727009472a2c02e1ff7f062fd5b3e637285bffc8fc2d340c76fb449d6e0f34
name="prometheus-${version}.linux-amd64"
test_dir=$(mktemp -d "${TMPDIR:-/tmp}/kubedoctor-prometheus.XXXXXXXX")
trap 'rm -rf -- "$test_dir"' EXIT

curl --fail --location --silent --show-error --retry 2 --max-time 120 \
  -o "$test_dir/$name.tar.gz" \
  "https://github.com/prometheus/prometheus/releases/download/v${version}/$name.tar.gz"
printf '%s  %s\n' "$sha256" "$test_dir/$name.tar.gz" | sha256sum --check
tar -xzf "$test_dir/$name.tar.gz" -C "$test_dir" "$name/prometheus" "$name/promtool"
KUBEDOCTOR_PROMETHEUS_BIN="$test_dir/$name/prometheus" \
  KUBEDOCTOR_PROMTOOL_BIN="$test_dir/$name/promtool" \
  go test -count=1 -v ./internal/history -run '^TestPrometheusIntegration$'
