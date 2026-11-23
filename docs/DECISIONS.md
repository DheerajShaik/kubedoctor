# Decisions

## 2026-11-23 — Versions and coverage

The release targets Kubernetes 1.35 and 1.36. The stable typed APIs used in both are available in client-go v0.36.6. The kind release supplies pinned images for both target minors. Go 1.27.4, Cobra 1.10.2 and the official MCP Go SDK 1.8.0 are pinned. Future Kubernetes minors require validation before expanding the compatibility claim. [Research](RESEARCH.md)

## 2026-11-23 — Coverage and process exit

The engine collects available observations first and evaluates deterministic rules even when an endpoint fails. Required collection failures take exit 2 precedence. Optional evidence, including Metrics API data, standard DNS components, and Secret-key confirmation from events, has separate visible coverage. Unknown checks cannot substantiate a healthy conclusion. The display severity filter applies only after exit status has been determined. This keeps the same signal in scripts regardless of presentation.

## 2026-11-23 — Kubernetes reads and retained data

The collector accepts a user-selected kubeconfig context and applies a GET-route allowlist on its Kubernetes transport. Namespace scans include named hosting nodes, referenced storage classes and bounded conventional DNS reads. Distinct route and projection guards address subresource reads and sensitive values. Raw events, Secret data, environment literals and command lines do not enter findings. Optional direct Secret reads are outside v0.1. [Design review](DESIGN_REVIEW.md)

## 2026-11-23 — MCP client policy

The stdio process binds Kubernetes credentials and context at startup. Callers get a namespace allowlist and a separate cluster-scan switch. The adapter uses the same engine and report. A fixed scan budget and output cap apply regardless of client behavior.
