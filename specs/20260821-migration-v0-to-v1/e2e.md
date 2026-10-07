# E2E Test Strategy — OLMv0 → OLMv1 Migration

This document implements the Phase 8 E2E plan from [validation.md](validation.md).
It uses deterministic fixture scenarios for the migration tool's decision and recovery paths,
real-operator smoke scenarios for controller adoption, and an in-cluster Job that verifies
ServiceAccount authentication. All are required CI gates for pull requests, merge queues, and
pushes to `main`.

## Test environments

| Suite | Cluster | Catalog content | Purpose |
|---|---|---|---|
| `fixture` | kind + OLMv1 + OLMv0 CRDs only (no OLMv0 controllers) | Committed OLMv0-install snapshots and a digest-pinned CatalogSource | Deterministic coverage of V1, V2, V3, V4. |
| `real-operator` | separate kind cluster with OLMv0 + OLMv1 | OLMv0's installed OperatorHub catalog | Covers V5.2–V5.6 plus cleanup and healthy rollback (V5.8). V5.7 upgrade remains a gap. |
| `in-cluster-job` | fixture cluster | A replayed ecr-secret-operator installation | Proves both CLIs authenticate and migrate using only a Pod ServiceAccount. |
| `kind-only` | kind + OLMv1 | Local fixture objects, no OLMv0 controllers | Fast contract tests for resource rendering and COS adoption prerequisites. |

Do not use a mutable `latest` image or an unpinned release installer in CI. The default
bootstrap pins OLMv0 to `v0.46.0`, OLMv1 to `v1.12.0`, and kind to `v0.33.0` with the
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
| V4.7 | Cross-namespace fixture migration | Target namespace is created with copied PSA/SCC labels; source Deployments are scaled to zero before target creation; CE and rendered Deployment use the target; collected source Deployment is removed while the source namespace remains. |
| V4.7 | Acknowledged live namespace deletion | A real OLMv0 installation is converted with `--acknowledge-namespace-delete`; after CE installation in the target namespace, OLMv0 finalizes the CSV and the source namespace is deleted. |
| V4.8 | System-managed namespace fixture migration | Against the experimental controller profile, verify the explicit opt-in omits `spec.namespace`, migration imports the prepared bundle-metadata namespace into its COS, the catalog COS takes ownership, OLMv1 manages the target Deployment there, and source copies are removed. Unit tests verify unsupported CRDs reject the mode before mutation. |
| V1.4, V3.18 | Rollback | Refusal without acknowledgment for installed CE; CE/COS deletion and Subscription restoration with acknowledgment; on-disk backup files exist before deletion. |
| V1.5, V4.1 | Conflict cleanup | CE stays; Subscription and OLMv0 artifacts are removed; shared OperatorGroup is retained. |
| V1.6–V1.8, V2.10 | Batch and argument behavior | Four-section ordering; only Eligible converts; stop/continue behavior; a Subscription named `check` works. |
| V2.1–V2.9, V4.3–V4.5 | Eligibility matrix | One test per incompatibility, expected reason, no mutation on refusal, acknowledgment flips only soft cases. |
| V2.11 | Non-image source refusal (pending fixture E2E) | A non-image CatalogSource referenced by the Subscription or selected by an InstallPlan bundle lookup remains ineligible even if another ClusterCatalog serves the package; conversion leaves OLMv0 resources untouched. |
| V3.8–V3.11, V3.13–V3.17, V3.19–V3.20 | Catalog migration | image, poll interval, priority, unsupported source, dedup/adoption, overflow, fresh-reference deletion checks, and image/name conflicts. |
| V3.12, V4.2, V4.6 | Collection and adoption | Deployment config survives an OLMv1 upgrade, shared CRD adoption succeeds, and large payload uses Secrets. |
| V5.1–V5.9 | Real-operator smoke | Bootstrap, install a pinned AllNamespaces operator, catalog migration, conversion, upgrade, rollback, and four-state batch scan. |

## Test implementation and coverage

Use Go's `testing` package with `testify`-free polling helpers and `client-go` against the
kind kubeconfig. Tests are in `test/e2e/migration`, protected by the `e2e` build tag so `make migration/test-unit`
remains unit-only. The test process invokes the built CLIs for command behavior; it uses
the Kubernetes client only for setup and assertions. Every wait has a bounded timeout and
prints current objects/events on timeout.

Live setup waits for the source InstallPlan to reach `Complete` before checking CSV health.
`AtLatestKnown` alone does not prove installation has finished. The live namespace-deletion
scenario also enforces this prerequisite when invoked directly, and failure diagnostics include
both OLMv0 controller logs to identify installations racing the cutover.

The operator CLI routes single conversion through `Migrator.Migrate`, preview through
`Check` and `Gather`, and rollback/cleanup through `Rollback` and `Cleanup`. Fixture
refusal tests exercise the public check and preview paths before any mutation; live
tests exercise the same public conversion and recovery paths against OLMv0.
`Migrator.Progress` emits typed step/status events to the CLI, including warnings
and failures; consumers should not parse message text to infer progress state.
The operator CLI defaults to human-readable output. Use `--output=jsonl` on
`check`, `convert` (including `--dry-run`), `rollback`, or `cleanup` to stream
one JSON object per line. Records have a `type` (`progress`, `scan`, `check`,
`dry-run`, `result`, or `error`); progress records include `step`, `status`,
`command`, and, for single-operator work, `target`.
For example, `migrate-operators-v0-to-v1 convert my-operator -n operators
--dry-run --output=jsonl` emits a structured preview without raw resource data.
The catalog CLI also accepts `--output=jsonl`; its records use the same progress,
result, and terminal-error envelope. Each catalog result includes its per-source
outcome (`created`, `adopted`, `skipped`, `error`, or `dry-run`), reason, and notes.
Library callers receive typed progress through `CatalogMigrator.Progress` and can
use `migration/pkg/clioutput` for text or JSONL rendering.
Both CLIs fail on text or JSONL progress-write errors. Operator unit tests cover
the first structured error, text-write failure, and a failed batch result rather
than a success result; catalog unit tests cover text and JSONL failures even when
the result set is empty. These output tests do not need a cluster.

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

The live matrix's `TestMigration` verifies conflict cleanup and rollback. Cleanup retains
operator Deployments, CRDs, OperatorGroups, and the ClusterExtension, while orphan-deleting
the conflicting primary CSV and removing its OLMv0 artifacts. The injected conflict uses
manual approval to prevent an automatic install from racing cleanup. Its unapproved test-only
InstallPlan is removed before rollback; the original source InstallPlan is retained.
An unacknowledged rollback
preserves CE backup annotations and COS revisions; an acknowledged rollback must remove all
CE/COS management and restore the original Subscription spec, an `AtLatestKnown` Subscription,
a `Succeeded` CSV, and available Deployments within ten minutes. Unit tests additionally verify
orphan deletion policies, preservation of unrelated revisions, revision-list preflight failures,
and recovery creation/reconciliation failures without changing the backup. Conflict-cleanup
regressions cover CSV discovery without Subscription status, refusal to remove shared or
mismatched-package CSVs, and propagation of discovery/deletion errors. Failure artifacts
include OLMv0 Subscriptions, CSVs, InstallPlans, OperatorGroups, and OperatorConditions, and
rollback timeouts identify the resource and reconciliation state still blocking recovery.

After `make migration/e2e-fixture-setup`, run `make migration/test-e2e-batch`
to exercise four-state `check --all`, non-mutating `convert --all --dry-run`, and
conversion of Eligible operators only (V1.6/V5.9). The target replays a fresh
ecr-secret fixture and collects CLI coverage in `coverage/fixture/batch`; the fixture
CI job runs it and includes its data in combined coverage. Unit tests inject
multiple conversion/preview failures to verify stop-on-error, continuation, and
nonzero results after partial failure (V1.7). Unit tests also verify that batch
classification applies acknowledgment flags and rejects `-n` or `--ce-name` with `--all`.

After `make migration/e2e-fixture-setup`, run
`make migration/test-e2e-acknowledgments` for V2.1–V2.10, including V2.1a. Each of thirteen cases
replays the ecr-secret snapshot independently. Soft checks cover scoped target
namespaces, a namespace selector, OperatorCondition usage, OLMv0 API access,
a scoped ServiceAccount, Subscription state, and CSV phase. Each refuses
conversion without the matching flag (including an unrelated-flag attempt),
accepts a non-mutating preview with the correct flag, then converts and verifies
the CE's audit annotation. Dependency properties, APIServices, generated dependency
Subscriptions, missing catalog packages, and CSVs without AllNamespaces support remain blocked even with all soft
acknowledgments enabled. Refusals preserve source Subscription/CSV/OperatorGroup
identity and content and create no CE/COS. The fixture CI job collects each case's
instrumented CLI coverage under `coverage/fixture/acknowledgments`, automatically
included by `make migration/report-coverage-all`.
Dry-run now runs readiness, compatibility, and catalog resolution rather than
previewing an operator that actual conversion would reject.

After `make migration/e2e-fixture-setup`, run `make migration/test-e2e-backup`
for V3.18. Three independently replayed ecr-secret cases verify that dry-run
does not write files; successful conversion creates the requested directory
and preserves source GVK, identity, spec, and status in Subscription,
OperatorGroup, CSV, and associated InstallPlan YAML; and a deterministic disk
write failure warns but still installs the CE with authoritative backup annotations.
An impersonated read-only user can inspect resources and port-forward catalogd,
but cannot delete the Subscription: the CLI reaches that denial only after all
backup files exist, with source resources and OLMv1 management unchanged.
An extra associated plan is saved; an unrelated plan is excluded. Unit tests
cover each filesystem failure, private file permissions, optional resources,
exact manifest GVKs, snapshot isolation, informational plan-list failures, and
same-name plans from different namespaces. Fixture CI collects
CLI coverage under `coverage/fixture/backup`, included in combined reporting.

1. The `migration-test` workflow runs unit coverage, fixture E2E, live-operator E2E, COS
   supersession E2E, and the in-cluster Job E2E independently. Unit, fixture, and live tests
   collect CLI coverage; COS supersession collects direct `migration/...` package coverage. A
   final job merges all of those profiles and displays the total report.
2. Run all jobs for pull requests, merge queues, and pushes to `main`. The fixture,
   live-operator, COS-supersession, and in-cluster Job suites are required merge gates.
   Preserve diagnostics and coverage artifacts for every coverage-producing suite; the focused
   in-cluster Job check uploads neither.
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
make migration/test-e2e-live-namespace-delete
make migration/e2e-teardown
```

### Fixture tests

This suite uses a separate cluster containing OLMv1 and OLMv0 CRDs, but no OLMv0
controllers. It replays the committed OLMv0 snapshots.

```bash
make migration/e2e-fixture-setup
make migration/test-e2e-fixture-matrix
make migration/test-e2e-batch
make migration/test-e2e-acknowledgments
make migration/test-e2e-backup
make migration/test-e2e-cross-namespace
make migration/test-e2e-system-managed-namespace
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

### In-cluster Job test

This focused test is independent of coverage collection. It provisions the fixture cluster,
loads a locally built, uninstrumented image into Kind, then runs catalog and operator migration
from a Job using projected ServiceAccount credentials (with a test-only `cluster-admin` binding).
It intentionally retains the Job namespace for log inspection; the next invocation removes it
before creating a fresh Job.

```bash
make migration/test-e2e-in-cluster-job
E2E_CLUSTER_NAME=library-olm-fixture-e2e make migration/e2e-teardown
```
