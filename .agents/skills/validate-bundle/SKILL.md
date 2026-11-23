---
name: validate-bundle
description: Check the KubeDoctor evidence format and offline entry points after changing a collector, schema or analyzer.
---

Read the release charter, bundle contract, and identity rules. For each collector field, identify the supported rule or relationship that requires it. Add the minimized representation and strict import check together. Preserve partial source states, schema versions, object UIDs and observed times. Test that current capture and offline replay agree, use a bad kubeconfig with networking disabled, and confirm old/different rule versions require an explicit interpretation. Use only synthetic bundles in examples and PRs.
