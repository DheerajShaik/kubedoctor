# Internal correctness and security review

An independent reviewer inspected the collector, rule engine, CLI, and MCP adapter after the design review and before external review. Five findings were fixed in the implementation.

| Priority | Finding | Fix and evidence |
| --- | --- | --- |
| P1 | Client-go logged server-supplied Warning headers outside the report redaction boundary. The reviewer reproduced a seeded header in binary stderr. | Disabled client-go warning output and discarded contextual request logs. The transport tests and temporary-PATH kubectl smoke now verify that the sentinel stays out of stdout and stderr. |
| P1 | The SDK dispatches tool requests asynchronously, allowing overlapping scans to multiply collector work. | Added a shared one-slot scan gate. The test overlaps a namespace and cluster call, verifies the busy result, and retries after the earlier scan exits. |
| P2 | A generic StorageClass API 404 could be classified as a confirmed missing class. | Require that Kubernetes status details identify the requested class for an object-level miss. The wire fixtures distinguish unavailable API evidence. |
| P2 | Services could show a checked port status with no backend mapping evidence. A Service for an uninspected controller could also raise a warning even when backend intent was unknown. | Report inconclusive missing evidence and skip expected zero-backend checks. Preserve informational evidence when intent is unknown. The network fixtures cover both cases. |
| P2 | Extreme Metrics API decimals could expand during formatting or overflow conversion to bytes. | Check representation and unit ranges before running exact formatting or conversion. Extreme, negative, and boundary quantities have regression fixtures. |

The independent follow-up passed the full Go test suite and a rebuilt kubectl smoke. GitHub CI and external review are tracked on [PR #1](https://github.com/DheerajShaik/kubedoctor/pull/1).
