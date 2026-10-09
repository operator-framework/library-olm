# Kind migration demo

Run these scripts from the repository root. They use a dedicated
`library-olm-demo` Kind cluster and only the Kind-generated kubeconfig at
`.kubeconfig/library-olm-demo`; they do not use your current Kubernetes context.
The setup reuses the E2E version pins (Kind v0.33.0, Kubernetes v1.36.1,
OLMv0 v0.46.0, and experimental operator-controller v1.12.0), builds both CLIs,
and migrates the bootstrap CatalogSource so operator checks can resolve a
serving ClusterCatalog. It is safe to rerun without recreating the cluster.

Prerequisites: Go, Docker, `kubectl`, `curl`, and `sha256sum`; the `make` target
installs the pinned Kind binary through Bingo. Release manifests and container
images must be reachable. Allow several minutes for OLM and operator installation.

```bash
hack/demo/migration/setup.sh
hack/demo/migration/install.sh ecr-secret-operator
hack/demo/migration/check.sh ecr-secret-operator
hack/demo/migration/migrate.sh --dry-run ecr-secret-operator
hack/demo/migration/migrate.sh ecr-secret-operator
hack/demo/migration/teardown.sh
```

The install script also accepts `redis-operator` or
`external-secrets-operator`. Call it once for each desired operator; it refuses
to replace an existing namespace. `check.sh` with no argument (or `all`) runs
the batch scan. `migrate.sh operator [operator ...]` converts named operators
in order and stops on the first failure. `migrate.sh --all` uses the CLI's batch
mode and converts **every eligible Subscription in the demo cluster**, not only
the three named demo operators. `--continue-on-error` is available with `--all`.
Use `--dry-run` for a non-mutating preview and `--output jsonl` on check or
migration for structured CLI output; for example:

```bash
hack/demo/migration/install.sh redis-operator
hack/demo/migration/check.sh --output jsonl redis-operator
hack/demo/migration/migrate.sh --dry-run --output jsonl redis-operator
hack/demo/migration/migrate.sh --all --continue-on-error
```

For manual inspection, use `kubectl --kubeconfig .kubeconfig/library-olm-demo`.
The teardown script deletes only `library-olm-demo` and leaves the kubeconfig
file in place; setup regenerates it on the next run.
