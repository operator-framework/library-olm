# IEEE 829 Test Plan — OLMv0 → OLMv1 Migration

## 1. Test Plan Identifier

**Identifier:** `OPRUN-4715-TP-001`  
**Feature:** [OPRUN-4715 — OLMv0 → OLMv1 Migration library & CLIs (prototype)](https://redhat.atlassian.net/browse/OPRUN-4715)  
**Version:** 1.0  
**Date:** 2026-09-18  
**Status:** Active; Phase 8 test implementation is in progress.

This plan follows the IEEE 829 test-plan structure. It is the executable-test companion to
[requirements.md](requirements.md), [validation.md](validation.md),
[e2e.md](e2e.md), and [status.md](status.md).

## 2. Introduction

OPRUN-4715 delivers a prototype Go library and two example CLIs that migrate OLMv0-managed
operators and catalogs to OLMv1 management. The test objective is to demonstrate that the
library and CLIs safely classify, migrate, recover, and refuse unsupported OLMv0 state while
preserving workloads and enabling OLMv1 adoption.

Testing must establish the feature's acceptance criteria: automated CI, the public library API,
four-state classification with reasons, non-interactive operator and catalog CLIs, required
resource/field mappings, migration recovery, and published design documentation. The plan uses
fast fake-client unit tests, deterministic kind fixture E2E tests, and live-operator kind smoke
tests. A focused in-cluster Job test verifies that the CLIs can also run using Pod credentials.

## 3. Test Items

| Item | Location / interface | Test level |
|---|---|---|
| Operator migration library | `migration/pkg/migration` | Unit, fixture E2E, live E2E |
| Catalog migration library | `migration/pkg/catalogmigration` | Unit, fixture E2E, live E2E |
| Operator migration CLI | `migration/examples/cmd/migrate-operators-v0-to-v1` | Fixture, live, in-cluster Job E2E |
| Catalog migration CLI | `migration/examples/cmd/migrate-catalogs-v0-to-v1` | Fixture, live, in-cluster Job E2E |
| Fixture data and local FBC | `test/e2e/migration/fixtures` | Fixture E2E |
| E2E harness and setup scripts | `test/e2e/migration`, `hack/e2e/migration`, `migration.mk` | E2E infrastructure |
| CI workflow and coverage aggregation | `.github/workflows/migration-test.yaml`, Make targets | CI / non-functional |

## 4. Features to Be Tested

### 4.1 Library and CLI behavior

- `ScanAll`, `Check`, `Gather`, `Migrate`, `Rollback`, `Cleanup`, and catalog-migration APIs.
- Operator CLI verbs `check`, `convert`, `rollback`, and `cleanup`, including single-target and
  `--all` behavior, dry-run, backup, acknowledgement, and failure-continuation flags.
- Catalog CLI image CatalogSource conversion, dry-run, deduplication/adoption, and safe source
  deletion rules.
- Non-interactive operation and positional operator names that match a command verb.

### 4.2 Classification, compatibility, and safety

- The `Eligible`, `Ineligible`, `AlreadyMigrated`, and `Conflict` states and their reasons.
- Hard rejection of dependencies, APIService definitions, absent catalog packages, and OLMv0
  generated dependencies.
- Soft compatibility checks C1, C4, C5, C6, and C8; each acknowledgement must make only the
  intended soft case eligible and leave an audit annotation on the ClusterExtension.
- Refusal-before-mutation: an unsafe or unresolved input must create neither a
  `ClusterExtension` nor a `ClusterObjectSet`.

### 4.3 Migration, recovery, and mappings

- COS creation and `Succeeded=True` before ClusterExtension creation; no client-written OLMv1
  status; `IfNoController` collision protection and CRD adoption.
- Subscription, OperatorGroup, ClusterExtension, CatalogSource, and ClusterCatalog field mappings
  defined in requirements R4, R6, R7, and R8.
- Backup annotations and optional on-disk backup; rollback acknowledgement and restoration;
  Conflict cleanup while retaining the ClusterExtension and shared OperatorGroup where required.
- Resource collection, deduplication, shared CRD adoption, and SecretPacker behavior for a large
  bundle.

### 4.4 End-to-end environments

- Side-by-side OLMv0 and OLMv1 bootstrap on Kubernetes-in-kind.
- Deterministic replay of committed snapshots for `ecr-secret-operator`,
  `external-secrets-operator`, and `redis-operator`, using an in-cluster TLS fixture registry and
  locally built file-based catalog.
- Live installation through OLMv0, CatalogSource-to-ClusterCatalog migration, OLMv1 conversion,
  health/adoption checks, upgrade, and rollback for the selected real-operator matrix.
- Migration invoked from a ServiceAccount-authenticated Job inside the fixture cluster.

### 4.5 Traceability and coverage

`validation.md` is the detailed acceptance-test inventory. The following is the plan-level
allocation; an item is not considered complete merely because it is allocated here.

| Validation coverage | Primary test type | Current implementation status |
|---|---|---|
| V1.1–V1.3, V3.1–V3.7 | Unit plus fixture/live E2E | Baseline check, dry-run, conversion, and catalog path implemented; full mapping assertions remain. |
| V1.4–V1.5, V3.18 | Unit plus fixture E2E | Unit/recovery coverage in progress; complete rollback, cleanup, and backup-directory E2E assertions remain. |
| V1.6–V1.8 | Unit and CLI tests | Batch ordering, stop/continue, and command-name edge case remain to be completed. |
| V2.1–V2.10, V4.3–V4.5 | Focused unit tests; fixture refusal tests | Non-steady-state and unresolved-package refusal are covered in fixture E2E; complete acknowledgement and four-state matrix remains. |
| V3.8–V3.11, V3.13–V3.17, V3.19 | Catalog unit tests plus fixture/live E2E | Basic CatalogSource migration covered; edge, adoption, overflow, and deletion-reference cases remain. |
| V3.12, V4.1–V4.2, V4.6 | Unit plus live E2E | Planned; requires deployment upgrade, shared-resource, and large-payload scenarios. |
| V5.1–V5.9 | Live kind E2E | Bootstrap, real installation, catalog conversion, check, and conversion are covered; upgrade, rollback, and four-state batch scenario remain. |
| V4.7–V4.8 | Fixture and live E2E plus unit tests | Explicit cross-namespace and acknowledged source-namespace deletion are covered. System-managed namespace mode is covered against the experimental controller CRD, including omitted CE namespace and unsupported-CRD rejection. |
| V6.* | Product / downstream qualification | Not a Kind CI gate; topology, architecture, and restricted-network coverage require separate environments. |

## 5. Features Not to Be Tested

- APIService-based operator migration is excluded while C3 remains a hard block; it is revisited
  only after OPRUN-4723 has complete operator-controller renderer support.
- Hosted Control Planes are out of scope. SNO, compact, multi-node, non-x86 architectures, and
  restricted/disconnected network qualification are downstream/product-environment work, not
  kind CI coverage.
- Production packaging, `oc` plugin behavior, and delivery certification are outside the example
  CLI prototype scope.

## 6. Approach

### 6.1 Unit tests

Run `make migration/test-unit`. Tests use the controller-runtime fake client for deterministic
library behavior and collect a Go coverage profile for `./migration/...`. They must cover normal
paths and boundary/error paths without requiring Kind, a kubeconfig, controllers, or external
catalog access. This target is independent of all E2E setup.

### 6.2 Fixture E2E tests

Run `make migration/e2e-fixture-setup` followed by
`make migration/test-e2e-fixture-matrix`, `make migration/test-e2e-cross-namespace`, and
`make migration/test-e2e-system-managed-namespace`, then tear down the fixture cluster. The fixture
environment has Kind, OLMv1, OLMv0 CRDs, and **no OLMv0 controllers**. It replays committed,
sanitized OLMv0 installs and builds a local FBC from them. The TLS registry certificate comes
from OLMv1's `olmv1-ca`; fixture tests therefore do not depend on a mutable Quay image/digest or
the live OperatorHub catalog.

Use fixture tests for reproducible conversion, field-mapping, refusal-before-mutation, catalog
edge cases, rollback/cleanup, and command-output assertions. Each scenario receives an isolated
namespace, bounded waits, and failure collection of relevant objects, events, and controller
logs.

### 6.3 Live-operator E2E tests

Run `make migration/e2e-setup`, `make migration/test-e2e-live-matrix`, then
`make migration/e2e-teardown`. This uses a separate Kind cluster with OLMv0 and OLMv1 installed.
The suite installs packages through OLMv0, creates/migrates a CatalogSource, invokes the CLIs,
and verifies actual controller reconciliation and adoption. It is a smoke/integration layer, not
a substitute for exhaustive fixture cases. Pin the Kind, Kubernetes node, OLMv0, and OLMv1
versions documented in `e2e.md`.

### 6.4 In-cluster Job test

Run `make migration/test-e2e-in-cluster-job` against the fixture environment. Build and load an
uninstrumented CLI image into Kind and execute both CLIs from a Job using a projected
ServiceAccount token. Retain the Job namespace for diagnosis; this focused authentication test
does not upload coverage or diagnostic artifacts.

### 6.5 Coverage and CI

Build E2E CLIs with `go build -cover`. Fixture and live suites collect CLI coverage under
`artifacts/e2e/coverage`; unit tests write their profile under `artifacts/coverage`. After all
coverage-producing suites, run `make migration/report-coverage-all` to merge and display a
single report. The in-cluster Job is deliberately excluded from coverage collection.

CI runs unit coverage, fixture E2E, and live E2E independently, retains their diagnostics and
coverage inputs, and then runs a dependent aggregate-report job. The Job E2E is an independent
functional check. The unit suite owns the minimum 80% `migration/pkg/...` gate; E2E coverage is
reported for visibility rather than used as a percentage gate. Provisioning or image-pull errors
may be retried once, but assertion failures must not be retried automatically.

## 7. Item Pass/Fail Criteria

- A test item passes only when all its mapped `validation.md` assertions pass and no unexpected
  cluster mutation occurs.
- A migration passes only when its COS reaches `Succeeded=True` before the CE is created, the CE
  reaches `Installed=True`, expected source cleanup occurs, and workloads/resources required to
  remain are preserved or adopted.
- A refusal test passes only on non-zero/refusal behavior with the expected reason and no CE/COS
  creation or unintended deletion.
- A rollback/cleanup test passes only with its stated acknowledgement behavior, expected retained
  resources, and restored Subscription where applicable.
- A catalog test passes only when the resulting ClusterCatalog is `Serving=True`, its mapping and
  adoption/deduplication behavior match V3, and deletion conditions are honored.
- A CI run passes only when required jobs pass, the coverage report is generated from available
  unit/fixture/live data, and the unit coverage threshold is met.

## 8. Suspension Criteria and Resumption Requirements

Suspend an E2E suite when Kind cannot be created, pinned manifests/images cannot be obtained,
OLMv0 or OLMv1 required CRDs/controllers do not become ready within the configured timeout, or
the fixture registry/catalog cannot serve its content. Preserve artifacts and cluster state long
enough to diagnose the failure, then tear down the known cluster explicitly.

Resume after the failed prerequisite is corrected and a clean, correctly version-pinned cluster
has been created. Do not mask functional failures by re-running assertions. Phase-6 and
APIService test work resumes only after their documented upstream dependencies are available.

## 9. Test Deliverables

- This test plan and the requirement/validation traceability documents.
- Go unit tests, E2E tests, fixture snapshots, Kind configuration, setup/teardown/build scripts,
  and Make targets.
- CI workflow results, unit and merged coverage reports, and failure diagnostics.
- On failure: relevant resource YAML, events, migration-object state, and controller/pod logs
  under `artifacts/e2e`.
- A status update in `status.md` identifying merged versus pending test work and remaining
  validation gaps.

## 10. Testing Tasks

1. Maintain version-pinned Kind/OLMv0/OLMv1 bootstrap and validate readiness.
2. Maintain sanitized, committed fixture snapshots and locally build the fixture FBC on setup.
3. Implement and run unit tests for every library branch, acknowledgement, recovery ownership
   decision, and catalog mapping boundary.
4. Implement fixture E2E cases for every remaining V1–V4 validation row.
5. Maintain live tests for representative real operators and controller-adoption behavior.
6. Run the in-cluster Job test to protect the in-cluster authentication path.
7. Publish/inspect combined coverage and prioritize unexecuted migration and catalog paths.
8. Triage failures as product defects, fixture drift, environment/provisioning failures, or test
   defects; add a regression test for every confirmed product defect.

## 11. Environmental Needs

| Need | Requirement |
|---|---|
| Local / CI host | Go toolchain and Bingo-managed Kind; container runtime available to Kind. |
| Kubernetes | Kind Kubernetes `v1.36.1` node image unless the test run explicitly validates another supported version. |
| OLM components | Pinned OLMv0 `v0.46.0` and OLMv1 `v1.12.0` manifests, or CI-approved overrides of those pinned inputs. |
| Kubeconfig | Kind-generated `.kubeconfig/library-olm-e2e` by default; `E2E_KUBECONFIG` only overrides that output path. |
| Fixture environment | Separate `library-olm-fixture-e2e` cluster; OLMv0 CRDs only, OLMv1 controllers, local TLS registry/FBC, committed snapshots. |
| Live environment | Separate `library-olm-e2e` cluster with both OLMv0 and OLMv1 controllers and OLMv0 catalog access. |
| Artifacts | Writable `artifacts/coverage` and `artifacts/e2e/coverage` directories; CI artifact retention. |

## 12. Responsibilities

| Role | Responsibility |
|---|---|
| Feature owner | Prioritize validation gaps, approve scope/acceptance decisions, and maintain Jira status. |
| Migration-library maintainers | Implement fixes, unit tests, and regression coverage. |
| E2E/CI maintainers | Maintain fixtures, bootstrap scripts, workflow reliability, artifacts, and coverage aggregation. |
| Reviewers | Verify test-to-requirement traceability, safety assertions, fixture sanitization, and no false coverage claims. |
| Operator-controller maintainers | Resolve upstream Phase 6/APIService prerequisites and provide compatibility guidance. |

## 13. Staffing and Training Needs

Contributors need familiarity with Kubernetes resources and controller reconciliation, OLMv0
Subscription/CatalogSource behavior, OLMv1 ClusterExtension/ClusterObjectSet/ClusterCatalog
behavior, Kind lifecycle, and Go coverage. Reviewers of fixture changes must understand that
snapshots are committed test inputs and may not contain credentials or cluster-specific secrets.

## 14. Schedule

Testing follows implementation milestones rather than fixed dates:

1. Keep the merged unit, fixture, and live foundations passing on every change.
2. Merge pending Phase 8 negative/recovery, in-cluster Job, documentation, and coverage work.
3. Close the remaining V1–V5 rows in priority order: safety/rejection and recovery first,
   catalog edge cases and mapping next, then upgrade/large-payload/shared-resource scenarios.
4. Revisit deferred V4.7 and APIService scenarios when their upstream work lands.
5. Declare Phase 8 complete only after its coverage target and applicable validation inventory
   pass in CI.

## 15. Risks and Contingencies

| Risk | Impact | Mitigation |
|---|---|---|
| Mutable remote catalogs or removed image digests | Non-reproducible fixtures and CI failures | Use committed snapshots and a locally built TLS FBC; pin bootstrap inputs. |
| Kind/controller startup or image-pull flakiness | E2E delay or false failure | Bounded readiness checks, diagnostics, isolated clusters, one provisioning retry. |
| Real operator external dependencies | Live smoke test instability | Select self-contained operators; move deterministic semantics to fixtures. |
| Fixture drift from a real OLMv0 install | False confidence | Deliberately refresh, sanitize, review, and commit snapshots only from a healthy live installation. |
| Unsafe migration cleanup | Workload/resource loss | Require refusal-before-mutation, backup/recovery tests, orphan-cascade assertions, and regression tests. |
| Upstream OLMv1 dependencies | Validation scope blocked | Keep C3 and install-namespace scenarios explicitly deferred; track OPRUN-4723 and Phase 6 prerequisites. |
| Coverage-only confidence | Untested controller behavior | Require both unit threshold and fixture/live functional gates; report merged E2E coverage separately. |

## 16. Approvals

| Approval role | Name | Date / decision |
|---|---|---|
| Feature owner | TBD | TBD |
| Migration-library maintainer | TBD | TBD |
| QE / test owner | TBD | TBD |
| Operator-controller dependency owner (as needed) | TBD | TBD |
