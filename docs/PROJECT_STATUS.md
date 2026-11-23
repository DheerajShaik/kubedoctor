# Project status

## v0.2.0 in development

Baseline `2f31b93` and the v0.1.0 draft verified on GitHub. The repository is public. The v0.2.0 feature branch is `codex/v0.2-investigations`. The first integrated revision is `bca2c25` on [PR #2](https://github.com/DheerajShaik/kubedoctor/pull/2). [Verification record](releases/v0.2.0/VALIDATION.md) tracks executed tests and limits. See the [release charter](releases/v0.2.0/RELEASE_CHARTER.md), [design and public contracts](releases/v0.2.0/DESIGN.md), and [acceptance matrix](releases/v0.2.0/ACCEPTANCE.md).

## v0.1.0 historical checkpoints

- Charter FROZEN-v1.0. The initial upstream commit was `3204c076bf82147d211d51d43773e1173966699c` on `main`. The implementation branch is `codex/kubedoctor-v0-1`.
- The CLI implements 17 checks with versioned JSON, terminal output, scoped collection, and partial coverage. The stdio server shares the engine and supports three tools subject to the configured scope. The [validation record](VALIDATION.md) distinguishes synthetic from live tests.
- The independent design review and implementation review completed. The fixes are in [implementation review](INTERNAL_REVIEW.md).
- [PR #1](https://github.com/DheerajShaik/kubedoctor/pull/1) was opened as a draft at `b357f3f5c97d307312968ae36e07a820eebb908c` to run the first hosted Kubernetes matrix. Review found a dependency update and further correctness/security follow-ups. The live PR provides the current head and check status.
- The local rootless Docker runtime cannot start kind because cgroup delegation is unavailable. Hosted CI runs the live suites. Repository access and implementation branch pushes were verified against GitHub. The GitHub connector provides PR and CI access even though the shell's GitHub API route returns HTTP 403.
- Before merging, verify hosted CI and completed external Codex review for the current PR head. Verify branch restrictions, merge state, default-branch validation and the unpublished draft release on GitHub. Follow the [charter](../PROJECT_CHARTER.md).
