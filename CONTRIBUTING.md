# Contributing to library-olm

`library-olm` is an Apache 2.0-licensed collection of Go libraries and example CLIs for Operator Lifecycle Manager. Contributions through GitHub issues and pull requests are welcome. The module is still at `v0`, so discuss public API changes early: callers should not assume API stability yet.

## Discuss the change

For a substantial feature or behavior change, open an issue or start a discussion before coding. The OLM community also meets in the [#olm-dev Slack channel](https://kubernetes.slack.com/archives/C0181L6JYQ2) and the [OLM working group](https://github.com/operator-framework/community#operator-lifecycle-manager-working-group). Small fixes can go straight to a focused pull request. Use a draft PR when you want feedback on an unfinished approach.

## Become an Operator Framework member

You do not need GitHub organization membership to contribute. For membership, follow the [Operator Framework contributor ladder](https://github.com/operator-framework/community/blob/master/contributor-ladder.md): stay active in a project area, enable GitHub two-factor authentication, subscribe to the Operator Framework mailing list, and document your contributions. Find two eligible sponsors as described in the ladder; the community repository uses [CODEOWNERS](https://github.com/operator-framework/community/blob/master/CODEOWNERS) to identify its maintainer team. If you need help finding sponsors, ask in #olm-dev. Then [open a membership request](https://github.com/operator-framework/community/issues/new?template=membership.md), mention both sponsors, complete the checklist, and include representative contributions. An organization admin reviews the request after the sponsors confirm it.

## Set up and validate locally

Use the Go version declared in [`go.mod`](go.mod). Tool versions are pinned through Bingo; `make help` lists available targets.

```sh
make build                 # build the example CLIs into bin/
make build-all             # compile all packages
make migration/test-unit   # test migration packages and collect coverage
make verify                # tidy, format, vet, lint, and check the diff
```

For migration E2E changes, follow the independent fixture and live-cluster run orders in the [E2E guide](specs/20260821-migration-v0-to-v1/e2e.md). Fixture setup uses committed snapshots and no OLMv0 controller; live setup installs real operators through OLMv0. Do not refresh committed fixtures as a side effect of ordinary tests. Remove only the dedicated Kind cluster you created when finished.

## Keep changes reviewable

Place reusable component code under `<component>/pkg/`, example CLIs under `<component>/examples/cmd/`, and E2E tests under `test/e2e/<component>/`. The current component is `migration/`; avoid making repository-wide guidance or tooling migration-specific. Use `gofmt`, idiomatic Go names, and tests named `Test<Behavior>`. Include unit tests for changed branches and E2E coverage when behavior depends on Kubernetes or OLM controllers. For migration behavior changes, update the relevant requirements, plan, validation, and E2E documentation together.

## Submit a pull request

By contributing, you certify the [Developer Certificate of Origin](https://developercertificate.org/). Sign every commit with `git commit --signoff` and preserve existing trailers when rewriting commits. Use a short, descriptive subject such as `fix: reject unsafe migration target` or `test: cover catalog adoption`.

The [PR template](.github/pull_request_template.md) asks for a summary and motivation, API documentation, tests, useful commit messages, and links to related issues. In the PR description, include the validation commands you ran and call out any E2E suite you could not run. Avoid adding a dependency or raising the Go version without explaining the need and review impact.
