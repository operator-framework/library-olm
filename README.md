# library-olm

A `v0` collection of Go libraries and CLIs for Operator Lifecycle Manager (OLM).

This repository is the basis for the eventual `operator-framework/library-olm`. It is
personal and pre-release: APIs may change without notice while at `v0`.

## Contents

- [`migration/`](migration/) — OLMv0 → OLMv1 migration library and CLIs
  ([OCPSTRAT-2693](https://redhat.atlassian.net/browse/OCPSTRAT-2693)). Start with
  [`specs/20260821-migration-v0-to-v1/README.md`](specs/20260821-migration-v0-to-v1/README.md).

The migration work follows a Specification-Driven Design (SDD) layout:

| Doc | Purpose |
|---|---|
| [specs/20260821-migration-v0-to-v1/README.md](specs/20260821-migration-v0-to-v1/README.md) | Overview of the feature |
| [specs/20260821-migration-v0-to-v1/requirements.md](specs/20260821-migration-v0-to-v1/requirements.md) | What must be built, field-by-field |
| [specs/20260821-migration-v0-to-v1/plan.md](specs/20260821-migration-v0-to-v1/plan.md) | How it will be built (phased) |
| [specs/20260821-migration-v0-to-v1/validation.md](specs/20260821-migration-v0-to-v1/validation.md) | How we prove it works |

## Development tools

The repository pins `golangci-lint`, `go-apidiff`, `kind`, and `crane` in the
`tool` block of `go.mod`. Run them with `go tool <name>` or through the Make
targets that use them, such as `make lint` and `make migration/e2e-fixture-setup`.

## License

Apache 2.0 — see [LICENSE](LICENSE).
