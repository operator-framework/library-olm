# Implementation Status — OLMv0 → OLMv1 Migration

Last updated: 2026-09-09

---

## Summary

Phases 1–5 are fully implemented and merged into `main`. Phase 6 is blocked on an
upstream operator-controller dependency. Phase 7 (operator-controller) is partially
implemented (infrastructure merged, not yet wired into the rendering path). Phase 8
(testing) is not yet started.

---

## Phases

### Phase 1 — Repo bootstrap & prototype port [OPRUN-4717] ✅ DONE

Merged via stacked PRs #11–#16 on 2026-08-26.

- Module `github.com/operator-framework/library-olm` established
- GitHub Actions workflows (build, test, golangci-lint, go-apidiff)
- Prow config in `openshift/release` (tide, lgtm/approve, hold) — completed
- `migration/pkg/migration/` and `migration/examples/cmd/migrate-operators-v0-to-v1/` ported from perdasilva prototype
- `ClusterObjectSet` (COS) with `CollisionProtection: IfNoController`
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
- C3 (APIService definitions — **hard block**, permanent until OLMv1 supports end-to-end)
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

### Phase 6 — Install-namespace change [OPRUN-4721] ⛔ BLOCKED

Blocked on [OCPSTRAT-2690] / [OPRUN-4505] / [operator-controller PR #2825] — making
`spec.namespace` optional / COS-managed. No implementation started.

### Phase 7 — OLMv1 APIService renderer support [OPRUN-4723] ⚠️ PARTIAL

Implementation in `operator-framework/operator-controller` PR #2885 (open).

- `BundleCSVAPIServiceGenerator` added to `internal/operator-controller/rukpak/render/registryv1/`
- `CheckAPIServiceDeploymentReferentialIntegrity` added
- Cert provider updated for `APIService` objects
- **NOT registered** in `ResourceGenerators` or `BundleValidator` — end-to-end Boxcutter
  support not yet confirmed
- C3 remains a hard block in the migration tool until this is fully wired and validated

### Phase 8 — Testing ⚠️ IN PROGRESS

Initial unit coverage has been added for catalog parsing and priority/poll conversion,
catalog migration dry-run/adoption behavior, compatibility gates, phase ordering, resource
deduplication, SecretPacker, and common options/report helpers. `make migration/test-unit` passes.

The phase exit criterion is not yet met: package coverage is 16.8% for
`migration/pkg/migration` and 64.7% for `migration/pkg/catalogmigration` (target: ≥80%),
and the kind E2E harness/scenarios have not been added. Depends on Phases 1–5 being complete
(now satisfied).

---

## Post-merge review fixes

PR #23 (`olm-review-fixes`, open) addresses all inline review comments from perdasilva on merged PRs #14 and #16:

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

**Pending:** commit needs GPG signature (`git sign`) then `git push origin olm-review-fixes --force`.

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
| #23 | Address review feedback from PRs #14 and #16       | olm-review-fixes          | OPEN   |

---

## Open items

1. **Sign and push PR #23** — `git sign && git push origin olm-review-fixes --force`
2. **operator-controller PR #2885** — APIService infrastructure (open, not yet approved)
3. **Phase 6** — blocked on OCPSTRAT-2690 / operator-controller PR #2825
4. **Phase 8** — unit tests (≥80% coverage target) and E2E on kind needed
