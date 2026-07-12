# Development

## Nix development shell

The repository provides a Nix flake for `x86_64-linux` and `aarch64-linux`.

Enter the development environment:

```bash
nix develop
```

The shell includes:

- Go 1.25
- `gopls` and Go tools
- `golangci-lint`
- `kubectl`
- Helm
- Kind
- Kustomize
- `ko` and `skopeo`
- GNU Make, Git, `jq`, and `yq`

The repository-local `bin/` directory is added to `PATH`, allowing the Makefile to install and use pinned Kubebuilder tools such as `controller-gen` and `setup-envtest` without polluting the user profile.

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

Build the operator image from the fork:

```bash
make docker-build
```

Package Helm chart version `0.2.0`:

```bash
make helm-package CHART_VERSION=0.2.0
```

## Flake lock

After cloning, create or refresh the lock file with:

```bash
nix flake lock
```

Commit `flake.lock` so all contributors and CI use the same `nixpkgs` revision.
