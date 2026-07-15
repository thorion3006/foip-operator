# Probe examples

`../helm/values-node-health-only.yaml` is the smallest Helm values example. It
creates a FailoverIp with no external probe, so node health is the only health
signal. `../helm/values-safe.yaml` is a complete Helm values example. It deliberately
includes four packaging contracts:

- `node-health-only` has no required probe and can be used before an application
  endpoint exists.
- `tcp-service` checks a TCP port.
- `https-service` checks an HTTPS readiness endpoint and a 2xx range.
- `kubernetes-service` checks a Kubernetes Deployment readiness field.

The three probes are composed with `probeComposition: All`. Replace the
`.example.invalid` target and the placeholder failover IP before applying this
example. The chart only renders resources; it does not prove that a network
endpoint, Kubernetes object, or netcup API is reachable.

Render it without contacting a cluster:

```sh
helm template foip-operator charts/foip-operator \
  -f examples/helm/values-safe.yaml
```

Node eligibility still requires the documented netcup server and primary-MAC
annotations.
