# Contributing to foip-operator

Thanks for considering a contribution.

## Scope

This repository is an operator for netcup failover IPs. Contributions are most useful when they improve one of these areas:

- Correctness of failover behavior
- RBAC and Kubernetes manifest alignment
- Observability, packaging, and release automation
- Tests, documentation, and operational safety

## Before You Open a PR

- Read the README and the release notes for the current version
- Check whether the change affects both controller binaries
- Prefer small, reviewable commits
- Include tests when behavior changes
- Check the recommended label names in [.github/labels.md](.github/labels.md) if you are adding or updating issue workflow labels

## Local Checks

Run the checks that match the area you changed:

```sh
make manifests
make generate
make lint
make test
make test-e2e
```

Not every change needs all of these, but changes to APIs, RBAC, or controller behavior usually do.

## Pull Request Expectations

- Explain the user-visible impact
- Mention any rollout or upgrade implications
- Call out changes to images, tags, or release automation
- Keep docs and examples in sync with code changes

## Style

- Follow the existing Go and Kubernetes conventions in the repo
- Keep reconciliation idempotent
- Favor explicit logging and metrics over silent behavior

## AI-Assisted Contributions

AI tools may help draft or review changes, but the contributor remains responsible for:

- Verifying correctness
- Running the relevant checks
- Ensuring licensing and attribution are appropriate
