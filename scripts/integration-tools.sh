#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  printf 'Usage: %s <1.35.8|1.36.4> <tool-directory>\n' "$0" >&2
  exit 2
fi

kubernetes_version=$1
mkdir -p "$2"
tool_dir=$(cd "$2" && pwd)
platform="$(uname -s)/$(uname -m)"

case "$platform" in
  Linux/x86_64) architecture=amd64 ;;
  Linux/aarch64|Linux/arm64) architecture=arm64 ;;
  *)
    printf 'The pinned tool installer supports Linux amd64 and arm64. Install kind v0.33.0 and kubectl v%s for %s.\n' "$kubernetes_version" "$platform" >&2
    exit 2
    ;;
esac

case "$kubernetes_version/$architecture" in
  1.35.8/amd64) sha256=874d5e72dbb819f43cff16bcd1e4f8bac5b7f2361fe1e55049b0a6c676fb0cbf ;;
  1.35.8/arm64) sha256=cc749967b62f4422260bc9c0aa7a7c55f45175ae38cb8d95767b5d2b7e04c1fd ;;
  1.36.4/amd64) sha256=8b8f088da2dab964f853b38464033b1be15ede2839eca751482357c45abdd05a ;;
  1.36.4/arm64) sha256=0ecf44450ee6063bf19dd166a103ee6df4a9034455c2abce626e6eea657d73fb ;;
  *)
    printf 'Unsupported Kubernetes test version: %s\n' "$kubernetes_version" >&2
    exit 2
    ;;
esac

download_dir=$(mktemp -d "${TMPDIR:-/tmp}/kubedoctor-tools.XXXXXXXX")
trap 'rm -rf -- "$download_dir"' EXIT
curl --fail --silent --show-error --location --retry 2 --max-time 90 \
  -o "$download_dir/kubectl" \
  "https://dl.k8s.io/release/v${kubernetes_version}/bin/linux/${architecture}/kubectl"
printf '%s  %s\n' "$sha256" "$download_dir/kubectl" | sha256sum --check
install -m 0755 "$download_dir/kubectl" "$tool_dir/kubectl"
GOBIN="$tool_dir" go install sigs.k8s.io/kind@v0.33.0
"$tool_dir/kind" version
"$tool_dir/kubectl" version --client
