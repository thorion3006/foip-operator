# Development

## Nix development shell

The repository provides a Nix flake for `x86_64-linux` and `aarch64-linux`.

Enter the development environment manually:

```bash
nix develop
```

The shell includes:

- Go 1.26
- `gopls` and Go tools
- `golangci-lint`
- `kubectl`
- Helm
- Kind
- Kustomize
- `ko` and `skopeo`
- `direnv` and `nix-direnv`
- GNU Make, Git, `jq`, and `yq`

The repository-local `bin/` directory is added to `PATH`, allowing the Makefile to install and use pinned Kubebuilder tools such as `controller-gen` and `setup-envtest` without polluting the user profile.

## Direnv

The checked-in `.envrc` automatically loads the default flake development shell and reloads when `flake.nix` or `flake.lock` changes.

Install and enable `direnv` in your shell, then approve the repository once:

```bash
direnv allow
```

With `nix-direnv` enabled, entering the repository activates the cached Nix development environment automatically. Leaving the directory unloads it.

## Common workflows

```bash
make generate
make manifests
make fmt
make vet
make test
make lint
make helm-lint
```

Build the three binaries:

```bash
make build
```

Build the operator image using the fork default:

```bash
make docker-build
```

The default image is:

```text
ghcr.io/thorion3006/foip-operator/operator:0.3.0
```

## Observability

The manager Deployment and Helm chart expose Prometheus-scrapable metrics on
`/metrics`. Traces are exported through OpenTelemetry when you provide an OTLP
endpoint via environment variables such as `OTEL_EXPORTER_OTLP_ENDPOINT`.

The chart sets `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` for both the
controller and node-interface workloads. For raw kustomize installs, patch the
same env vars into `config/manager/manager.yaml` or your own overlay.

The chart also exposes `observability.metrics.enabled` and
`observability.traces.enabled` so you can switch off metrics or tracing without
changing the application image. `observability.otlp.endpoint` and
`observability.otlp.insecure` control the collector target.

The metrics are deliberately resource-independent. Useful generic alert rules
include `foip_failover_phase{phase="Blocked"} > 0` or
`increase(foip_provider_cooldown_block_total[10m]) > 0`; dashboards should use
phase, provider, operation, and outcome labels only. Transition spans use the
`foip.transition_id` attribute, so provider and probe child spans can be joined
without putting IP addresses, URLs, headers, or credentials into labels.

For a product-neutral deployment, scrape `/metrics` with Prometheus (or a
compatible collector), send OTLP traces to any OpenTelemetry Collector, and
route the two example expressions to the alerting system of your choice.

Package and publish Helm chart version `0.3.0`:

```bash
make helm-package
make helm-push
```

The default Helm OCI registry is:

```text
oci://ghcr.io/thorion3006
```

All release defaults can still be overridden per invocation:

```bash
make docker-build IMG=ghcr.io/example/foip-operator:test
make helm-package CHART_VERSION=0.3.0
make helm-push HELM_OCI_REPOSITORY=oci://ghcr.io/example
```

## Flake lock

After cloning, create or refresh the lock file with:

```bash
nix flake lock
```

Commit `flake.lock` so all contributors and CI use the same `nixpkgs` revision.
