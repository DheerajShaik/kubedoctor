---
name: validate-review
description: Verify KubeDoctor changes, inspect diagnostic safety, and record release gate evidence for the revision.
---

Start with docs/VALIDATION.md and identify the exact revision. Run relevant local checks and exercise real entry points. Verify the Kubernetes HTTP method and path boundary, seeded-sensitive fixtures, exit precedence, and MCP stdio. Capture results and unverified items in project status. For a PR verify CI, external review, head, and protections independently. Merge only when the charter gates hold.
