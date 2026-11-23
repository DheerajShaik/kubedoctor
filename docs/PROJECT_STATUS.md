# Project status

## v0.2.0 incident investigation

The release includes 26 diagnostic checks, minimized evidence capture, offline reproduction, controller-aware comparison, standalone reports, optional historical sizing and scoped MCP investigation. The repository is public. The release workflow prepares binaries and a checksum manifest in an unpublished draft after the release revision passes its gates.

[PR #2](https://github.com/DheerajShaik/kubedoctor/pull/2) records the final reviewed revision, merge state and hosted CI. The [Actions history](https://github.com/DheerajShaik/kubedoctor/actions/workflows/ci.yml?query=branch%3Amain) records default-branch verification. The [releases page](https://github.com/DheerajShaik/kubedoctor/releases) shows the unpublished draft to authorized collaborators after packaging. See [executed validation and benchmark results](releases/v0.2.0/VALIDATION.md), the [acceptance matrix](releases/v0.2.0/ACCEPTANCE.md), the [release charter](releases/v0.2.0/RELEASE_CHARTER.md), and the [design](releases/v0.2.0/DESIGN.md).

## v0.1.0 historical checkpoints

- Charter FROZEN-v1.0. The initial upstream commit was `3204c076bf82147d211d51d43773e1173966699c` on `main`. The implementation branch is `codex/kubedoctor-v0-1`.
- The CLI implements 17 checks with versioned JSON, terminal output, scoped collection, and partial coverage. The stdio server shares the engine and supports three tools subject to the configured scope. The [validation record](VALIDATION.md) distinguishes synthetic from live tests.
- The independent design review and implementation review completed. The fixes are in [implementation review](INTERNAL_REVIEW.md).
- [PR #1](https://github.com/DheerajShaik/kubedoctor/pull/1) was opened as a draft at `b357f3f5c97d307312968ae36e07a820eebb908c` to run the first hosted Kubernetes matrix. Review found a dependency update and further correctness/security follow-ups. The live PR provides the current head and check status.
- The local rootless Docker runtime cannot start kind because cgroup delegation is unavailable. Hosted CI runs the live suites. Repository access and implementation branch pushes were verified against GitHub. The GitHub connector provides PR and CI access even though the shell's GitHub API route returns HTTP 403.
- Before merging, verify hosted CI and completed external Codex review for the current PR head. Verify branch restrictions, merge state, default-branch validation and the unpublished draft release on GitHub. Follow the [charter](../PROJECT_CHARTER.md).
