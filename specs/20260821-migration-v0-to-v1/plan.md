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

**Verify COS handoff end-to-end in `operator-controller`.** The released v1.12.0 behavior is
supersession rather than adoption: a migration-created revision 1 with collision protection
`None` reaches `Succeeded=True`; creating the CE produces a controller-owned, catalog-derived
revision 2 with `Prevent`, which also reaches `Succeeded=True`. Track boxcutter phase 2 /
`ClusterObjectDeployment` (R2.7). *Exit:* documented, automated confirmation of this handoff.
Verified during Phase 8 E2E.

**COD transition (R2.7), in progress:** the library selects COD creation when its CRD exists,
current-generation rollout checks, and COD-aware rollback. API types are pinned to
operator-controller commit `dae9b613163a`, matching the COD CI controller and including
the adoption label requirement. Migration stamps `apps.kubernetes.io/managed-by:
operator-controller` on the ownerless COD before handoff; unit and E2E assertions
check this label and preservation of the original COD identity. Directly created
COS revisions use the same key with value `object-controller` for COD adoption;
the object-controller labels COD-generated revisions itself. Rollback persists
verified COD UIDs before deletion to support dangling revision references on retries.
Definitive API create rejections permit recovery; ambiguous failures remain conservative.
The COD-backed CE applier merged in upstream
PR #3058. On 2026-10-29, a disposable kind cluster running operator-controller main
`255a4026` passed the fixture handoff test: one COD retained its UID across CE creation,
and its revisions 1 and 2 used `None` and `Prevent` collision protection. A separate
full CLI fixture conversion and acknowledged rollback restored the Subscription,
removed COD/COS resources, and preserved the operator Deployment UID and availability.
The default E2E setup builds the pinned current experimental controller. A separate CI job
builds pinned COD-capable commit `dae9b613163a8fc5df36e3b9ad5a770a64c0048c` and
runs covered handoff and workload-preserving rollback E2E targets. The rollback target
passed locally on 2026-10-29.
Both covered targets passed again on 2026-11-05 against `bbe43971eadd` on kind
Kubernetes 1.37, verifying labeled COD adoption and rollback.
The covered handoff and rollback targets also pass against `77754d81ea01`,
including COS manager-label assertions. Direct COS creation and the
`UnmanagedRevision` terminal condition are covered by unit regressions.
The current API uses typed `inlineManifest`/`secretRef` entries for creation and
rollback. Older experimental COS/COD schemas are not supported; the controller
and library must use matching definitions.

---

## Phase 1 — Repo bootstrap & prototype port — [OPRUN-4717](https://redhat.atlassian.net/browse/OPRUN-4717)
**Goal:** Stand up the `v0` module and port the prototype onto OLMv1's current APIs.
- Module `github.com/operator-framework/library-olm` (`v0.x`); GitHub Actions (build, test,
  golangci-lint, go-apidiff); prow config (tide, lgtm/approve, hold) mirroring operator-controller.
- Port to `migration/pkg/migration/` and `migration/examples/cmd/migrate-operators-v0-to-v1/`.
- Rename `ClusterExtensionRevision*` → `ClusterObjectSet*` (`clusterobjectset.go` apply-config,
  `ClusterObjectSetTypeSucceeded`) (R2.2).
- Replace inline COS objects with boxcutter **SecretPacker**; set migration COS collision protection to `None` (R2.4).
- `go.mod` deps: `operator-framework/operator-controller` (`ocv1`), `operator-framework/api` (OLMv0).

**Depends on:** prerequisite (for the COS handoff contract). **Exit:** `go build ./...` and
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
- `compatibility.go`: implement C1, C4, C5, C6, C8 as overridable (soft) checks; C1a, C2, and C3
  as hard (non-overridable) blocks. C1a verifies that the installed CSV declares AllNamespaces
  support; acknowledging a watch-scope change cannot override missing bundle support. Add
  `checkNoOLMv0APIAccess` (C5, inspecting all installed
  RBAC, **excluding** `operatorconditions`, flagging only if OLMv0 API access exists without
  OLMv1 RBAC). Keep the OperatorCondition-**status** check (C4, R9).
- `types.go`: `Options` gains one `bool` per soft flag — `AcknowledgeWatchScopeChange`,
  `AcknowledgeOperatorCondition`, `AcknowledgeOLMv0APIAccess`, `AcknowledgeScopedServiceAccount`,
  `AcknowledgeNotSteadyState`, `AcknowledgeNamespaceDelete`, `AcknowledgeInstalled` — plus
  `ContinueOnError`.
- On use, record `olm.operatorframework.io/acknowledged-<flag>: "true"` on the CE.
- Collector places all objects (incl. CRDs) into the migration COS with `None` collision protection.

**Note:** C3 is a permanent hard block. OLMv1 does not support APIService-owning operators;
the migration tool must reject them before making any changes. The renderer work recorded in
[OPRUN-4723](https://redhat.atlassian.net/browse/OPRUN-4723) does not change that supported
behavior. C2/C9 dependency refusals are likewise intentional because OLMv1 does not resolve
OLMv0 operator dependencies; neither class of refusal is a remaining migration gap.

**Depends on:** Phases 1, 2. **Exit:** each soft check flips Ineligible→Eligible when its
flag is set; CE carries the matching annotation.

## Phase 4 — Migration & recovery commands — [OPRUN-4720](https://redhat.atlassian.net/browse/OPRUN-4720)
**Goal:** The operator CLI surface (R1.1, R1.2, R1.4, R1.5, R1.8) — verb-plus-target
(kubectl/`oc`-style); each verb takes an operator name or `--all`.
- `check <op> | --all`: readiness + compatibility + four-state classification; `--all` scans the cluster.
- `convert <op> | --all`: profile → resolve catalog → back up Subscription and OperatorGroup
  specs to CE annotations (R2.5) → optional `--backup <directory>` of the profiled and
  CSV-associated InstallPlans (R2.6; deduplicate by namespace/name) → collect
  (primary: `Operator` CR `status.components.refs`; supplementary: `olm.owner` label query,
  ownerRef query, InstallPlan steps; dedup by GVK+ns+name — see R5) → create COS (wait
  `status.completedAt` and `Ready=True`) → create CE → cleanup.
  `--dry-run` previews without mutation. `--all` prints the four-section summary, then converts
  each Eligible operator; `--continue-on-error` to keep going. Batch classification uses
  the requested acknowledgment overrides; `-n` and `--ce-name` are rejected with `--all`.
- `rollback <ce-name> | --all`: require `--acknowledge-installed` when CE is `Installed=True`;
  add a rollback finalizer and start CE orphan deletion to stop new revisions,
  snapshot COS-controlled resource identities on CE, merge verified ownership from any revision
  created by an in-flight reconciliation before orphan-deleting it, wait for orphan propagation,
  release OLMv1 revision metadata from preserved resources, restore the Subscription from its
  backup annotation, then remove the CE finalizer.
- `cleanup <ce-name> | --all`: for `Conflict`; delete Subscription (orphan) + `CleanupOLMv0Resources`.
- Fail operator commands on text or JSONL progress-write errors before reporting a
  successful check, scan, or conversion result.
- CLI files under `migration/examples/cmd/migrate-operators-v0-to-v1/` (one file per verb).
- PR #59 routes single-operator verbs through the public `Migrator` APIs and adds
  `--output=jsonl`; dry-runs emit a preview and a completion result record.

**Depends on:** Phases 1–3. **Exit:** `check`, `convert` (single + `--all`), `rollback`, and
`cleanup` each pass their VALIDATION per-command checks on kind.

## Phase 5 — Catalog migration CLI *(parallelizable with 2–4)* — [OPRUN-4722](https://redhat.atlassian.net/browse/OPRUN-4722)
**Goal:** `migrate-catalogs-v0-to-v1` (R7).
- `migration/pkg/catalogmigration/` + `migration/examples/cmd/migrate-catalogs-v0-to-v1/`.
- List CatalogSources; skip already-migrated (matching image); create `ClusterCatalog` from
  the image and wait `Serving=True`; report per source; `--dry-run`.
- Report non-image sources (configmap/internal/address) as not migratable.
- Keep `MigrateCatalogs` result semantics while emitting typed progress, notes, and failures
  through the same callback used by operator migration. Share text/JSONL output utilities
  across both CLIs and expose `--output` on the catalog CLI. Treat text and JSONL
  progress-write failures as command errors, including for an empty scan.
- Before optional CatalogSource deletion, recheck current Subscription references and retain
  the source on lookup failure or image/name conflict.

**Depends on:** Phase 1. **Exit:** migratable CatalogSources map to serving ClusterCatalogs
(with shared images deduplicated); operator scan then reports catalog-available.

## Phase 6 — Install-namespace change ✅ implemented — [OPRUN-4721](https://redhat.atlassian.net/browse/OPRUN-4721)
**Goal:** Support `--install-namespace` differing from the Subscription namespace (R6, R9), plus
an explicitly selected OLMv1 system-managed namespace mode.
- Create or update the target namespace before removing OLMv0 management, copying PSA
  (`pod-security.kubernetes.io/*`) and `security.openshift.io/scc.podSecurityLabelSync` labels.
- Move collected namespace-scoped resources to the target namespace in the migration COS and,
  after the target CE is installed, remove their source copies.
- Delete the source namespace only with `--acknowledge-namespace-delete`; retain it by default.
- `--system-managed-install-namespace` uses the pinned operator-controller experimental
  optional-namespace CRD. It capability-gates the mode and omits the CE field. Migration
  prepares the bundle-metadata namespace before its COS and imports it for the catalog
  revision's ownership handoff, after which OLMv1 manages it. Unit tests
  reject CRDs that require the field; the dedicated fixture E2E verifies the omitted field,
  target namespace, and source-resource cleanup.

**Depends on:** Phases 1 and 3. **Exit:** resources land in the new namespace with PSA/SCC
labels copied; old namespace deleted only when acknowledged. The system-managed mode requires
the experimental optional-namespace controller profile, capability rejection, and its dedicated
E2E scenario. This is not a separate availability gap: the migration COS itself requires the
experimental operator-controller installation.

## Phase 7 — OLMv1 APIService support decision *(cross-repo, operator-controller)* — [OPRUN-4723](https://redhat.atlassian.net/browse/OPRUN-4723)
**Decision:** OLMv1 does not support operators that own `apiregistration.k8s.io` APIService
objects. APIService-based migration is out of scope and C3 remains a permanent,
non-overridable migration rejection.

Some renderer code was merged in operator-controller, including
`BundleCSVAPIServiceGenerator`, `CheckAPIServiceDeploymentReferentialIntegrity`, and
certificate-provider updates. The controller's manifest provider continues to reject bundles
with owned APIService definitions, so that code must not be treated as supported end-to-end
functionality.

**Status:** no controller or migration E2E is planned. Retain C3's refusal tests as a
regression guard. APIService migration is outside the supported OLMv1 feature set, not an
unfinished migration-library feature.

## Phase 8 — Testing — [OPRUN-4762](https://redhat.atlassian.net/browse/OPRUN-4762)
**Goal:** Confidence across unit and E2E (R-wide).
- Unit tests with `controller-runtime/pkg/client/fake` cover readiness, compatibility (each ack
  flag), scan (4 states), catalog parsing, collector (CRD/collision handling, namespace rewrite,
  dedup), and rollback/cleanup.
- E2E on kind (OLMv0 + OLMv1): all four states, each acknowledgment override, rollback, cleanup,
  catalog migration, and the COS-supersession prerequisite.
- C10 safety check: reject missing or non-image CatalogSources referenced by either the
  Subscription or an InstallPlan bundle lookup before conversion, regardless of package
  availability in another ClusterCatalog.
- Combined unit and coverage-producing E2E runs must cover ≥80% of the statements in
  `migration/pkg/...`; neither suite has a separate percentage minimum. The combined threshold
  is a Phase 8 target and is not currently enforced by the Make target or CI.

**Depends on:** Phases 1–5 (via Phase 4; Phase 6 tests gated on that phase). **Exit:** combined
coverage target met and enforced in CI; E2E scenarios in VALIDATION pass in CI. The coverage
gate remains open work.

---

## Dependency summary

```
Prerequisite (OPRUN-4716, operator-controller) ──┐  (parallel; needed before Phase 8)
                                                  ▼
Phase 1 (4717) ──► Phase 2 (4718) ──► Phase 3 (4719) ──► Phase 4 (4720) ──► Phase 8
   │                  └─────────────────────────────────► Phase 6 (4721)
   └──► Phase 5 (4722, parallel) ──────────────────────────────────────────► Phase 8

Phase 7 (4723, operator-controller): APIService-owning operators remain unsupported; C3 is permanent
```

Readiness reporting includes a lightweight COS API availability check; this is diagnostic, not a guarantee that a later write will succeed. Preparation does not repeat that COS CRD check. COD discovery remains necessary to select the revision manager. Gather (including CLI dry-run) and migration validate the prepared revision with a strict server-side dry-run create before removing OLMv0 management. Validation and creation share the same revision and Secret-reference builders; dry-run does not persist revisions or packed Secrets. The API server validates schema, admission, and permissions, but does not run controller-level validation of referenced workloads. Rollback permits verified newer sibling handoffs while retaining resource-UID, owner, and management-label checks.


## Follow-ups from the COS/COD compatibility review

These items remain outside PR93's initial compatibility scope. Keep them open
until their implementation and validation are complete; a review response alone
is not completion.

### Protected handoff to ClusterExtension

- [ ] Complete either [PR95 (COD-only)](https://github.com/operator-framework/library-olm/pull/95)
  or [PR97 (COS+COD)](https://github.com/operator-framework/library-olm/pull/97).
  These are alternative approaches, not sequential requirements.
- COD-only: create an adoption-disabled COD with `collisionProtection: None`,
  wait for its initial rollout, then atomically enable `Prevent`, remove
  phase/object overrides, and enable CE adoption. Complete the protected rollout
  before creating the CE.
- COS+COD: complete initial COS adoption with `None` before creating the protected
  COD with `Prevent`, then allow the CE handoff.
- Validate that initial resources are adopted, later unrelated-object collisions
  are rejected, and failed handoffs preserve recovery information.

Source: [handoff safety review](https://github.com/operator-framework/library-olm/pull/93#discussion_r4556776332).

### Explicit migration annotation cleanup

- [ ] Design an explicit API/CLI operation for users who no longer need migration
  recovery metadata. Do not remove annotations automatically after migration;
  users may still need rollback, and historical values can become stale.
- Classify provenance, invocation identity, backup data, and bundle metadata by
  resource and consumer. Determine which fields can be removed safely and how
  removing them affects rollback. Preserve metadata still used by controllers.
- Define dry-run output and explicit user acknowledgment for any loss of recovery
  capability. Test resource ownership checks, repeated cleanup, and preservation
  of active workloads and controller-owned metadata.
- Keep this operation distinct from the existing cleanup of OLMv0 conflicts.

Source: [annotation retention discussion](https://github.com/operator-framework/library-olm/pull/93#discussion_r4556715778).

### Align owner labels with upstream controllers

- [ ] Revisit `LabelOwnerKind`, `LabelOwnerName`, and managed-by labels once
  operator-controller/object-controller settle their requirements.
- Until then, retain alignment with the pinned controller on COD metadata, COD
  template metadata, and directly created COS resources. Do not remove labels
  preemptively.
- Coordinate any change with adoption, resource discovery, rollback, and cleanup
  tests so removing a label does not break identity or ownership checks.

Sources: [COD labels](https://github.com/operator-framework/library-olm/pull/93#discussion_r4556739359),
[template labels](https://github.com/operator-framework/library-olm/pull/93#discussion_r4556747596),
[COS labels](https://github.com/operator-framework/library-olm/pull/93#discussion_r4556851701).

### Shared revision content conversion and typed construction

- [ ] Centralize conversion from supported bundle formats and collected migration
  resources to `ClusterObjectSetContent` in library-olm. Migration's `PhaseSort`
  and operator-controller's internal phase construction currently duplicate
  ordering and content-conversion logic.
- Define a reusable converter with explicit inputs, shared phase-ordering tests,
  and adapters for both callers. Keep controller reconciliation, migration
  ownership, and Secret persistence outside the converter.
- [ ] Replace the apply-configuration JSON marshal/unmarshal round-trip with a
  consistent typed-object construction path. Address phase sorting and Secret
  packing together rather than adding a manual conversion layer solely at the
  COD constructor. Assess changes to exported APIs such as `PhaseSort` explicitly.
- Preserve shared builders for server-side dry-run and actual creation, and test
  inline manifests, packed references, optional fields, labels, and annotations.

Sources: [shared converter](https://github.com/operator-framework/library-olm/pull/93#discussion_r4554296554),
[typed construction](https://github.com/operator-framework/library-olm/pull/93#discussion_r4556794570).

Rollback persists verified resource identities and UIDs on the restored Subscription before deleting the CE backup. Remigration includes still-unowned retained resources (including catalog-generated RBAC) omitted from the OLMv0 inventory; missing resources are skipped, replacements and newly controlled resources are rejected, and normal collection is deduplicated.


### Alternative COS + COD handoff

This draft evaluates COS + COD instead of COD-only staging. Create an adoptable
initial COS with `None`, waits for it to complete adoption and become ready, then
creates an adoptable COD with `Prevent`. The
COD adopts the initial revision and creates a protected staging revision; the CE
then adopts the same COD and rolls out catalog content. No permissive phase or
object override may remain in the COD template. Failure after initial COS creation
preserves staging resources rather than restarting OLMv0 automatically.

The focused handoff suite covers the additional staging revision and a negative
rollout collision. It remains part of the existing coverage-enabled handoff CI
jobs. The direct-COS fallback inherited from the base remains unchanged; choosing
the handoff protocol and removing the fallback are separate decisions.

Recovery before CE creation must not rely only on an in-memory backup. Persist the
original Subscription and OperatorGroup specs on retained staging resources at
creation, before rollout/handoff failures can strand the operator. Preserve them
on failed or uncertain handoffs and refuse automatic OLMv0 restart. Regression
tests must decode the server-side backup after discarding the original plan and
verify it can restore the original Subscription spec after safe target release.

Revision collision observations during COD handoff are retried within the existing rollout timeout. Only current-generation completion and availability allow migration to continue; persistent collisions fail with the controller diagnostic and preserve staged recovery resources.
