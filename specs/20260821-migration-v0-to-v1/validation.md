# Validation — OLMv0 → OLMv1 Migration

Verifiable acceptance criteria drawn from the RFC,
[OCPSTRAT-2693](https://redhat.atlassian.net/browse/OCPSTRAT-2693), and
[requirements.md](requirements.md). Each item is phrased so it can be asserted in a unit or
E2E test. IDs (`V*`) are referenced by the traceability table at the end.

Unit tests use `sigs.k8s.io/controller-runtime/pkg/client/fake`; E2E runs on a kind cluster
with both OLMv0 and OLMv1 installed.

## V1. Per-command behavior

- **V1.1** `check <operator> -n <ns>` on a healthy AllNamespaces operator with an available catalog reports all checks green and exits 0; makes no cluster changes.
- **V1.2** `convert <operator> -n <ns> --dry-run` lists every resource that would be migrated (grouped by kind), including CRDs, and reports them as COS objects with `CollisionProtection: IfNoController`; makes no cluster changes.
- **V1.3** `convert <operator> -n <ns>` results in a `ClusterExtension` reaching `Installed=True`; the `Subscription` and `CSV` are deleted; CRDs remain and are adopted by OLMv1; the COS reached `Succeeded=True` **before** the CE was created.
- **V1.4** `rollback <ce-name> --acknowledge-installed` deletes the CE and COS (orphan cascade), recreates the `Subscription` from the backup annotation, and the operator returns to OLMv0 management (`AtLatestKnown`/`UpgradePending`). Without `--acknowledge-installed` on an `Installed=True` CE, rollback refuses and exits non-zero.
- **V1.5** `cleanup <ce-name>` on a Conflict state deletes the `Subscription` and OLMv0 artifacts (Operator CR, OperatorCondition, copied CSVs, OperatorGroup if last) and leaves the CE intact.
- **V1.6** `check --all` and `convert --all` print sections in order Conflict → Ineligible → AlreadyMigrated → Eligible; Conflicts are never auto-migrated; `convert --all` migrates only the Eligible operators.
- **V1.7** `convert --all` stops on the first failure by default; with `--continue-on-error` it logs the failure and continues, exiting non-zero if any operator failed.
- **V1.8** No command ever blocks on interactive input. An operator whose name collides with a verb (e.g. `check`) is migratable as a positional argument (`convert check -n <ns>`).

## V2. Eligibility matrix (one case per check, R3)

For each, a fixture operator produces the expected state + reason, and setting the override
flag flips it to `Eligible`:

- **V2.1 (C1)** OperatorGroup with `targetNamespaces` → Ineligible "watch scope"; `--acknowledge-watch-scope-change` → Eligible (migrates to AllNamespaces).
- **V2.2 (C2, hard)** CSV with `olm.package.required` → Ineligible "dependencies"; no override.
- **V2.3 (C2, hard)** CSV with `olm.gvk.required` → Ineligible "dependencies"; no override.
- **V2.4 (C3, hard, temporary)** CSV with owned APIServices → Ineligible "apiservices"; no override. Removed entirely when OPRUN-4723 merges.
- **V2.5 (C4)** OperatorCondition with `status.conditions` entries → Ineligible "operator-condition"; `--acknowledge-operator-condition` → Eligible.
- **V2.6 (C5)** CSV `.clusterPermissions` granting `operators.coreos.com/subscriptions` → Ineligible "olmv0-api-access"; `--acknowledge-olmv0-api-access` → Eligible.
- **V2.7 (C6)** OperatorGroup with `serviceAccountName` → Ineligible "scoped serviceaccount"; `--acknowledge-scoped-serviceaccount` → Eligible.
- **V2.8 (C7, hard)** Package absent from all ClusterCatalogs → Ineligible "package not found; run migrate-catalogs-v0-to-v1 first"; no override.
- **V2.9 (C8)** CSV not `Succeeded` or Subscription not at `AtLatestKnown`/`UpgradePending` → Ineligible "not at steady state"; `--acknowledge-not-steady-state` → Eligible.
- **V2.10 (C9, hard)** Subscription carries `olm.generated-by` → Ineligible "OLMv0-managed dependency"; no override.

## V3. Field-mapping assertions (R4/R6/R7/R8)

- **V3.1** Manual approval → `CE.spec.source.catalog.version` pinned to the installed version.
- **V3.2** Automatic approval → CE version unset (channel-based upgrades allowed).
- **V3.3** Subscription `spec.channel` → single-element `CE.spec.source.catalog.channels`; empty channel → omitted.
- **V3.4** `CE.spec.source.catalog.selector` pins to the resolved catalog via `olm.operatorframework.io/metadata.name`.
- **V3.5** `CE.spec.serviceAccount` is never set (deprecated/ignored), even when the OperatorGroup had a `serviceAccountName`.
- **V3.6** CE carries `migrated-from-subscription`, `migration-subscription-backup`, `migration-operatorgroup-backup`, and one `acknowledged-<flag>` annotation per flag used.
- **V3.7** OperatorGroup deleted only when both `--delete-operatorgroup` is passed AND no other Subscriptions remain in the namespace; left in place otherwise.
- **V3.8** CatalogSource `spec.image` → `ClusterCatalog.spec.source.image.ref`; ClusterCatalog `metadata.name` follows the deduplication strategy (V3.13/V3.14).
- **V3.9** CatalogSource `registryPoll.interval` → `pollIntervalMinutes` (integer minutes); dropped when the image ref is a digest.
- **V3.10** CatalogSource `priority` carried to ClusterCatalog `priority`.
- **V3.11** A `configmap`/`internal`/address-only CatalogSource is reported not-migratable and skipped.
- **V3.12** Subscription `spec.config` maps to `CE.spec.config.inline.deploymentConfig` with all sub-fields except `selector`; the operator Deployment reflects env/envFrom/resources/tolerations/nodeSelector/affinity/volumes/volumeMounts/annotations, and still does after an OLMv1-driven upgrade. A `spec.config.selector`, if present, is dropped (with a warning).
- **V3.13** Two CatalogSources in different namespaces share a name **and** the same image → `migrate-catalogs-v0-to-v1` creates a single `ClusterCatalog` using that name; both Subscriptions' CE selectors resolve to the same ClusterCatalog.
- **V3.14** Two CatalogSources in different namespaces share a name **but** have different images → `migrate-catalogs-v0-to-v1` creates two ClusterCatalogs named `<name>-<namespace-A>` and `<name>-<namespace-B>`; each Subscription's CE selector resolves to the correct ClusterCatalog by image match.
- **V3.15** CatalogSource `spec.priority` value outside `int32` range → reported as not migratable and skipped by default; with `--acknowledge-priority-overflow` the value is capped at `math.MaxInt32` / `math.MinInt32` and migration proceeds.
- **V3.16** A pre-existing `ClusterCatalog` whose `spec.source.image.ref` matches the CatalogSource image is adopted (not duplicated); `migrate-catalogs-v0-to-v1` reports it as already covered and sets `olm.operatorframework.io/migrated-from-catalogsource: <namespace>/<name>` on the ClusterCatalog if the annotation is not already present. Running `migrate-catalogs-v0-to-v1` against the Red Hat default catalogs (which already exist as ClusterCatalogs) produces no new ClusterCatalogs and leaves the existing annotations unchanged.
- **V3.17** Two CatalogSources from different namespaces both map to the same ClusterCatalog (same image) → the `migrated-from-catalogsource` annotation is set exactly once by whichever CatalogSource is processed first; subsequent processing of the other CatalogSource leaves the annotation unchanged.
- **V3.18 (R2.6)** `convert <operator> -n <ns> --backup <directory>` writes four files before any deletions: `subscription.yaml`, `operatorgroup.yaml`, `clusterserviceversion.yaml`, and `installplans/` (one YAML per InstallPlan); directory is created if it does not exist; migration proceeds if directory write fails (backup is informational only).
- **V3.19 (R1.2)** `migrate-catalogs-v0-to-v1 --delete-catalogsource` deletes the CatalogSource only when both conditions are met: the flag is passed **and** no Subscription references it; when one or both conditions are not met, the CatalogSource is left in place.

## V4. Edge-case tests (R9)

- **V4.1** Two operators in one namespace: migrating one leaves the OperatorGroup intact.
- **V4.2** Two operators sharing a CRD: `IfNoController` lets the second adopt without a collision error.
- **V4.3** Operator not at steady state → Ineligible (C8) with a clear reason.
- **V4.4** Dependency operator (`olm.generated-by` present / declares requirements) → flagged; an operator others depend on migrates but emits a dependents warning.
- **V4.5** OperatorCondition disambiguation: an operator with OLMv0-stamped OperatorCondition RBAC but **empty** `status.conditions` is **Eligible** (RBAC is not treated as usage).
- **V4.6** Large bundle exceeding inline size limits migrates successfully via SecretPacker.
- **V4.7** Namespace change copies `pod-security.kubernetes.io/*` and `security.openshift.io/scc.podSecurityLabelSync` to the new namespace; old namespace deleted only with `--acknowledge-namespace-delete`.

## V5. End-to-end scenario (kind)

- **V5.1** Bootstrap kind with OLMv0 + OLMv1 side-by-side.
- **V5.2** Install an AllNamespaces operator via an OLMv0 Subscription; confirm healthy.
- **V5.3** `migrate-catalogs-v0-to-v1` → CatalogSource becomes a serving ClusterCatalog.
- **V5.4** `check <operator> -n <ns>` → all green (catalog now found).
- **V5.5** `convert <operator> -n <ns> --dry-run` → lists resources incl. CRDs (`IfNoController`).
- **V5.6** `convert <operator> -n <ns>` → CE `Installed=True`; Subscription/CSV deleted; CRDs adopted (demonstrates close-to-zero downtime with namespace unchanged).
- **V5.7** Upgrade via OLMv1 → CRDs updated through the normal bundle lifecycle.
- **V5.8** `rollback <ce-name> --acknowledge-installed` → Subscription restored, CE deleted.
- **V5.9** Fresh cluster with operators in all four states → `check --all` → correct four-section output; `convert --all` warns on Conflicts and migrates only Eligible.

## V6. Non-functional (Jira deployment considerations)

- **V6.1** Works on all node topologies: SNO, compact (3-node), and multi-node.
- **V6.2** No architecture-specific behavior (x86_64, aarch64, ppc64le, s390x).
- **V6.3** Works connected and in restricted networks (catalog resolution by package name against pre-mirrored catalogs).
- **V6.4** Only AllNamespaces install mode is produced (Own/Single converted with acknowledgment).
- **V6.5** Self-managed, classic (standalone) clusters. HCP explicitly not covered (R10).

## V7. Deliverable checks

- **V7.1** Repo at `/home/tshort/git/operator-framework/library-olm` with a pushed public personal remote; `specs/20260821-migration-v0-to-v1/` holds README, requirements, plan, validation.
- **V7.2** `requirements.md` covers every Subscription (R4), OperatorGroup (R6), and CatalogSource (R8) spec field, plus the full ClusterExtension target mapping (R7).
- **V7.3** `plan.md` has 8 phases + the cross-repo prerequisite, each with an exit criterion.
- **V7.4** This traceability table has no requirement without at least one validation item.

---

## Traceability

| Requirement | Validated by |
|---|---|
| R1.1 Library API | V1.1–V1.7 (via library calls), V3.*, V4.* |
| R1.2 Two CLIs | V1.*, V3.8–V3.11, V3.13–V3.19, V5 |
| R1.3 Four-state classification | V1.6, V2.1–V2.10, V5 |
| R1.4 `--all` ordering | V1.6 |
| R1.5 Batch failure / `--continue-on-error` | V1.7 |
| R1.6 Non-interactive | V1.8 |
| R1.7 Downtime posture | V5.6 (namespace unchanged), V4.7 (namespace change) |
| R1.8 Recovery | V1.4, V1.5 |
| R2.1 v0 module + CI | V7.1, V7.3 |
| R2.2 ClusterObjectSet rename | V1.3 (COS created), V4.6 |
| R2.3 Wait for COS Succeeded; no status writes | V1.3 |
| R2.4 SecretPacker + IfNoController | V1.2, V4.2, V4.6 |
| R2.5 CE annotations | V3.6 |
| R2.6 `--backup <directory>` flag | V3.18 |
| R2.7 Boxcutter phase 2 | Prerequisite note (PLAN) |
| R3 C1–C9 | V2.1–V2.10 |
| R4 Subscription fields | V3.1–V3.3, V3.12, V4.4 |
| R5 Resource collection strategy | V1.3, V4.2, V4.6 |
| R6 OperatorGroup fields | V2.1, V2.7, V3.5, V3.7 |
| R7 ClusterExtension mapping | V3.1–V3.6, V3.12 |
| R8 CatalogSource→ClusterCatalog | V3.8–V3.11, V3.13–V3.17, V3.19 |
| R9 Edge cases | V4.1–V4.7 |
| R10 Non-goals | V6.4, V6.5 |
