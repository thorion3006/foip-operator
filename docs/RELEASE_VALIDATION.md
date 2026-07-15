# Release validation

The packaging gate is deterministic and does not need a Kubernetes cluster or
network access:

```sh
./hack/validate-packaging.sh
```

It lints and renders the Helm chart with both default and conservative example
values, checks node-health-only plus TCP/HTTPS/Kubernetes/composed resources,
and verifies safe defaults for replicas, disruption protection, pod security,
and Secret `resourceNames`. It also checks that the release workflow keeps
manifest-index metadata, validates one Linux `amd64` and one Linux `arm64`
entry, and packages/pushes the chart using the release version.

The release workflow runs this gate before the publish job. The workflow's
registry step additionally verifies the actual multi-architecture manifest by
digest and attaches SBOM/provenance attestations.

External-only checks remain external by design: registry authentication and
push/sign/attest operations, the live netcup API, real TCP/HTTPS endpoints,
Kubernetes API readiness, and a Kind deployment are not reproduced by the
offline packaging gate.
