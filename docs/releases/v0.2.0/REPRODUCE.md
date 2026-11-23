# Regenerate the offline example

From a clean checkout with the Go toolchain in go.mod and a new temporary directory, run the commands below. The generator constructs two synthetic Kubernetes observations with fixed UIDs and timestamps and never opens a cluster client.

```sh
out=$(mktemp -d)
go run ./test/examples "$out"
go build -o "$out/kubectl-doctor" ./cmd/kubectl-doctor
"$out/kubectl-doctor" replay "$out/before.json" -o json --file "$out/investigation.json"
"$out/kubectl-doctor" replay "$out/before.json" -o html --file "$out/investigation.html"
"$out/kubectl-doctor" compare "$out/before.json" "$out/after.json" -o html --file "$out/comparison.html"
```

The first capture shows a blocked image pull and a Deployment past its progress deadline. The second preserves the Pod and controller UIDs and reports a running, ready container. See the [incident lab](LAB.md) for a separate connected demonstration against live Kubernetes.
