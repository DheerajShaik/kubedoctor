# Validation

Run from the repository root with the version in go.mod.

```sh
go test ./...
go vet ./...
go test -race ./...
go build ./cmd/kubectl-doctor
```

Record exact commands, revision, results, and limitations below as checks run.

| Check | Observed result |
| --- | --- |
| Frozen charter | Attachment and `PROJECT_CHARTER.md` matched byte for byte |
| Repository | Verified private, correct owner and main branch via GitHub connector |
| Runtime | Linux amd64, Go 1.27.4 verified by official SHA-256, rootless Docker server 29.9.2 reachable |
| Release gates | Awaiting implementation |
