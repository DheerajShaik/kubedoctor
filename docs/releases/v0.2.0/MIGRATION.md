# Installing and upgrading

The v0.1 namespace and cluster commands retain report JSON schema `1`, filter semantics and exit thresholds. Existing report JSON is still a rendered result. Offline replay requires a v0.2 evidence bundle captured with the new command. Old reports cannot reconstruct uncaptured source evidence.

## Windows x64 release archive

Run in a new PowerShell directory with an authenticated GitHub CLI account that can see the unpublished draft. The snippet places the verified executable in the per-user directory used by the v0.1 installation. Close running instances before upgrading.

```powershell
$repo = 'DheerajShaik/kubedoctor'
$zip = 'kubectl-doctor-v0.2.0-windows-amd64.zip'
gh release download v0.2.0 --repo $repo --pattern $zip --pattern SHA256SUMS
if ($LASTEXITCODE -ne 0) { throw 'Release download failed' }
$expected = Get-Content SHA256SUMS | Where-Object { $_.EndsWith("  $zip") }
if (@($expected).Count -ne 1) { throw 'Expected one archive checksum' }
if ((Get-FileHash $zip -Algorithm SHA256).Hash -ne $expected.Split(' ')[0]) { throw 'Checksum mismatch' }
$stage = Join-Path ([IO.Path]::GetTempPath()) ([guid]::NewGuid().ToString())
Expand-Archive -Path $zip -DestinationPath $stage
$bin = Join-Path $env:USERPROFILE '.local\bin'
New-Item -ItemType Directory -Force -Path $bin | Out-Null
Copy-Item (Join-Path $stage 'kubectl-doctor.exe') (Join-Path $bin 'kubectl-doctor.exe') -Force
& (Join-Path $bin 'kubectl-doctor.exe') version
kubectl doctor version
```

## Linux amd64 release archive

Use an empty working directory and the same draft access. On ARM64 or macOS choose the corresponding archive. macOS provides `shasum -a 256` for SHA-256 verification.

```sh
archive=kubectl-doctor-v0.2.0-linux-amd64.tar.gz
gh release download v0.2.0 -R DheerajShaik/kubedoctor -p "$archive" -p SHA256SUMS
awk -v name="$archive" '$2 == name {print; found++} END {if (found != 1) exit 1}' SHA256SUMS > selected.sha256
sha256sum -c selected.sha256 && tar xzf "$archive" kubectl-doctor && install -Dm755 kubectl-doctor "$HOME/.local/bin/kubectl-doctor"
"$HOME/.local/bin/kubectl-doctor" version
```

Make the executable's directory available on PATH for kubectl plugin discovery. Source builds use the toolchain in go.mod; release binaries embed the exact tag and commit. A verified checksum establishes agreement with the checksum manifest supplied via GitHub.

## Try an investigation without Kubernetes

From the repository root with the plugin installed, choose new output filenames. The example bundles are deterministic synthetic observations of one Deployment and Pod before and after recovery.

```sh
kubectl doctor inspect docs/releases/v0.2.0/examples/before.json
kubectl doctor investigate docs/releases/v0.2.0/examples/before.json -o html --file investigation.html
kubectl doctor replay docs/releases/v0.2.0/examples/before.json -o json
kubectl doctor compare docs/releases/v0.2.0/examples/before.json docs/releases/v0.2.0/examples/after.json -o html --file comparison.html
```

Open the resulting HTML file directly in a browser. Standalone execution uses the same arguments after `kubectl-doctor`. For original final results use `replay --mode stored -o json`. For an intentional interpretation with the installed rules use `replay --mode reevaluate` and inspect the recorded rule versions.

## Capture in your selected context

```sh
kubectl doctor capture namespace payments --bundle before.json
# After an independently authorized change, capture the same scope again.
kubectl doctor capture namespace payments --bundle after.json
kubectl doctor compare before.json after.json --fail-on warning -o json --file regression.json
```

Capture and comparison are read-only. This pipeline example uses the current runner's separately provisioned read-only kubeconfig. It embeds no credentials and deploys no workloads. The comparison exits 2 for insufficient evidence or an incompatible pair and 1 for a qualifying regression. A passing gate covers the documented diagnostic checks. The [compatibility lab](LAB.md) describes the connected disposable-cluster demonstration.
