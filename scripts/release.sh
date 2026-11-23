#!/usr/bin/env bash
set -euo pipefail
umask 022

version=${1:?usage: scripts/release.sh v0.2.0 [output-directory]}
if [[ ! $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'Invalid release version %s\n' "$version" >&2
  exit 2
fi
repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"
revision=$(git rev-parse HEAD)
build_time=$(git show -s --format=%ct "$revision")
output=${2:-dist}
mkdir -p "$output"
output=$(cd "$output" && pwd)
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT
export CGO_ENABLED=0 TZ=UTC
platforms=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)
archives=()
for platform in "${platforms[@]}"; do
  target_os=${platform%/*}
  target_arch=${platform#*/}
  archive_name="kubectl-doctor-${version}-${target_os}-${target_arch}"
  stage="$staging/$archive_name"
  mkdir "$stage"
  executable=kubectl-doctor
  if [[ $target_os == windows ]]; then
    executable+=.exe
  fi
  GOOS="$target_os" GOARCH="$target_arch" go build -trimpath -buildvcs=false \
    -ldflags "-X main.version=$version -X main.commit=$revision" -o "$stage/$executable" ./cmd/kubectl-doctor
  cp LICENSE README.md "$stage/"
  chmod 0755 "$stage/$executable"
  chmod 0644 "$stage/LICENSE" "$stage/README.md"
  names=("$executable" LICENSE README.md)
  if [[ $target_os == windows ]]; then
    archive="$archive_name.zip"
    touch -d "@$build_time" "$stage/$executable" "$stage/LICENSE" "$stage/README.md"
    # Create the zip in an empty staging area to avoid updating a prior archive.
    (cd "$stage" && zip -X -q "$staging/$archive" "${names[@]}")
    mv "$staging/$archive" "$output/$archive"
  else
    archive="$archive_name.tar.gz"
    tar --sort=name --mtime="@$build_time" --owner=0 --group=0 --numeric-owner \
      -C "$stage" -cf - "${names[@]}" | gzip -n > "$output/$archive"
  fi
  archives+=("$archive")
done
(cd "$output" && sha256sum "${archives[@]}" > SHA256SUMS)

# Ship synthetic incident examples and the release contracts as a separate
# archive so that operators can try offline replay before connecting a cluster.
release_docs="docs/releases/$version"
if [[ -d $release_docs/examples ]]; then
  guide="kubedoctor-${version}-examples"
  mkdir -p "$staging/$guide"
  cp -r "$release_docs/examples" "$staging/$guide/"
  cp "$release_docs"/{MIGRATION,LAB,BUNDLE,INVESTIGATION,HISTORY,HTML,DIAGNOSTICS,ACCEPTANCE,VALIDATION,REPRODUCE}.md "$staging/$guide/"
  cp LICENSE "$staging/$guide/"
  find "$staging/$guide" -type f -exec chmod 0644 {} +
  example_archive="$guide.zip"
  (cd "$staging" && find "$guide" -type f -exec touch -d "@$build_time" {} + && find "$guide" -type f | LC_ALL=C sort | zip -X -q "$example_archive" -@)
  mv "$staging/$example_archive" "$output/$example_archive"
  (cd "$output" && sha256sum "$example_archive" >> SHA256SUMS)
fi
