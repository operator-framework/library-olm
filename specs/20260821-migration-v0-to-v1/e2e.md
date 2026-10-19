# E2E Test Strategy — OLMv0 → OLMv1 Migration

This document tracks the implemented portion of the Phase 8 E2E plan from
[validation.md](validation.md). It uses deterministic fixture scenarios for migration and
rejection paths, real-operator smoke scenarios, COS supersession, and an in-cluster Job that
verifies ServiceAccount authentication.

The suites install experimental OLMv1 because migration requires its COS API. The
system-managed namespace scenario uses the same profile, so standard-profile availability is
not a migration gap. Dependency and APIService fixtures verify intentional compatibility
refusals, not features this library is expected to implement.

## Test environments

| Suite | Cluster | Catalog content | Purpose |
|---|---|---|---|
| `fixture` | kind + OLMv1 + OLMv0 CRDs only (no OLMv0 controllers) | Committed OLMv0-install snapshots and a locally built FBC served by an in-cluster TLS registry | Deterministic migration and rejection coverage without a remote catalog. |
| `real-operator` | separate kind cluster with OLMv0 + OLMv1 | OLMv0's installed OperatorHub catalog | Covers V5.2–V5.6 plus cleanup, healthy rollback, and remigration (V5.8). V5.7 upgrade remains a gap. |
| `in-cluster-job` | fixture cluster | A replayed ecr-secret-operator installation | Proves both CLIs authenticate and migrate using only a Pod ServiceAccount. |

Do not use a mutable public `latest` image or an unpinned release installer in CI. The fixture
registry's `latest` tag is private to the disposable cluster and rebuilt from committed inputs on
every setup. The default bootstrap pins OLMv0 to `v0.46.0`, OLMv1 to `v1.12.0`, and kind to
`v0.33.0` with the digest-pinned Kubernetes `v1.36.1` node image; CI should
mirror those release artifacts and override the URLs when it cannot access GitHub. The job inputs
are the kind node image, OLMv0 manifest URL and digest, operator-controller release
manifest URL and digest, and (only for the smoke suite) the catalog image/package/channel/CSV
version. The fixture catalog is constructed from committed snapshots on every fixture-cluster
setup, so it does not rely on a retained Quay digest or a mutable external catalog tag.

## Bootstrap

1. For the live suite, run `make migration/e2e-setup` to create (or reuse) an isolated kind cluster named `library-olm-e2e` using
   [`test/e2e/migration/kind-config.yaml`](../../test/e2e/migration/kind-config.yaml), exporting an explicit
   Kind-generated kubeconfig at `.kubeconfig/library-olm-e2e`. `E2E_KUBECONFIG` is only
   an optional override for that generated output path.
2. Live only: apply the pinned OLMv0 quickstart manifest and wait for both `olm-operator` and
   `catalog-operator` deployments to be Available.
3. Live only: install the pinned experimental operator-controller release and wait for its deployment,
   catalogd, cert provider, and required CRDs to be established.
4. For the fixture suite, run `make migration/e2e-fixture-setup` to create (or reuse) the separate
   `library-olm-fixture-e2e` cluster and generate `.kubeconfig/library-olm-fixture-e2e`. This
   path applies OLMv0 CRDs without installing OLMv0 controllers, installs the pinned experimental
   operator-controller release, builds bundle and FBC images from committed snapshots, publishes
   them over TLS to the in-cluster fixture registry, and applies the fixture `CatalogSource`.
   The registry certificate is issued by OLMv1's `olmv1-ca`; publishing uses the CA and an HTTPS
   port-forward, not an insecure HTTP registry connection.
5. The fixture test migrates the `CatalogSource` with `migrate-catalogs-v0-to-v1` and waits for
   the resulting `ClusterCatalog` to become `Serving=True`.
6. Before each fixture scenario, reset the relevant migration resources and replay the
   selected snapshot into its named namespace. On failure, collect namespaced resources and
   events, cluster-scoped migration objects, and available controller logs under
   `artifacts/e2e`.

The upstream projects currently provide a checked-in OLMv0 quickstart manifest and an
operator-controller kind/E2E installation flow. This repository owns version pinning and
the combined-cluster compatibility check rather than copying either project's mutable
installer. See the [OLMv0 quickstart manifest](https://github.com/operator-framework/operator-lifecycle-manager/blob/master/deploy/upstream/quickstart/olm.yaml)
and [operator-controller E2E guidance](https://github.com/operator-framework/operator-controller/blob/main/AGENTS.md).

## Fixtures

The committed fixture inputs capture steady-state OLMv0 installs for three real packages:
`ecr-secret-operator`, `external-secrets-operator`, and `redis-operator`. Each snapshot has its
OLMv0 resources, namespaced resources, CRDs, and any preinstall ServiceAccount needed for replay.
Fixture setup turns those inputs into bundle images and an FBC image using `crane`, then publishes
them to a TLS registry in `migration-e2e-registry`. The in-cluster `CatalogSource` points at that
registry; fixture replay never installs an OLMv0 controller and never pulls an OperatorHub catalog
from Quay.

This is deliberately a snapshot test, not a synthetic operator catalog. Unit tests cover many
individual compatibility and recovery boundaries. Fixture E2E now also runs a four-state batch,
thirteen acknowledgment/refusal cases, and explicit cross-namespace migration alongside the
three baseline snapshot conversions. Catalog edge cases and large-payload/shared-resource E2E
scenarios remain in the target matrix below; PRs #64 and #70 added catalog-edge and large-bundle
unit coverage, respectively.

## Scenario matrix

This is the target matrix derived from `validation.md`, not a claim that every row is implemented.
The currently implemented coverage is described in the Fixtures and CI sections.

| Validation IDs | Scenario | Required assertions |
|---|---|---|
| V1.1, V1.2, V1.3, V3.1–V3.7 | Baseline fixture migration | `check`, dry-run, COS `Succeeded=True` before CE creation, CE `Installed=True`, field mappings, CE backups/audit, Sub/CSV cleanup. |
| V4.7 | Cross-namespace fixture migration | Target namespace is created with copied PSA/SCC labels; source Deployments are scaled to zero before target creation; CE and rendered Deployment use the target; collected source Deployment is removed while the source namespace remains. |
| V4.7 | Acknowledged live namespace deletion | A real OLMv0 installation is converted with `--acknowledge-namespace-delete`; after CE installation in the target namespace, OLMv0 finalizes the CSV and the source namespace is deleted. |
| V4.8 | System-managed namespace fixture migration | Against the experimental controller profile, verify the explicit opt-in omits `spec.namespace`, migration imports the prepared bundle-metadata namespace into its COS, the catalog COS takes ownership, OLMv1 manages the target Deployment there, and source copies are removed. Unit tests verify unsupported CRDs reject the mode before mutation. |
| V1.4, V3.18 | Rollback and remigration | Refusal without acknowledgment for installed CE; CE/COS deletion and Subscription restoration with acknowledgment; preserved Deployments lose OLMv1 ownership metadata, retain their UIDs, and are adopted by new COS revisions on a second conversion; on-disk backup files exist before deletion. |
| V1.5, V4.1 | Conflict cleanup | CE stays; Subscription and OLMv0 artifacts are removed; shared OperatorGroup is retained. |
| V1.6–V1.8, V2.10 | Batch and argument behavior | Four-section ordering; only Eligible converts; stop/continue behavior; a Subscription named `check` works. |
| V2.1–V2.9, V4.3–V4.5 | Eligibility matrix | One test per incompatibility, expected reason, no mutation on refusal, acknowledgment flips only soft cases. |
| V2.11 | Non-image source refusal | A non-image CatalogSource referenced by the Subscription or selected by an InstallPlan bundle lookup remains ineligible even if another ClusterCatalog serves the package; conversion leaves OLMv0 resources untouched. |
| V3.8–V3.11, V3.13–V3.17, V3.19–V3.20 | Catalog migration | image, poll interval, priority, unsupported source, dedup/adoption, overflow, fresh-reference deletion checks, and image/name conflicts. |
| V3.12, V4.2, V4.6 | Collection and adoption | Deployment config survives an OLMv1 upgrade, shared CRD adoption succeeds, and large payload uses Secrets. |
| V5.1–V5.9 | Real-operator smoke | Bootstrap, install a pinned AllNamespaces operator, catalog migration, conversion, upgrade, rollback, and four-state batch scan. |

PR #74 added fixture acceptance cases for V2.11, dependent warnings (V4.4), command-named
Subscriptions (V1.8), and DeploymentConfig mapping (V3.12), plus a live four-state batch with
two eligible operators (V5.9). The fixture and live CI jobs run these cases.

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
--dry-run --output=jsonl` emits a structured preview without raw resource data,
followed by a completion result record.
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
migration/test-e2e-live-matrix` runs only the real-operator smoke scenarios. Both use the
Kind-generated kubeconfig by default and require their cluster to have been bootstrapped first.
Both commands write diagnostics below `E2E_ARTIFACTS` (default: `artifacts/e2e`).

The migration binaries run outside the Go test process, so E2E coverage is collected separately.
E2E CLI binaries are built with `go build -cover`, and coverage-producing E2E targets write data
under `artifacts/e2e/coverage`. `make migration/test-unit` writes the unit profile under
`artifacts/coverage`; after the E2E suites have run, `make migration/report-coverage-all` merges
their profiles and displays the combined result. `make migration/test-coverage-all` reruns unit
tests before displaying that report. The in-cluster Job is intentionally uninstrumented and does
not contribute coverage. The ≥80% target measures `migration/pkg/...` statements across the
combined unit and coverage-producing E2E profiles, with no separate per-suite minimum. This
combined threshold is not yet enforced by the Make target or CI.

## CI status

The `migration-test` workflow runs unit coverage, fixture E2E, live-operator E2E, the
in-cluster Job E2E, and the focused COS-supersession E2E as independent jobs. Unit, fixture,
live, and COS-supersession tests publish coverage inputs; a dependent coverage job displays
their combined report. The Job intentionally does not upload diagnostics or coverage artifacts.

The supersession check is deliberately separate from the conversion matrix. With the released
operator-controller v1.12.0, migration creates revision 1 with collision protection `None`;
creating the ClusterExtension results in a controller-owned, catalog-derived revision 2 with
collision protection `Prevent`. The check requires both revisions to succeed.

## CI rollout

The live matrix's `TestMigration` verifies conflict cleanup and rollback. Cleanup retains
operator Deployments, CRDs, OperatorGroups, and the ClusterExtension, while orphan-deleting
the conflicting primary CSV and removing its OLMv0 artifacts. The injected conflict uses
manual approval to prevent an automatic install from racing cleanup. Its unapproved test-only
InstallPlan is removed before rollback; the original source InstallPlan is retained.
An unacknowledged rollback
preserves CE backup annotations and COS revisions; an acknowledged rollback starts CE orphan deletion while retaining its backup with a rollback finalizer, then must remove all
CE/COS management, including revision annotations on preserved workloads, and restore the original Subscription spec before releasing that finalizer. It then waits for an `AtLatestKnown` Subscription,
a `Succeeded` CSV, and available Deployments within ten minutes. The test then runs `convert` again, waits for the new CE to install, and checks that the same Deployment UIDs have revision annotations and controller references to new COS revisions. Unit tests additionally verify
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

`make migration/test-e2e-live-batch` repeats the four-state selection check with
two operators installed by OLMv0 and three isolated state markers. It asserts
that both eligible operators are selected and converted. It has its own
CLI coverage directory and runs in the live CI job.
The live batch and fixture acceptance targets verify that their kubeconfig
identifies the corresponding Kind cluster before deleting any test resources.

`make migration/test-e2e-acceptance` replays the ecr-secret snapshot separately for
four focused cases: non-image CatalogSource refusal before mutation (V2.11),
dependent-operator warnings during check, dry-run, and conversion (V4.4),
conversion of a Subscription named like a CLI command (V1.8), and full
deploymentConfig mapping into both the ClusterExtension and operator Deployment
(V3.12, except post-upgrade persistence). Each case contributes CLI coverage.

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
make migration/test-e2e-live-batch
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
make migration/test-e2e-acceptance
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
git commit --signoff -m 'test: refresh OLMv0 operator snapshots'
make migration/e2e-teardown
```

For a single package, replace `all` with its name from `test/e2e/migration/operators.tsv`, for example
`make migration/e2e-install-v0 E2E_OPERATOR=redis-operator`. Snapshot capture must occur while the
operator remains OLMv0-managed. Fixture CI never captures snapshots, so it can run
independently and in parallel with the live suite.

### In-cluster Job test

This focused test is independent of coverage collection. It provisions (or reuses) the fixture
cluster, builds and loads a locally built, uninstrumented image into Kind, then runs catalog and operator migration
from a Job using projected ServiceAccount credentials (with a test-only `cluster-admin` binding).
It intentionally retains the Job namespace for log inspection; the next invocation removes it
before creating a fresh Job.

```bash
make migration/test-e2e-in-cluster-job
E2E_CLUSTER_NAME=library-olm-fixture-e2e make migration/e2e-teardown
```
