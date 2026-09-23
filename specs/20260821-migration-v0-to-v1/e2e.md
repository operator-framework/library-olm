# E2E Test Strategy — OLMv0 → OLMv1 Migration

This document tracks the implemented portion of the Phase 8 E2E plan from
[validation.md](validation.md). It uses deterministic fixture scenarios for migration and
rejection paths, real-operator smoke scenarios, COS supersession, and an in-cluster Job that
verifies ServiceAccount authentication.

## Test environments

| Suite | Cluster | Catalog content | Purpose |
|---|---|---|---|
| `fixture` | kind + OLMv1 + OLMv0 CRDs only (no OLMv0 controllers) | Committed OLMv0-install snapshots and a locally built FBC served by an in-cluster TLS registry | Deterministic migration and rejection coverage without a remote catalog. |
| `real-operator` | separate kind cluster with OLMv0 + OLMv1 | OLMv0's installed OperatorHub catalog | Covers the V5.2–V5.6 baseline with real deployed operators; V5.7 upgrade and V5.8 rollback remain gaps. |
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

1. Run `make migration/e2e-setup` to create (or reuse) an isolated kind cluster named `library-olm-e2e` using
   [`test/e2e/migration/kind-config.yaml`](../../test/e2e/migration/kind-config.yaml), exporting an explicit
   Kind-generated kubeconfig at `.kubeconfig/library-olm-e2e`. `E2E_KUBECONFIG` is only
   an optional override for that generated output path.
2. Apply the pinned OLMv0 quickstart manifest and wait for both `olm-operator` and
   `catalog-operator` deployments to be Available.
3. Install the pinned operator-controller release manifest and wait for its deployment,
   catalogd, cert provider, and required CRDs to be established.
4. For a fixture cluster, build bundle and FBC images from the committed snapshots, publish them
   over TLS to the in-cluster fixture registry, then apply the fixture `CatalogSource`. The
   registry certificate is issued by OLMv1's `olmv1-ca`; publishing uses the CA and an HTTPS
   port-forward, not an insecure HTTP registry connection.
5. The fixture test migrates the `CatalogSource` with `migrate-catalogs-v0-to-v1` and waits for
   the resulting `ClusterCatalog` to become `Serving=True`.
6. Before each scenario, create a new namespace and apply exactly one fixture set. After
   each scenario, collect all migration objects, CSV/Subscription/InstallPlan events, pod
   logs, and `kubectl get --all-namespaces -o yaml` for the test namespace on failure.

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

This is deliberately a snapshot test, not a synthetic fixture matrix. Unit tests cover many
individual compatibility and recovery boundaries; the fixture E2E suite currently verifies the
real captured shapes, catalog conversion, normal conversion, and two refusal-before-mutation
guards (a non-steady Subscription and an unresolvable package).

## Scenario matrix

This is the target matrix derived from `validation.md`, not a claim that every row is implemented.
The currently implemented coverage is described in the Fixtures and CI sections.

| Validation IDs | Scenario | Required assertions |
|---|---|---|
| V1.1, V1.2, V1.3, V3.1–V3.7 | Baseline fixture migration | `check`, dry-run, COS `Succeeded=True` before CE creation, CE `Installed=True`, field mappings, CE backups/audit, Sub/CSV cleanup. |
| V4.7 | Cross-namespace fixture migration | Target namespace is created with copied PSA/SCC labels; source Deployments are scaled to zero before target creation; CE and rendered Deployment use the target; collected source Deployment is removed while the source namespace remains. |
| V4.7 | Acknowledged live namespace deletion | A real OLMv0 installation is converted with `--acknowledge-namespace-delete`; after CE installation in the target namespace, OLMv0 finalizes the CSV and the source namespace is deleted. |
| V4.8 | System-managed namespace fixture migration | Against the experimental controller profile, verify the explicit opt-in omits `spec.namespace`, migration prepares the bundle-metadata namespace for COS ordering, OLMv1 manages the target Deployment there, and source copies are removed. Unit tests verify unsupported CRDs reject the mode before mutation. |
| V1.4, V3.18 | Rollback | Refusal without acknowledgment for installed CE; CE/COS deletion and Subscription restoration with acknowledgment; on-disk backup files exist before deletion. |
| V1.5, V4.1 | Conflict cleanup | CE stays; Subscription and OLMv0 artifacts are removed; shared OperatorGroup is retained. |
| V1.6–V1.8, V2.10 | Batch and argument behavior | Four-section ordering; only Eligible converts; stop/continue behavior; a Subscription named `check` works. |
| V2.1–V2.9, V4.3–V4.5 | Eligibility matrix | One test per incompatibility, expected reason, no mutation on refusal, acknowledgment flips only soft cases. |
| V3.8–V3.11, V3.13–V3.17, V3.19 | Catalog migration | image, poll interval, priority, unsupported source, dedup/adoption, overflow, and deletion-reference behavior. |
| V3.12, V4.2, V4.6 | Collection and adoption | Deployment config survives an OLMv1 upgrade, shared CRD adoption succeeds, and large payload uses Secrets. |
| V5.1–V5.9 | Real-operator smoke | Bootstrap, install a pinned AllNamespaces operator, catalog migration, conversion, upgrade, rollback, and four-state batch scan. |

## Test implementation and coverage

Use Go's `testing` package with `testify`-free polling helpers and `client-go` against the
kind kubeconfig. Tests are in `test/e2e/migration`, protected by the `e2e` build tag so `make migration/test-unit`
remains unit-only. The test process invokes the built CLIs for command behavior; it uses
the Kubernetes client only for setup and assertions. Every wait has a bounded timeout and
prints current objects/events on timeout.

`make migration/test-e2e-fixture-matrix` runs only deterministic fixture scenarios. `make
migration/test-e2e-live-matrix` runs only the real-operator smoke scenarios. Both use the
Kind-generated kubeconfig by default and require their cluster to have been bootstrapped first.
Both commands write diagnostics below `E2E_ARTIFACTS` (default: `artifacts/e2e`).

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
make migration/e2e-teardown
```

### Fixture tests

This suite uses a separate cluster containing OLMv1 and OLMv0 CRDs, but no OLMv0
controllers. It replays the committed OLMv0 snapshots.

```bash
make migration/e2e-fixture-setup
make migration/test-e2e-fixture-matrix
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
