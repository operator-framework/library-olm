# OLMv0 → OLMv1 Migration

A `v0` Go library plus two CLIs that migrate operators from **OLMv0** (`Subscription` /
`ClusterServiceVersion` / `CatalogSource`) to **OLMv1** (`ClusterExtension` /
`ClusterObjectSet` / `ClusterCatalog`) management — with minimal downtime and full
rollback at every step.

> Status: upstream **prototype** for
> [OCPSTRAT-2693](https://redhat.atlassian.net/browse/OCPSTRAT-2693). The library is
> versioned `v0` — breaking API changes are expected. Binary packaging, Console UI, and
> Hosted Control Planes are out of scope (see [requirements.md](requirements.md) §R10).

## Why

With OLMv1 reaching general availability, cluster administrators need a supported path to
move operators currently managed by OLMv0 onto OLMv1. Without tooling, adoption means
manually decommissioning and reinstalling each operator — risky, error-prone, and likely
to cause downtime or data loss for workloads that depend on those operators. This tooling
automates the transition, reduces operational risk, and accelerates OLMv1 adoption.

## The two CLIs

Catalogs are migrated **first**, then operators — the operator tool refuses to migrate an
operator whose package is not served by a `ClusterCatalog`.

### `migrate-catalogs-v0-to-v1`
Migrates OLMv0 `CatalogSource` resources to OLMv1 `ClusterCatalog` resources.

| Flag | Purpose |
|---|---|
| `--dry-run` | Print what would be created without modifying the cluster |

### `migrate-operators-v0-to-v1`
Migrates OLMv0 `Subscription`/`CSV` installations to OLMv1 `ClusterExtension`/`ClusterObjectSet`.

Every action is a verb subcommand that takes either an operator name **or** `--all`
(kubectl/`oc`-style). Because the target is an argument, an operator literally named
`check` or `convert` is unambiguous (`convert check`).

| Command | Target | Purpose |
|---|---|---|
| `check <operator> \| --all` | Subscription(s) | Report readiness, compatibility, and four-state classification (no changes). `--all` scans the whole cluster. |
| `convert <operator> \| --all` | Subscription(s) | Perform the migration. `--dry-run` previews without changing the cluster. `--all` prints the four-section summary and converts every Eligible operator. |
| `rollback <ce-name> \| --all` | ClusterExtension(s) | Restore an operator to OLMv0 management. |
| `cleanup <ce-name> \| --all` | ClusterExtension(s) | Finish a partial migration (Conflict state). |

Common flags: `-n/--namespace` (Subscription namespace), `--all`, `--dry-run` (on
`convert`), `--continue-on-error` (on `convert --all`), and the `--acknowledge-*` override
flags. Note the target differs by phase: `check`/`convert` act on a `Subscription`
(name + `-n` namespace); `rollback`/`cleanup` act on the resulting `ClusterExtension`.

The tool is **non-interactive**: there are no prompts. `convert --dry-run` is the preview
mechanism, and every risk override is an explicit `--acknowledge-*` flag (see
[requirements.md](requirements.md) §R3).

## Operator states

`migrate-operators-v0-to-v1 check --all` classifies every OLMv0 `Subscription` into one of four states (and `convert --all` prints the same summary before migrating the Eligible ones):

| State | Meaning | Action |
|---|---|---|
| **Eligible** | Passes all readiness & compatibility checks; package available in a `ClusterCatalog`; no `ClusterExtension` yet | Migrate |
| **Ineligible** | Fails one or more checks | Report the specific reason; skip (override with the matching `--acknowledge-*` flag) |
| **AlreadyMigrated** | No `Subscription`, but a `ClusterExtension` annotated `migrated-from-subscription` exists | Report as done; skip |
| **Conflict** | Both a `Subscription` **and** an annotated `ClusterExtension` exist — indicates a failed cleanup | Warn prominently; block; resolve with `cleanup` or `rollback` |

## High-level flow

```
migrate-catalogs-v0-to-v1        (prerequisite: CatalogSource -> ClusterCatalog)
        │
        ▼
check ──────────────► convert ─────────────────────────────────────► cleanup
 (classify / compat;   │  (--dry-run to preview)                        (delete Sub/CSV,
  --all to scan)       │                                                 OperatorGroup if last)
                               ├─ profile Subscription/CSV/InstallPlan
                               ├─ resolve target ClusterCatalog (by image)
                               ├─ back up Subscription and OperatorGroup specs (CE annotations)
                               ├─ collect owned resources (5 sources, dedup)
                               ├─ create ClusterObjectSet (SecretPacker, IfNoController)
                               │     └─ wait for COS controller: Succeeded=True
                               └─ create ClusterExtension (adopts the COS)
                               ▲
                               └── rollback: delete CE+COS (orphan cascade), restore Subscription
```

Workloads keep running throughout when the install namespace is unchanged — only the
management plane changes (**close-to-zero downtime**). When a namespace change is required,
some downtime is unavoidable while the deployment restarts in the new namespace, but it is
minimized.

## Architecture at a glance

- **Standalone `v0` module**, two binaries, prow + GitHub Actions CI.
- Targets OLMv1's `ClusterObjectSet` (COS) — the current name for the revision object
  (formerly `ClusterExtensionRevision`).
- The migration tool **creates** the COS and **waits** for the COS controller to set
  `Succeeded=True`; it never writes status on OLMv1 APIs. It then creates the
  `ClusterExtension`, which adopts the COS via owner labels.
- Collected objects are stored via boxcutter's **SecretPacker** (not inline) to support
  large bundles, with `CollisionProtection: IfNoController` so OLMv1 can adopt existing
  resources (including CRDs) without conflict.

## Prototype lineage

- [joelanford/operator-controller `0-to-1`](https://github.com/joelanford/operator-controller/tree/0-to-1) — single-file proof of concept
- [perdasilva/operator-controller `migration`](https://github.com/perdasilva/operator-controller/tree/migration) — the base this work is ported from

## Documents

| Doc | Contents |
|---|---|
| [requirements.md](requirements.md) | Functional/architectural requirements, eligibility rules, per-field handling for every Subscription, OperatorGroup, and CatalogSource field, and the ClusterExtension/ClusterCatalog target mappings |
| [plan.md](plan.md) | The eight implementation phases |
| [validation.md](validation.md) | Verifiable acceptance criteria and a requirement-traceability table |
