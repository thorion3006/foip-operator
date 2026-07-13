# foip-operator

This operator monitors node objects and assigns a netcup failover IP to one of the 
"healthiest" nodes. It can be used as a "poor man's load balancer" for the control 
plane or services on a Kubernetes cluster running in netcup. The operator also ensures 
that each node's network interface is configured to receive traffic for the failover IP.

Built in Go using [kubebuilder](https://book.kubebuilder.io/) and the 
[netcup SCP REST API](https://www.netcup.com/en/helpcenter/documentation/servercontrolpanel/api).

This repository is a fork of [niklasbeierl/foip-operator](https://github.com/niklasbeierl/foip-operator).
Thanks to Niklas Beierl for the original project.

## Fork Differences

Compared with the original repository, this fork currently:

- Uses a make-before-break failover flow so the target node prepares before the route moves
- Builds a smaller `scratch` runtime image with CA certificates copied in
- Publishes multi-arch release images for both `linux/amd64` and `linux/arm64`
- Adds OCI image metadata, SBOM generation, provenance attestation, and cosign signing in the release workflow
- Prefers `rootless-podman`, then `podman`, then `docker` for local image builds
- Keeps the controller RBAC and manifests aligned with the current controllers and status updates
- Adds OpenTelemetry tracing hooks plus Prometheus-scrapable operational metrics for both controller binaries

## Motivation

I wanted a single point of contact for a cluster hosted in netcup, without adding extra 
nodes for "real" load balancers. As of writing, netcup does not offer managed load 
balancers. My solution is to automatically assign a failover IP to one of the 
"healthiest" nodes. This solves two problems:

1. It allows maintenance on individual nodes without paying too much attention to networking.
2. It recovers connectivity if the currently serving node becomes unhealthy or is lost.

## Limitations

Especially for problem 2, this approach is **not** the best solution. Failover only 
happens once the control plane detects that the node is unhealthy, which may take from
seconds to a few minutes. Rerouting the failover IP also takes a few seconds.

Another important consideration: **netcup failover IPs can only be re-assigned every 5 
minutes**.

## Project status

This project is *works-for-the-author*-grade software. I am happy to review PRs that 
improve it or add flexibility, but there are no guarantees.

I'd be open to adding support for other hosting providers with similar mechanisms, for
example Hetzner floating ips.

## Architecture

There are two controllers that run as separate workloads.

### foip controller (Deployment)

Monitors nodes and `FailoverIp` resources. Ensures the failover IP is routed to one of 
the "healthiest" nodes via the netcup SCP REST API. Runs with leader election so only 
one instance is active at a time.

### node-interface controller (DaemonSet)

Runs on every node. Checks whether a  failover IP should be routed to this node and 
ensures the IP is assigned to the correct network interface. This means **you don't have
to handle network configuration of your nodes manually**.

### Choosing the healthiest node

Several node conditions are checked and ordered by severity. The node with the 
fewest or least severe issues is chosen:

```
NetworkUnavailable=True    # Networking broken
Ready=False                # Node probably lost
Ready=Unknown              # Node probably lost
spec.unschedulable         # Cordoned
PIDPressure=True           # System resource pressure
MemoryPressure=True
DiskPressure=True
```

A node switch only happens if a strictly healthier node is available. 
Equal health does not lead to a switch (avoids alphabetical flip-flop).

### netcup REST Api

The netcup SCP REST API is documented [here](https://www.netcup.com/en/helpcenter/documentation/servercontrolpanel/api).

The new netcup rest api authenticates with OIDC. As of writing, the only way to get a 
long-lived credential for automation is to obtain a refresh token with the device login 
flow. Because that is not very ergonomic, this repo builds a small helper binary
`netcup-auth` (see below). The refresh token must be used at least once 
every 30 days to not expire. The controller should ensure this since it fetches 
the status of the failover ip for every reconcile and reconciles at least every 
24 hours.

## Installation

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
two annotations. When you view your server in the new SCP UI (in beta as of writing) you
can get the server id from the URL and the primary MAC in the network section.
Then you can annotate your nodes like this:

```sh
kubectl annotate node <nodename> \
  foip.noshoes.xyz/server-id=<integer-server-id> \
  foip.noshoes.xyz/primary-mac=<mac-address>
```

Only nodes with both annotations are considered for assignment.


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
kopeo list-tags docker://ghcr.io/thorion3006/foip-operator
```

To get the default values:

```sh
helm show values oci://ghcr.io/thorion3006/foip-operator --version <version>
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
```

```sh
kubectl apply -f failoverip.yaml
```

### Checking status

```sh
kubectl describe foip my-failover-ip
```

```
Spec:
  Ip:           1.2.3.4
  Secret Name:  netcup-scp-credentials
Status:
  Assigned Node:      node-1
  Desired Node:       node-1
  Last Sync Attempt:  2026-06-02T14:00:00Z
  Last Sync Success:  2026-06-02T14:00:01Z
```

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

The operator exposes its operational metrics on the existing `/metrics` endpoint, so
the controller Deployment can still be scraped by Prometheus through the bundled
`ServiceMonitor`.

Tracing is enabled through OpenTelemetry environment variables. Set an OTLP endpoint
from your deployment tooling and the binaries will export spans automatically:

```sh
OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector.observability:4317
OTEL_SERVICE_NAME=foip-operator-foip
OTEL_RESOURCE_ATTRIBUTES=service.version=0.2.2,foip.component=foip
```

The Helm chart wires the service name and resource attributes by default for both
the controller and node-interface workloads. If you deploy with the raw kustomize
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
