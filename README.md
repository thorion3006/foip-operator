# foip-operator

This operator manages a netcup failover IP with a persisted, restart-safe state
machine. It selects among annotated Kubernetes nodes, verifies candidate readiness
before touching the provider, supports optional reusable probes, and preserves
ownership across controller restarts or leader changes.

The project is built in Go using [kubebuilder](https://book.kubebuilder.io/) and the
[netcup SCP REST API](https://www.netcup.com/en/helpcenter/documentation/servercontrolpanel/api).

This repository was developed with assistance from AI tools. Human review is still
expected for changes that affect production behavior, security, or release artifacts.

The security workflow intentionally allows two Trivy misconfiguration exceptions
that are required by the current architecture: the node-interface DaemonSet uses
`hostNetwork: true` so it can manage the host `/32`, and the controller reads the
runtime Netcup credentials from a Kubernetes Secret. These are deliberate
exceptions, not oversights.

For internals, controller flow, limitations, and edge cases, see [ARCHITECTURE.md](ARCHITECTURE.md).
Release behavior and the breaking API summary are in the [v1.0.1 GitHub release notes](https://github.com/thorion3006/foip-operator/releases/tag/v1.0.1).

## Fork Acknowledgement

This repository is a fork of [niklasbeierl/foip-operator](https://github.com/niklasbeierl/foip-operator).
Thanks to Niklas Beierl for the original project.

## Usage

Recommended install path: **Helm**.

- Use Helm if you want the packaged release, chart values, and observability toggles
- Use Kustomize if you want raw manifests, custom overlays, or to wire the resources into an existing GitOps flow

### Prerequisites

- A Kubernetes cluster
- `kubectl` configured to access it
- `helm` v3
- A netcup account with at least one failover IP and the [SCP Webservice](https://helpcenter.netcup.com/en/wiki/server/scp-webservice/) activated

### 1. Credentials

Generate an OAuth2 offline refresh token using the included helper:

```sh
go run ./cmd/netcup-auth/ --namespace <namespace> --secret-name netcup-scp-credentials
```

This opens a browser for the netcup device login flow, then prints a ready-to-paste
`kubectl create secret` command containing `userId` and `refreshToken`.

### 2. Node annotations

The operator identifies which netcup server each Kubernetes node corresponds to via
two annotations. When you view your server in the SCP UI you can get the server ID
from the URL and the primary MAC in the network section. Then you can annotate your
nodes like this:

```sh
kubectl annotate node <nodename> \
  foip.noshoes.xyz/server-id=<integer-server-id> \
  foip.noshoes.xyz/primary-mac=<mac-address>
```

Only nodes with both annotations are considered for assignment.

The controller uses those annotations to choose the target node, and the node-interface
workload uses the MAC address to manage the local `/32`.


### 3. Install the chart

```sh
helm install foip-operator oci://ghcr.io/thorion3006/foip-operator \
  --namespace <namespace> \
  --version <version> \
  -f my-values.yaml
```

Helm unfortunately can't list available versions from oci registries yet, but you can
for example use skopeo.

```sh
skopeo list-tags docker://ghcr.io/thorion3006/foip-operator
```

To get the default values:

```sh
helm show values oci://ghcr.io/thorion3006/foip-operator --version <version>
```

### Install with Kustomize

If you prefer raw manifests or want to manage the deployment with your own overlays,
use the Kustomize targets:

```sh
export IMG=ghcr.io/thorion3006/foip-operator/operator:<version>
make install
make deploy IMG=$IMG
```

If you want a single rendered manifest bundle for GitOps or offline use:

```sh
make build-installer IMG=$IMG
kubectl apply -f dist/install.yaml
```

### 4. Create a FailoverIp resource (Optional)

If you don't want to specify your foip in the helm values, you can create it manually. 
In that case you need to grant the controller service account access to the secret, 
either with a new Role and RoleBinding or by adding its name to `existingSecrets` in the 
helm values.

```yaml
# failoverip.yaml
apiVersion: foip.noshoes.xyz/v1
kind: FailoverIp
metadata:
  name: my-failover-ip
  namespace: <namespace>
spec:
  ip: 1.2.3.4
  secretName: netcup-scp-credentials
  # Optional safety knobs:
  # recoveryPolicy: HoldDualOwnership
  # probeComposition: All
  # probes:
  #   - shared-http-check
```

```sh
kubectl apply -f failoverip.yaml
```

### Checking status

```sh
kubectl describe foip my-failover-ip
```

Inspect `status.phase`, `status.transitionID`, `status.sourceNode`,
`status.targetNode`, `status.providerObservedOwner`, `status.localOwners`, and the
`Ready`, `Stabilizing`, `TargetPrepared`, `ProviderConverged`,
`TrafficVerified`, and `OwnershipConverged` Conditions. A successful handoff
requires exactly one reported local owner; `Degraded` and `Blocked` are explicit
safety states, not transient log messages.

Set `spec.recoveryPolicy` to `HoldDualOwnership` (the safe default),
`RollbackProvider`, `CommitDegraded`, or `ManualIntervention` to choose the
post-route probe failure behavior.

`FailoverProbe` resources are optional and ingress-neutral. Use `TCP`, `TLS`,
`HTTP`, `HTTPS`, or `Kubernetes` probes with `All`, `Any`, or quorum
composition. Network targets may use `${targetNodeIP}` or `${failoverIP}`;
explicit DNS names are also supported. TLS/HTTPS probes use certificate
verification by default and may reference a PEM CA bundle through
`spec.caBundleSecretRef`.

Provider cooldown, candidate hysteresis, and stale-owner cleanup are persisted
in `FailoverIp` status. Tune `failureThreshold`, `recoveryThreshold`,
`cleanupMaxAttempts`, and `cleanupRetrySeconds` only when the default safety
budget is unsuitable; cleanup becomes `Degraded` when its bounded budget is
exhausted.

After correcting a blocked/degraded cause, request one controlled retry by
changing the reconciliation token:

```sh
kubectl annotate foip my-failover-ip \
  foip.noshoes.xyz/reconcile="$(date +%s)" --overwrite
```

Reusable `FailoverProbe` objects can be installed once and referenced from
multiple `FailoverIp` resources. The chart supports creating both kinds of
resources at install time with `failoverIps` and `failoverProbes`.

### Troubleshooting

Check logs across all operator pods:

```sh
kubectl logs -l app.kubernetes.io/name=foip-operator \
  --all-containers --prefix -f --max-log-requests 10
```

If you want to access LoadBalancer services (e.g. via klipper/ServiceLB/Cilium 
Node IPAM LB) through the failover IP, add it as an external IP to the service:

```yaml
# ...
spec:
# ...
  externalIPs:
    - 1.2.3.4
# ...
```

### Observability

The operator exposes its operational metrics on the existing `/metrics` endpoint,
so the controller Deployment can still be scraped by Prometheus through the
bundled `ServiceMonitor`.

Tracing is enabled through OpenTelemetry environment variables. Set an OTLP endpoint
from your deployment tooling and the binaries will export spans automatically:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.observability:4317
OTEL_SERVICE_NAME=foip-operator-foip
OTEL_RESOURCE_ATTRIBUTES=service.version=1.0.1,foip.component=foip
```

The Helm chart wires the service name and resource attributes by default for both
the controller and node-interface workloads. If you deploy with the raw Kustomize
manifests, add the same environment variables to your pod specs.

In the Helm chart you can also toggle observability features directly:

```yaml
observability:
  metrics:
    enabled: true
  traces:
    enabled: true
  otlp:
    endpoint: http://otel-collector.observability.svc.cluster.local:4317
    insecure: true
```

- Set `observability.metrics.enabled: false` to stop binding the `/metrics` port.
- Set `observability.traces.enabled: false` to skip OTLP export env wiring.
- Point `observability.otlp.endpoint` at any collector you want, with `insecure`
  controlling whether the chart injects `OTEL_EXPORTER_OTLP_INSECURE=true`.

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
