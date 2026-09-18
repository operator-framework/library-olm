# E2E Test Strategy — OLMv0 → OLMv1 Migration

This document implements the Phase 8 E2E plan from [validation.md](validation.md).
It deliberately uses two suites: deterministic fixture scenarios for the migration tool's
decision and recovery paths, and real-operator smoke scenarios for controller adoption.
Both suites are required CI gates for pull requests, merge queues, and pushes to `main`.

## Test environments

| Suite | Cluster | Catalog content | Purpose |
|---|---|---|---|
| `fixture` | kind + OLMv1 + OLMv0 CRDs only (no OLMv0 controllers) | Committed OLMv0-install snapshots and a digest-pinned CatalogSource | Deterministic coverage of V1, V2, V3, V4. |
| `real-operator` | separate kind cluster with OLMv0 + OLMv1 | OLMv0's installed OperatorHub catalog | Proves V5.2–V5.8 with real deployed operators. |
| `kind-only` | kind + OLMv1 | Local fixture objects, no OLMv0 controllers | Fast contract tests for resource rendering and COS adoption prerequisites. |

Do not use a mutable `latest` image or an unpinned release installer in CI. The default
bootstrap pins OLMv0 to `v0.46.0`, OLMv1 to `v1.11.0`, and kind to `v0.31.0` with the
digest-pinned Kubernetes `v1.36.1` node image; CI should
mirror those release artifacts and override the URLs when it cannot access GitHub. The job inputs
are the kind node image, OLMv0 manifest URL and digest, operator-controller release
manifest URL and digest, fixture catalog image digest, and (only for the smoke suite) the
catalog image/package/channel/CSV version. Catalog images are specified by immutable SHA
digests, never `latest`, so a CI run is reproducible. Record these in the workflow job summary.

## Bootstrap

1. Run `make migration/e2e-setup` to create (or reuse) an isolated kind cluster named `library-olm-e2e` using
   [`e2e/migration/kind-config.yaml`](../../e2e/migration/kind-config.yaml), exporting an explicit
   Kind-generated kubeconfig at `.kubeconfig/library-olm-e2e`. `E2E_KUBECONFIG` is only
   an optional override for that generated output path.
2. Apply the pinned OLMv0 quickstart manifest and wait for both `olm-operator` and
   `catalog-operator` deployments to be Available.
3. Install the pinned operator-controller release manifest and wait for its deployment,
   catalogd, cert provider, and required CRDs to be established.
4. Apply the committed OLMv0-install snapshots, including the digest-pinned fixture
   `CatalogSource`, then migrate it with `migrate-catalogs-v0-to-v1` and wait for its
   `ClusterCatalog` to become `Serving=True`.
5. Before each scenario, create a new namespace and apply exactly one fixture set. After
   each scenario, collect all migration objects, CSV/Subscription/InstallPlan events, pod
   logs, and `kubectl get --all-namespaces -o yaml` for the test namespace on failure.

The upstream projects currently provide a checked-in OLMv0 quickstart manifest and an
operator-controller kind/E2E installation flow. This repository owns version pinning and
the combined-cluster compatibility check rather than copying either project's mutable
installer. See the [OLMv0 quickstart manifest](https://github.com/operator-framework/operator-lifecycle-manager/blob/master/deploy/upstream/quickstart/olm.yaml)
and [operator-controller E2E guidance](https://github.com/operator-framework/operator-controller/blob/main/AGENTS.md).

## Fixtures

The fixture catalog contains small deployable bundles, all using a harmless pause-style
Deployment and a test CRD. It must provide independent packages for:

- `eligible`: an AllNamespaces CSV with a CRD, ServiceAccount, Role/Binding, Service and
  Deployment. It is the baseline conversion, rollback, and CRD-adoption package.
- `c1-watch-scope`, `c4-condition`, `c5-v0-rbac`, `c6-serviceaccount`, and `c8-unsteady`:
  each contains exactly one soft incompatibility. Each scenario runs once without its
  acknowledgment and once with it, asserting the CE audit annotation.
- `c2-package-dependency`, `c2-gvk-dependency`, `c3-apiservice`, and `c9-generated`:
  hard-block fixtures. They never create a CE.
- `large-bundle`: objects sufficient to force SecretPacker externalization.
- `shared-crd-a` and `shared-crd-b`: two packages owning the same CRD, proving
  `IfNoController` adoption.

Fixtures are source-controlled YAML/FBC inputs. The image is built once per CI run,
tagged with the commit SHA, loaded with `kind load docker-image`, and never pulled from a
public mutable catalog.

## Scenario matrix

| Validation IDs | Scenario | Required assertions |
|---|---|---|
| V1.1, V1.2, V1.3, V3.1–V3.7 | Baseline fixture migration | `check`, dry-run, COS `Succeeded=True` before CE creation, CE `Installed=True`, field mappings, CE backups/audit, Sub/CSV cleanup. |
| V1.4, V3.18 | Rollback | Refusal without acknowledgment for installed CE; CE/COS deletion and Subscription restoration with acknowledgment; on-disk backup files exist before deletion. |
| V1.5, V4.1 | Conflict cleanup | CE stays; Subscription and OLMv0 artifacts are removed; shared OperatorGroup is retained. |
| V1.6–V1.8, V2.10 | Batch and argument behavior | Four-section ordering; only Eligible converts; stop/continue behavior; a Subscription named `check` works. |
| V2.1–V2.9, V4.3–V4.5 | Eligibility matrix | One test per incompatibility, expected reason, no mutation on refusal, acknowledgment flips only soft cases. |
| V3.8–V3.11, V3.13–V3.17, V3.19 | Catalog migration | image, poll interval, priority, unsupported source, dedup/adoption, overflow, and deletion-reference behavior. |
| V3.12, V4.2, V4.6 | Collection and adoption | Deployment config survives an OLMv1 upgrade, shared CRD adoption succeeds, and large payload uses Secrets. |
| V5.1–V5.9 | Real-operator smoke | Bootstrap, install a pinned AllNamespaces operator, catalog migration, conversion, upgrade, rollback, and four-state batch scan. |
| V4.7 | Namespace change | Deferred: Phase 6 is blocked. Add only when its upstream prerequisite lands. |

## Test implementation and coverage

Use Go's `testing` package with `testify`-free polling helpers and `client-go` against the
kind kubeconfig. Tests are in `test/e2e/migration`, protected by the `e2e` build tag so `make migration/test-unit`
remains unit-only. The test process invokes the built CLIs for command behavior; it uses
the Kubernetes client only for setup and assertions. Every wait has a bounded timeout and
prints current objects/events on timeout.

`make migration/test-e2e-fixture-matrix` runs only deterministic fixture scenarios. `make
migration/test-e2e-live-matrix` runs only the real-operator smoke scenarios. Both use the Kind-generated
kubeconfig by default. The
targets require a pre-bootstrapped cluster rather than silently provisioning or deleting
one; bootstrap automation will be added with the pinned installation inputs. Both commands
write diagnostics below `E2E_ARTIFACTS` (default: `artifacts/e2e`).

Coverage is optional because the migration binaries run outside the Go test process. E2E CLI
binaries are always built with `go build -cover`, and the E2E targets collect their coverage
under `artifacts/e2e/coverage`. `make migration/test-unit` always writes the unit profile under
`artifacts/coverage`; after both E2E matrices have run, `make migration/report-coverage-all`
merges the existing profiles and displays the combined result. `make migration/test-coverage-all`
reruns the unit suite before displaying that report. The report fails if unit or E2E coverage is
absent, preventing a misleading partial result. Upload the merged profile
and failure artifacts, but do not impose an E2E percentage threshold; the unit suite owns the
≥80% gate. This avoids treating controller waits and external command plumbing as unit
coverage while still showing which migration paths the E2E suite executes.

## CI rollout

1. The `migration-test` workflow runs unit coverage, fixture E2E, and live-operator E2E
   independently, each with its own Kind cluster and kubeconfig; a fourth job merges their
   coverage profiles and displays the total report.
2. Run all four jobs for pull requests, merge queues, and pushes to `main`. The fixture and
   live-operator suites are required merge gates. Preserve E2E diagnostics and CLI coverage
   artifacts for failed jobs.
3. Retry only provisioning/image-pull failures once; never retry a failed assertion automatically.

## Run order

Run each suite independently from the repository root. The live and fixture suites use
different Kind clusters and kubeconfigs.

### Unit tests

`make migration/test-unit` is fully independent of E2E setup: it does not create or contact a Kind
cluster, require a kubeconfig, or run files behind the `e2e` build tag.

```bash
make migration/test-unit
```

### Live-operator tests

This suite creates its own live environment with OLMv0 and OLMv1. The matrix installs each
operator through OLMv0 and deletes it as an OLMv1 extension afterward.

```bash
make migration/e2e-setup
make migration/test-e2e-live-matrix
make migration/e2e-teardown
```

### Fixture tests

This suite uses a separate cluster containing OLMv1 and OLMv0 CRDs, but no OLMv0
controllers. It replays the committed OLMv0 snapshots.

```bash
make migration/e2e-fixture-setup
make migration/test-e2e-fixture-matrix
E2E_CLUSTER_NAME=library-olm-fixture-e2e make migration/e2e-teardown
```

`migration/test-e2e-live-matrix` installs each package through OLMv0 itself, so neither
`migration/e2e-install-v0` nor `migration/e2e-snapshot-v0` is part of the normal run order.

### Optional snapshot refresh

Snapshots in `test/e2e/migration/fixtures/snapshots/` are committed test data. To deliberately update that
baseline, use a live cluster before running its migration matrix:

```bash
make migration/e2e-setup
make migration/e2e-install-v0 E2E_OPERATOR=all
make migration/e2e-snapshot-v0 E2E_OPERATOR=all
git add test/e2e/migration/fixtures/snapshots/
git diff --cached
git commit -m 'test: refresh OLMv0 operator snapshots'
make migration/e2e-teardown
```

For a single package, replace `all` with its name from `e2e/migration/operators.tsv`, for example
`make migration/e2e-install-v0 E2E_OPERATOR=redis-operator`. Snapshot capture must occur while the
operator remains OLMv0-managed. Fixture CI never captures snapshots, so it can run
independently and in parallel with the live suite.
