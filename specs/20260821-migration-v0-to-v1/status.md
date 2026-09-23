# Implementation Status — OLMv0 → OLMv1 Migration

Last updated: 2026-09-25

---

## Summary

Phases 1–5 are fully implemented and merged into `main`. Phase 6 is implemented in the
pending PR stack #46–#51, including the experimental system-managed namespace mode. Phase 7
(operator-controller) has registered renderer support but still needs controller E2E validation. Phase 8
has a complete automated testing foundation: unit coverage, deterministic fixture and
live-operator matrices, negative/recovery coverage, an in-cluster Job check, and
combined coverage reporting. Its broader validation inventory and coverage threshold
remain open work.

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
- Per-flag audit annotations written to CE: `olm.operatorframework.io/acknowledged-<flag>`

### Phase 4 — Migration & recovery commands [OPRUN-4720] ✅ DONE

Merged in PR #16.

- `migrate-operators-v0-to-v1` example CLI under `migration/examples/cmd/`
- `check` verb: readiness + compatibility + four-state classification; `--all`
- `convert` verb: catalog resolution → backup Sub/OG specs to CE annotations → collect
  resources → create COS (wait `Succeeded=True`) → create CE → cleanup; `--dry-run`;
  `--backup <dir>`; `--all` with `--continue-on-error`
- `rollback` verb: delete CE+COS (orphan cascade); restore Subscription from backup annotation;
  requires `--acknowledge-installed` when `Installed=True`
- `cleanup` verb: Conflict resolution — delete Subscription (orphan) + `CleanupOLMv0Resources`

### Phase 5 — Catalog migration CLI [OPRUN-4722] ✅ DONE

Merged in PR #15.

- `migration/pkg/catalogmigration/` library
- `migrate-catalogs-v0-to-v1` example CLI under `migration/examples/cmd/`
- Lists CatalogSources; skips already-migrated (matching image); creates `ClusterCatalog`
  and waits `Serving=True`; reports per source; `--dry-run`
- Non-image sources (configmap/internal/address) reported as not migratable

### Phase 6 — Install-namespace change [OPRUN-4721] 🚧 PENDING REVIEW

The stacked PRs #46–#51 implement the explicit install-namespace flow, acknowledged source
namespace deletion, cross-namespace coverage, documentation, and a system-managed mode.

- `--install-namespace` creates or updates the target namespace, copies PSA/SCC labels, moves
  collected resources, and removes source copies after cutover.
- `--acknowledge-namespace-delete` is the required destructive opt-in for deleting the source
  namespace after a cross-namespace migration.
- `--system-managed-install-namespace` requires operator-controller v1.12.0's experimental CRD,
  capability-checks its optional `spec.namespace`, omits the CE field, and prepares the
  bundle-metadata namespace before applying the migration COS.
- Fixture E2E covers explicit cross-namespace and system-managed migration; live E2E covers the
  acknowledged source-namespace deletion path.

### Phase 7 — OLMv1 APIService support decision [OPRUN-4723] ⛔ UNSUPPORTED

OLMv1 does not support operators with owned APIService objects. C3 is therefore a permanent,
non-overridable migration block, and no controller or migration E2E is planned.

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

The Phase 8 exit criterion is not yet met. Remaining work includes raising and measuring
coverage against the agreed threshold, covering every acknowledgment override and all scan
states end-to-end, `--all` ordering and `--continue-on-error`, rollback and conflict cleanup,
backup-directory behavior, shared-resource behavior, large-bundle SecretPacker behavior,
OLMv1-driven upgrade, and the remaining catalog edge cases in `validation.md`.

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
| #37 | Add migration negative tests and recovery fixes    | migration-negative-tests  | MERGED |
| #38 | Collect coverage from unit test target             | migration-unit-coverage   | MERGED |
| #39 | Add migration test plan                            | migration-test-plan       | MERGED |
| #40 | Harden migration catalog and target preflight      | migration-catalogd-ca-discovery | MERGED |
| #41 | Run migration CLIs in an in-cluster Job            | migration-in-cluster-job-tests | MERGED |
| #42 | Verify migrated ClusterObjectSet supersession      | migration-cos-adoption-test | MERGED |

## Pending PR stack

| PR | Title | Branch | Status |
|---|---|---|---|
| #46 | Support cross-namespace migration | migration-install-namespace-core | OPEN; base of stack #50 |
| #47 | Support acknowledged source namespace deletion | migration-install-namespace-delete | OPEN; depends on #46 |
| #48 | Cover cross-namespace migration scenarios | migration-install-namespace-tests | OPEN; depends on #47 |
| #49 | Describe install namespace migration | migration-install-namespace-docs | OPEN; depends on #48 |
| #51 | Support system-managed install namespaces | migration-system-managed-namespace | OPEN; depends on #49 |

PR #43 is the superseded, unstacked original install-namespace proposal; it is not part of
stack #50 and should not be merged alongside it.

---

## Open items

1. **Phase 6** — complete review and merge of stack #50 (PRs #46–#51). The system-managed
   path remains intentionally limited to the experimental optional-namespace CRD.
2. **Phase 8 / OPRUN-4762** — raise and measure
   `migration/pkg/...` coverage to the agreed threshold, and complete the remaining
   `validation.md` scenarios.
