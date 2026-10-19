# Implementation Status — OLMv0 → OLMv1 Migration

Last updated: 2026-10-16

---

## Summary

Phases 1–5 are fully implemented and merged into `main`. Phase 6's explicit install-namespace,
acknowledged source-namespace deletion, tests, and documentation are merged in PRs #46–#49,
with the source-Pod wait fix in #52 and experimental system-managed mode in #51. Phase 7
confirmed that APIService-owning operators remain unsupported in OLMv1; their refusal is
intentional, not a migration gap. Phase 8
has an automated testing foundation: unit coverage, deterministic fixture and live-operator
matrices, focused acknowledgment and batch tests, rollback/remigration coverage, an in-cluster
Job check, combined coverage reporting, and public-API/structured CLI output (#59).
Backup-directory E2E and non-image source rejection merged in PRs #58 and #61. Small validation
tests and AllNamespaces bundle-support validation merged in PRs #66–#67. Catalog edge
handling, shared catalog progress/output, and CLI usage-on-error behavior merged in PRs
#64, #65, #69, and #72. PR #70 added deployment-config and large-bundle unit coverage;
PR #73 added Kind demo scripts. PR #74 merged focused fixture acceptance cases and a live
four-state batch with two eligible operators. PR #76 releases preserved workloads from OLMv1
ownership during rollback and verifies that a second migration adopts them. The broader
validation inventory and coverage threshold remain open work.

---

## Phases

### Phase 1 — Repo bootstrap & prototype port [OPRUN-4717] ✅ DONE

Merged via stacked PRs #11–#16 on 2026-08-26.

- Module `github.com/operator-framework/library-olm` established
- GitHub Actions workflows (build, test, golangci-lint, go-apidiff)
- Prow config in `openshift/release` (tide, lgtm/approve, hold) — completed
- `migration/pkg/migration/` and `migration/examples/cmd/migrate-operators-v0-to-v1/` ported from perdasilva prototype
- Migration `ClusterObjectSet` (COS) revision with `CollisionProtection: None`; the
  controller subsequently creates its catalog-derived revision with `Prevent`
- SecretPacker for large bundles
- `olm.operatorframework.io/migrated-from-subscription: <ns>/<name>` annotation on COS and CE

### Phase 2 — Scan & classification [OPRUN-4718] ✅ DONE

Merged in PR #14.

- `OperatorStatus` enum: `Eligible` / `Ineligible` / `AlreadyMigrated` / `Conflict`
- `ScanAllSubscriptions` / `ScanSubscription` with four-state detection
- Conflict detected when both Subscription and annotated CE exist
- AlreadyMigrated detected when annotated CE exists but Subscription is gone
- Catalog availability check (C7) integrated into scan

### Phase 3 — Compatibility checks & acknowledgment framework [OPRUN-4719] ✅ DONE

Merged in PR #14. C3 restored to hard block in PR #22.

- C1 (AllNamespaces / namespace selector — soft, `AcknowledgeWatchScopeChange`)
- C2 (olm.package.required / olm.gvk.required — hard block)
- C3 (APIService definitions — **permanent hard block**; OLMv1 does not support them)
- C4 (OperatorCondition status entries — soft, `AcknowledgeOperatorCondition`)
- C5 (OLMv0-API RBAC without OLMv1 equivalent — soft, `AcknowledgeOLMv0APIAccess`)
- C6 (scoped ServiceAccount — soft, `AcknowledgeScopedServiceAccount`)
- C8 (not in steady state — soft, `AcknowledgeNotSteadyState`)
- C9 (OLMv0-managed dependency Subscription — hard block)
- C10 (Subscription and effective InstallPlan CatalogSources must be image-backed gRPC sources — hard block, PR #61)
- Per-flag audit annotations written to CE: `olm.operatorframework.io/acknowledged-<flag>`

### Phase 4 — Migration & recovery commands [OPRUN-4720] ✅ DONE

Merged in PR #16.

- `migrate-operators-v0-to-v1` example CLI under `migration/examples/cmd/`
- `check` verb: readiness + compatibility + four-state classification; `--all`
- `convert` verb: catalog resolution → backup Sub/OG specs to CE annotations → collect
  resources → create COS (wait `Succeeded=True`) → create CE → cleanup; `--dry-run`;
  `--backup <dir>`; `--all` with `--continue-on-error`
- `rollback` verb: start CE orphan deletion with a rollback finalizer before listing owned COS
  revisions, snapshot verified workloads, delete every revision, release preserved workloads
  from OLMv1 ownership, then restore the Subscription from its backup annotation;
  requires `--acknowledge-installed` when `Installed=True`
- `cleanup` verb: Conflict resolution — delete Subscription (orphan) + `CleanupOLMv0Resources`
- PR #59 routes single-operator CLI commands through the public `Migrator` APIs and adds
  typed progress events and `--output=jsonl`. JSONL dry-runs emit a preview and completion
  result; both text and structured output use the same command processing paths.
- PR #69 limits operator CLI usage output to argument errors; runtime migration failures
  report the error without displaying usage.

### Phase 5 — Catalog migration CLI [OPRUN-4722] ✅ DONE

Merged in PR #15.

- `migration/pkg/catalogmigration/` library
- `migrate-catalogs-v0-to-v1` example CLI under `migration/examples/cmd/`
- Lists CatalogSources; skips already-migrated (matching image); creates `ClusterCatalog`
  and waits `Serving=True`; reports per source; `--dry-run`
- Non-image sources (configmap/internal/address) reported as not migratable
- PR #61 added a hard refusal when either the Subscription's CatalogSource or the effective
  InstallPlan bundle-lookup source is missing or non-image, even if another ClusterCatalog
  serves the package.
- PR #64 hardened existing-catalog selection, conflicting names and images, priority overflow,
  and source-deletion checks. PR #65 unified catalog progress callbacks and text/JSONL output
  with operator migration; PR #72 limits catalog CLI usage output to argument errors.

### Phase 6 — Install-namespace change [OPRUN-4721] ✅ MERGED

PRs #46–#49 merged the explicit install-namespace flow, acknowledged source-namespace deletion,
cross-namespace coverage, and documentation. PR #52 merged the source-Deployment Pod-wait fix.
PR #51 merged the experimental system-managed mode.

- `--install-namespace` creates or updates the target namespace, copies PSA/SCC labels, moves
  collected resources, and removes source copies after cutover.
- `--acknowledge-namespace-delete` is the required destructive opt-in for deleting the source
  namespace after a cross-namespace migration.
- `--system-managed-install-namespace` requires operator-controller v1.12.0's experimental CRD,
  capability-checks its optional `spec.namespace`, omits the CE field, and prepares the
  bundle-metadata namespace before applying the migration COS.
- COS is itself an experimental OLMv1 API, so the migration tooling already requires the
  experimental controller profile. System-managed namespace availability in that same profile
  is not an additional migration gap; the capability gate remains a required safety check.
- Fixture E2E covers explicit cross-namespace migration; live E2E covers the acknowledged
  source-namespace deletion path. PR #51 added system-managed fixture coverage.

### Phase 7 — OLMv1 APIService support decision [OPRUN-4723] ⛔ UNSUPPORTED

OLMv1 does not support operators with owned APIService objects. C3 is therefore a permanent,
non-overridable migration block, not an open feature gap; no controller or migration E2E is planned.

Renderer code merged in `operator-framework/operator-controller` PR #2885, including the
generator, validator, and certificate-provider handling. It is not supported controller
functionality: the manifest provider continues to reject bundles with APIService definitions.
The C3 negative tests remain the regression coverage for this decision.

### Phase 8 — Testing ⚠️ IN PROGRESS

The testing implementation provides `make migration/test-unit`, Kind bootstrap, a
three-operator fixture matrix and a live-operator matrix. The committed fixture snapshots cover ecr-secret-operator,
external-secrets-operator, and redis-operator; the live matrix installs the same
packages through OLMv0. Neither matrix depends on the other's cluster or kubeconfig.
Migration CLIs are coverage-instrumented, and unit, fixture, and live coverage data can
be merged with `make migration/report-coverage-all`.

The fixture FBC is rebuilt locally from committed snapshots and served through a TLS
registry using OLMv1's `olmv1-ca`; it has no dependency on mutable Quay digests. The
suite covers catalog migration, check, dry-run resource/deletion previews, conversion,
negative non-mutation paths, recovery after CE creation failure, and execution of both
CLIs from a ServiceAccount-authenticated Job. The focused OPRUN-4716 check validates the
v1.12.0 handoff: migration revision 1 uses `None`, the controller creates revision
2 with `Prevent`, and both revisions reach `Succeeded=True`.

The live suite also creates a CatalogSource from OLMv0's catalog image and validates its
migration. Failure diagnostics collect resources/events and OLMv1 controller logs. CI runs
unit coverage, fixture E2E, live E2E, the in-cluster Job, and COS supersession independently,
then displays combined coverage from all coverage-producing jobs.

Live validation on 2026-09-23 migrated `logging-operator` from the OpenShift community
catalog. The resulting ClusterExtension reported `Installed=True` and `Available=True`; its
migration COS revision (`None`) was archived after the controller-created catalog revision
(`Prevent`) reached `Succeeded=True`. The existing operator Deployment stayed available with
its Pod running and no restarts.

PR #54 strengthened live rollback and conflict cleanup: it checks refusal without
`--acknowledge-installed`, removes all owned COS revisions, restores the original Subscription
spec, and waits for healthy OLMv0 reconciliation. PR #55 added a four-state fixture batch,
Eligible-only selection, non-mutating batch preview, and stop/continue failure tests. PR #56
added twelve fixture acknowledgment cases covering the soft flags and hard blocks (now thirteen
with the `unsupported-all-namespaces` case from PR #67), and made
dry-run enforce eligibility before previewing resources. These tests run in CI with coverage.

PR #76 closes the rollback followed by remigration regression. Rollback now starts CE deletion
with a finalizer before listing revisions, retains the backup through resource release, removes
revisions created by an in-flight reconcile, and clears OLMv1 ownership metadata only from
verified preserved resources. Its live test checks that Deployment UIDs survive rollback and
are adopted by new COS revisions after a second conversion. Unit tests cover the release guards
and revision recreation race.

The Phase 8 exit criterion is not yet met. PR #58 merged on-disk backup assertions and associated
InstallPlan capture. PR #66 added focused validation tests, including self-dependency detection
and command-name parsing. PR #70 added unit assertions for inline DeploymentConfig mapping and
large-bundle SecretPacker packing. PR #74 added fixture tests for non-image source refusal,
dependent warnings, command-named Subscriptions, and DeploymentConfig mapping, plus a live
four-state batch with two eligible operators. Remaining gaps include
post-upgrade field persistence, fixture/live catalog naming/adoption/overflow/deletion edges,
shared-resource adoption, OLMv1-driven upgrades, and the still-unenforced coverage threshold.
These are test gaps; they do not include
OLMv1 dependency resolution, APIService migration, or standard-profile system-managed namespace
availability. The documented ≥80% threshold applies to `migration/pkg/...` coverage combined
from unit and coverage-producing E2E runs; it is not yet enforced by the Make target or CI.
Downstream topology, architecture, and restricted-network qualification also remains outside
Kind CI.

---

## Post-merge review fixes

PR #23 (`olm-review-fixes`, merged) addressed the inline review comments from perdasilva on
merged PRs #14 and #16:

- `catalog.go`: FBC schema string constants (`fbcSchemaPackage`, `fbcSchemaBundle`, `fbcSchemaChannel`)
- `collector.go`: extend `olmv0OnlyKinds` to include CSV/Subscription/InstallPlan; remove
  redundant local `skipKinds`; rename `gatherResourcesByOwnerLabel`/`OwnerRef` parameters
  to `ownerName`
- `labels.go`: named wait-poll constants (`cosWaitPollInterval/Timeout`, `ceWaitPollInterval/Timeout`, `subWaitPollInterval/Timeout`)
- `migration.go`: use named wait constants; TODO for SecretPacker exportability
- `scan.go`: replace custom `splitNamespacedName` with `splitSubRef` using `strings.SplitN`
- `compatibility.go`: restore C3 hard block (already merged separately in PR #22)
- Catalog CLI: status string constants (`statusCreated`, `statusAdopted`, `statusSkipped`, `statusError`, `statusDryRun`)
- `convert.go`: TODO for progress channel design

---

## Merged PR chain

| PR  | Title                                              | Branch                    | Status |
|-----|----------------------------------------------------|---------------------------|--------|
| #1  | SDD Docs                                           | sdd-docs                  | MERGED |
| #11 | Add OWNERS, PR template, issue template            | olm-owners                | MERGED |
| #12 | Add bingo-managed tooling and golangci-lint config | olm-bingo-lint            | MERGED |
| #13 | Add stub Makefile, GitHub Actions workflows        | olm-makefile-workflows    | MERGED |
| #14 | Add migration/pkg/migration core library           | olm-library-core          | MERGED |
| #15 | Add migration/pkg/catalogmigration (Phase 5)       | olm-catalog-migration     | MERGED |
| #16 | Add example CLIs                                   | olm-examples              | MERGED |
| #22 | Restore C3 hard block                              | olm-restore-c3            | MERGED |
| #23 | Address review feedback from PRs #14 and #16       | olm-review-fixes          | MERGED |
| #28 | Pin kind with bingo                                | testing                   | MERGED |
| #29 | Define migration test strategy                     | testing-docs              | MERGED |
| #30 | Extend migration package coverage                  | unit-test                 | MERGED |
| #31 | Add migration unit-test Make target                | unit-test-makefile        | MERGED |
| #32 | Add live migration test suite                      | migration-live-tests      | MERGED |
| #33 | Add fixture migration E2E suite                    | migration-fixture-tests   | MERGED |
| #35 | Build fixture catalog locally                      | migration-fixture-catalog | MERGED |
| #37 | Add migration negative tests and recovery fixes    | migration-negative-tests-only | MERGED |
| #38 | Collect coverage from unit test target             | migration-unit-coverage   | MERGED |
| #39 | Add migration test plan                            | migration-test-plan       | MERGED |
| #40 | Harden migration catalog and target preflight      | migration-catalogd-ca-discovery | MERGED |
| #41 | Run migration CLIs in an in-cluster Job            | migration-in-cluster-job-tests | MERGED |
| #42 | Verify migrated ClusterObjectSet supersession      | migration-cos-adoption-test | MERGED |
| #44 | Upgrade E2E operator-controller to v1.12.0          | migration-operator-controller-v1-12 | MERGED |
| #45 | Include COS supersession coverage                  | migration-cos-supersession-coverage | MERGED |
| #46 | Support cross-namespace migration                  | migration-install-namespace-core | MERGED |
| #47 | Support acknowledged source namespace deletion     | migration-install-namespace-delete | MERGED |
| #48 | Cover cross-namespace migration scenarios          | migration-install-namespace-tests | MERGED |
| #49 | Describe install namespace migration               | migration-install-namespace-docs | MERGED |
| #52 | Wait only for source Deployment Pods               | migration-deployment-pod-wait | MERGED |
| #54 | Clean up rollback revisions and verify recovery    | migration-rollback-recovery-tests | MERGED |
| #55 | Verify batch selection and dry-run safety          | migration-batch-safety-tests | MERGED |
| #56 | Cover migration acknowledgments and dry-run eligibility | migration-acknowledgment-e2e-tests | MERGED |
| #51 | Support system-managed install namespaces | migration-system-managed-namespace | MERGED |
| #59 | Use public migration APIs and add JSONL CLI output | migration-cli-public-apis | MERGED |
| #58 | Verify disk backups and associated InstallPlans | migration-backup-directory-tests | MERGED |
| #61 | Reject Subscriptions referencing non-image catalogs | migration-non-image-catalogsource-rejection | MERGED |
| #62 | Clarify migration library GoDoc contracts | migration-library-godoc | MERGED |
| #63 | Reject existing system-managed namespaces | migration-reject-existing-system-namespace | MERGED |
| #64 | Handle catalog migration edge cases | migration-catalog-edge-cases | MERGED |
| #65 | Unify catalog migration progress and CLI output | migration-catalog-progress-output | MERGED |
| #66 | Cover small migration validation gaps | migration-small-validation-tests | MERGED |
| #67 | Require AllNamespaces bundle support | migration-all-namespaces-install-mode | MERGED |
| #69 | Show operator CLI usage only for argument errors | migration-cli-usage-errors | MERGED |
| #70 | Validate DeploymentConfig mapping and large bundle packing | migration-validation-payload-tests | MERGED |
| #72 | Show catalog CLI usage only for argument errors | migration-catalog-cli-usage-errors | MERGED |
| #73 | Add Kind migration demo scripts | demo-functionality | MERGED |
| #74 | Close focused migration acceptance gaps | migration-acceptance-test-gaps | MERGED |
| #75 | Format migration Makefile recipes | migration-makefile-recipe-formatting | MERGED |
| #76 | Release orphaned resources during migration rollback | fix/migration-rollback-remigration | MERGED |

---

## Open items

1. **Phase 8 / OPRUN-4762** — enforce and meet the `migration/pkg/...` coverage threshold,
   then complete the remaining applicable `validation.md` cases, especially fixture/live
   catalog edges, upgrades, shared resources, and large-payload E2E.

Dependency resolution and APIService-owning operators are unsupported by OLMv1. C2/C9 and C3
must continue to reject them; implementing those OLMv0 behaviors in this library is not an open
gap. The optional system-managed namespace mode is implemented and tested with the experimental
controller profile already required for COS, so its absence from a standard OLMv1 installation
is likewise not an additional gap.
