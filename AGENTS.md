# Repository Guidelines

## Project Structure & Module Organization

`library-olm` is a Go module intended to host multiple OLM libraries and tools. Keep each component self-contained: production packages belong under its top-level directory, component CLIs under `<component>/examples/cmd/`, component tests under `test/e2e/<component>/`, and supporting scripts under `hack/e2e/<component>/`.

The current component is `migration/`: reusable OLMv0-to-OLMv1 code is in `migration/pkg/`, and its CLIs are in `migration/examples/cmd/`. Its E2E fixtures live in `test/e2e/migration/fixtures/`. Migration specifications are in `specs/20260821-migration-v0-to-v1/`; update requirements, plan, validation, and E2E documentation together when changing migration behavior.

## Build, Test, and Development Commands

- `make build` builds both CLI binaries into `bin/`.
- `make build-all` compiles every package.
- `make verify` runs `go mod tidy`, formatting, vet, lint, and fails on resulting diffs.

### Migration component

- `make migration/test-unit` runs `./migration/...` tests and writes coverage to `artifacts/coverage/`.
- `make migration/e2e-fixture-setup` then `make migration/test-e2e-fixture-matrix` runs deterministic fixture E2E tests. Tear down with `E2E_CLUSTER_NAME=library-olm-fixture-e2e make migration/e2e-teardown`.
- `make migration/e2e-setup`, `make migration/test-e2e-live-matrix`, and `make migration/e2e-teardown` run live-operator coverage. See `specs/20260821-migration-v0-to-v1/e2e.md` for focused scenarios and run order.

## Coding Style & Naming Conventions

Use `gofmt` and keep imports compatible with the `gci` configuration. Prefer idiomatic Go names: exported APIs use `CamelCase`; package-private helpers use `camelCase`; tests follow `TestThingBehavior`. Wrap errors with useful operation context and preserve error identity with `%w`. Keep Kubernetes resource mutations explicit and validate destructive preconditions before changing OLMv0 resources.

## Testing Guidelines

Add unit coverage beside the affected component package using Go's standard `testing` package and fake controller-runtime clients where practical. E2E tests require the `e2e` build tag and should use their component Make targets rather than an ambient kubeconfig. Preserve committed fixtures as sanitized inputs; do not refresh them unintentionally. For migration, use `make migration/report-coverage-all` only after unit and relevant E2E coverage data exists.

## Commit & Pull Request Guidelines

Use concise conventional-style subjects such as `fix: ...`, `feat: ...`, `test: ...`, or `docs: ...`. Sign commits off (`git commit --signoff`) and preserve existing trailers. Keep PRs focused; describe behavior and risk, link the relevant issue or review thread, list validation commands, and call out required E2E or documentation updates.
