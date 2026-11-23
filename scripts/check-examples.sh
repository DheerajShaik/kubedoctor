#!/usr/bin/env bash
# Reproduce every release example through the same offline CLI entry points.
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"
examples=docs/releases/v0.2.0/examples
scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT

go run ./test/examples "$scratch"
go build -o "$scratch/kubectl-doctor" ./cmd/kubectl-doctor
export KUBECONFIG="$scratch/does-not-exist"
"$scratch/kubectl-doctor" replay "$scratch/before.json" -o json --file "$scratch/investigation.json"
"$scratch/kubectl-doctor" replay "$scratch/before.json" -o html --file "$scratch/investigation.html"
"$scratch/kubectl-doctor" compare "$scratch/before.json" "$scratch/after.json" -o html --file "$scratch/comparison.html"
for file in before.json after.json investigation.json investigation.html comparison.html; do
  if ! cmp "$examples/$file" "$scratch/$file"; then
    printf 'Regenerate %s with the release example fixture.\n' "$file" >&2
    exit 1
  fi
done
