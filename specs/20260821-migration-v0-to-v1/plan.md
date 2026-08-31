# Implementation Plan — OLMv0 → OLMv1 Migration

Eight phases plus a cross-repo prerequisite. Each phase lists its goal, key files,
dependencies, a one-line exit criterion, and the tracking Jira story. Requirement
references (`Rn`) point at [requirements.md](requirements.md); validation references at
[validation.md](validation.md).

Base for the port: [perdasilva/operator-controller `migration`](https://github.com/perdasilva/operator-controller/tree/migration)
(`internal/operator-controller/migration/` + `hack/tools/migrate/`).

### CLI as prototype / library exercise

The CLIs in this repo (`migrate-operators-v0-to-v1`, `migrate-catalogs-v0-to-v1`) are
**example consumers of the library**, not production delivery artifacts. Their purpose is to
exercise and test the library API during the prototype phase; binary packaging and production
delivery are deferred to the downstream TP ([OCPSTRAT-2692](https://redhat.atlassian.net/browse/OCPSTRAT-2692)).

To make this intent obvious in the repo layout, CLIs live under **`migration/examples/cmd/`**
rather than `migration/cmd/`:

```
migration/
  pkg/migration/           ← the library (canonical; the real deliverable)
  pkg/catalogmigration/    ← catalog migration library
  examples/
    cmd/
      migrate-operators-v0-to-v1/   ← example CLI exercising pkg/migration
      migrate-catalogs-v0-to-v1/    ← example CLI exercising pkg/catalogmigration
```

Downstream consumers (e.g., an `oc` plugin or the Console) will import `pkg/migration`
directly and build their own CLI surface.

---

## Prerequisite (cross-repo, runs in parallel) — [OPRUN-4716](https://redhat.atlassian.net/browse/OPRUN-4716)

**Verify COS adoption end-to-end in `operator-controller`.** No controller changes are
expected: the COS controller reconciles any COS regardless of origin, and the CE controller
discovers a pre-created COS via `olm.operatorframework.io/owner-name` and adopts it (SSA
patch) on first reconcile. Confirm a manually pre-created COS (owner labels, `revision: 1`,
correct bundle annotations) reaches `Succeeded=True` and is adopted by a subsequently created
CE **without** producing a duplicate COS. Track boxcutter phase 2 / `ClusterObjectDeployment`
(R2.7). *Exit:* documented confirmation the flow works with no operator-controller changes.
Verified during Phase 8 E2E.

---

## Phase 1 — Repo bootstrap & prototype port — [OPRUN-4717](https://redhat.atlassian.net/browse/OPRUN-4717)
**Goal:** Stand up the `v0` module and port the prototype onto OLMv1's current APIs.
- Module `github.com/operator-framework/library-olm` (`v0.x`); GitHub Actions (build, test,
  golangci-lint, go-apidiff); prow config (tide, lgtm/approve, hold) mirroring operator-controller.
- Port to `migration/pkg/migration/` and `migration/examples/cmd/migrate-operators-v0-to-v1/`.
- Rename `ClusterExtensionRevision*` → `ClusterObjectSet*` (`clusterobjectset.go` apply-config,
  `ClusterObjectSetTypeSucceeded`) (R2.2).
- Replace inline COS objects with boxcutter **SecretPacker**; set `CollisionProtection: IfNoController` (R2.4).
- `go.mod` deps: `operator-framework/operator-controller` (`ocv1`), `operator-framework/api` (OLMv0).

**Depends on:** prerequisite (for the adoption contract). **Exit:** `go build ./...` and
`go test ./...` pass; CI green on the skeleton.

## Phase 2 — Scan & classification — [OPRUN-4718](https://redhat.atlassian.net/browse/OPRUN-4718)
**Goal:** Four-state classification with catalog availability at scan time (R1.3, R4, C7).
- `types.go`: `OperatorStatus` enum (`Eligible`/`Ineligible`/`AlreadyMigrated`/`Conflict`),
  replacing `OperatorScanResult.Eligible bool`.
- `scan.go` `ScanAllSubscriptions`: detect `Conflict` (Sub + annotated CE) and
  `AlreadyMigrated` (no Sub + annotated CE); call catalog resolution per operator.
- `migration.go`: set the `olm.operatorframework.io/migrated-from-subscription: <ns>/<name>`
  annotation on **both** the COS and the CE (replacing the prototype's `migrated-from-v0: "true"`).
  Setting it on the COS makes migrated revisions discoverable by cluster admins independently of
  the CE, and provides provenance even if the CE annotation is lost.

**Depends on:** Phase 1. **Exit:** unit tests classify one fixture operator into each state.

## Phase 3 — Compatibility checks & acknowledgment framework — [OPRUN-4719](https://redhat.atlassian.net/browse/OPRUN-4719)
**Goal:** All eligibility rules (R3) and the override mechanism (R2.5).
- `compatibility.go`: implement C1, C4, C5, C6, C8 as overridable (soft) checks; C2 and C3
  as hard (non-overridable) blocks. Add `checkNoOLMv0APIAccess` (C5, inspecting all installed
  RBAC, **excluding** `operatorconditions`, flagging only if OLMv0 API access exists without
  OLMv1 RBAC). Keep the OperatorCondition-**status** check (C4, R9).
- `types.go`: `Options` gains one `bool` per soft flag — `AcknowledgeWatchScopeChange`,
  `AcknowledgeOperatorCondition`, `AcknowledgeOLMv0APIAccess`, `AcknowledgeScopedServiceAccount`,
  `AcknowledgeNotSteadyState`, `AcknowledgeNamespaceDelete`, `AcknowledgeInstalled` — plus
  `ContinueOnError`.
- On use, record `olm.operatorframework.io/acknowledged-<flag>: "true"` on the CE.
- Collector places all objects (incl. CRDs) into the COS with `IfNoController`.

**Note:** C3 remains a hard block until OLMv1 supports APIService-based operators end-to-end.
The rendering infrastructure was added in operator-controller ([OPRUN-4723](https://redhat.atlassian.net/browse/OPRUN-4723))
but is not yet wired into the Boxcutter path. C3 will be removed in a future phase once
end-to-end support is confirmed.

**Depends on:** Phases 1, 2. **Exit:** each soft check flips Ineligible→Eligible when its
flag is set; CE carries the matching annotation.

## Phase 4 — Migration & recovery commands — [OPRUN-4720](https://redhat.atlassian.net/browse/OPRUN-4720)
**Goal:** The operator CLI surface (R1.1, R1.2, R1.4, R1.5, R1.8) — verb-plus-target
(kubectl/`oc`-style); each verb takes an operator name or `--all`.
- `check <op> | --all`: readiness + compatibility + four-state classification; `--all` scans the cluster (calls `Check`/`ScanAll`).
- `convert <op> | --all`: profile → resolve catalog → back up Subscription and OperatorGroup
  specs to CE annotations (R2.5) → optional `--backup <directory>` (R2.6) → collect
  (primary: `Operator` CR `status.components.refs`; supplementary: `olm.owner` label query,
  ownerRef query, InstallPlan steps; dedup by GVK+ns+name — see R5) → create COS (wait
  `Succeeded=True`) → create CE → cleanup.
  `--dry-run` previews via `Gather`. `--all` prints the four-section summary, then converts
  each Eligible operator; `--continue-on-error` to keep going.
- `rollback <ce-name> | --all`: require `--acknowledge-installed` when CE is `Installed=True`; delete CE
  then COS with orphan cascade (fallback: new COS revision → `Succeeded=True` → orphan delete);
  restore Subscription from the backup annotation (`startingCSV` → `installedCSV`).
- `cleanup <ce-name> | --all`: for `Conflict`; delete Subscription (orphan) + `CleanupOLMv0Resources`.
- CLI files under `migration/examples/cmd/migrate-operators-v0-to-v1/` (one file per verb).

**Depends on:** Phases 1–3. **Exit:** `check`, `convert` (single + `--all`), `rollback`, and
`cleanup` each pass their VALIDATION per-command checks on kind.

## Phase 5 — Catalog migration CLI *(parallelizable with 2–4)* — [OPRUN-4722](https://redhat.atlassian.net/browse/OPRUN-4722)
**Goal:** `migrate-catalogs-v0-to-v1` (R7).
- `migration/pkg/catalogmigration/` + `migration/examples/cmd/migrate-catalogs-v0-to-v1/`.
- List CatalogSources; skip already-migrated (matching image); create `ClusterCatalog` from
  the image and wait `Serving=True`; report per source; `--dry-run`.
- Report non-image sources (configmap/internal/address) as not migratable.

**Depends on:** Phase 1. **Exit:** N CatalogSources → N serving ClusterCatalogs; operator scan
then reports catalog-available.

## Phase 6 — Install-namespace change ⚠️ blocked — [OPRUN-4721](https://redhat.atlassian.net/browse/OPRUN-4721)
**Goal:** Support `--install-namespace` differing from the Subscription namespace (R6, R9).
- **Blocked on** [OCPSTRAT-2690](https://redhat.atlassian.net/browse/OCPSTRAT-2690) /
  [OPRUN-4505](https://redhat.atlassian.net/browse/OPRUN-4505) /
  [PR #2825](https://github.com/operator-framework/operator-controller/pull/2825) (making
  `spec.namespace` optional / COS-managed). Once it lands, the tool may omit `spec.namespace`.
- Move namespace-scoped resources to the new namespace; copy PSA (`pod-security.kubernetes.io/*`)
  and `security.openshift.io/scc.podSecurityLabelSync` labels; delete the old namespace only with
  `--acknowledge-namespace-delete`.

**Depends on:** Phases 1, 3 + PR #2825. **Exit:** resources land in the new namespace with PSA/SCC
labels copied; old namespace deleted only when acknowledged.

## Phase 7 — OLMv1 APIService renderer support *(cross-repo, operator-controller)* — [OPRUN-4723](https://redhat.atlassian.net/browse/OPRUN-4723)
**Goal:** Add `apiregistration.k8s.io` support to the OLMv1 registry+v1 bundle renderer as
infrastructure for future end-to-end APIService support.

The rendering code (`BundleCSVAPIServiceGenerator`, `CheckAPIServiceDeploymentReferentialIntegrity`,
cert provider updates) was added to operator-controller as infrastructure but is **not yet
registered** in `ResourceGenerators` or `BundleValidator` because end-to-end Boxcutter support
is not yet confirmed. C3 remains a hard block until this is fully wired and validated.

**Scope (in `operator-controller`):**
- Added `BundleCSVAPIServiceGenerator`, `CheckAPIServiceDeploymentReferentialIntegrity`, and
  cert provider support for `APIService` objects (implemented but not yet registered in
  `ResourceGenerators` / `BundleValidator` pending Boxcutter end-to-end validation).
- C3 remains a hard block in the migration tool until the Boxcutter path is fully wired.

**Depends on:** nothing (ran in parallel). **Status:** rendering infrastructure merged; C3
removal deferred until end-to-end Boxcutter support is confirmed.

## Phase 8 — Testing — *(testing stories auto-created per epic)*
**Goal:** Confidence across unit and E2E (R-wide).
- Unit: ≥80% of `migration/pkg/...` with `controller-runtime/pkg/client/fake` — readiness,
  compatibility (each ack flag), scan (4 states), catalog parsing, collector (CRD/IfNoController,
  namespace rewrite, dedup), rollback/cleanup.
- E2E on kind (OLMv0 + OLMv1): all four states, each acknowledgment override, rollback, cleanup,
  catalog migration, and the COS-adoption prerequisite.

**Depends on:** Phases 1–5 (via Phase 4; Phase 6 tests gated on that phase). **Exit:** unit coverage target met;
E2E scenarios in VALIDATION pass in CI.

---

## Dependency summary

```
Prerequisite (OPRUN-4716, operator-controller) ──┐  (parallel; needed before Phase 8)
                                                  ▼
Phase 1 (4717) ──► Phase 2 (4718) ──► Phase 3 (4719) ──► Phase 4 (4720) ──► Phase 8
   │                  └─────────────────────────────────► Phase 6 (4721) BLOCKED
   └──► Phase 5 (4722, parallel) ──────────────────────────────────────────► Phase 8

Phase 7 (4723, operator-controller, parallel) ──► removes C3 from Phase 3 (operators with APIService definitions become Eligible)
```
