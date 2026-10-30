# Migration

## Upgrading to v1.2.0 from v1.1.0

There is no API change. Two operational differences require attention:

1. **Kustomize installs now ship a NetworkPolicy.** Ingress to the controller
   is restricted: the metrics endpoint (`:8443`) only accepts scrapes from
   namespaces labeled `metrics: enabled`. If you scrape `/metrics` with
   Prometheus (or any collector) running in an unlabeled namespace, scraping
   stops after the upgrade. Label the namespace before upgrading:

   ```sh
   kubectl label namespace <monitoring-namespace> metrics=enabled
   ```

   Helm installs are unaffected (the chart ships no NetworkPolicy).
2. **The chart's DaemonSet now requests CPU and memory by default** instead of
   running BestEffort. On very small or fully allocated nodes this can change
   scheduling; adjust `nodeInterface.resources` in your values if needed.

The release also moves the toolchain to Go 1.27 and the controller libraries to
kubernetes 0.37 / controller-runtime 0.25. No CRD, status, or values changes
are required.

## Migration to v1.0.0 from v0.3.0

The v1.0.0 production-safe orchestration release is a breaking API change. It does
not provide conversion webhooks or compatibility aliases for v0.3.0 objects,
status fields, or Helm values.

Before upgrading, record the current provider owner and stop any automation
that writes the old `desiredNode`, `preparedNode`, or `assignedNode` fields.
The new release persists `transitionID`, `phase`, `sourceNode`, `targetNode`,
`providerObservedOwner`, and `localOwners` instead. The release may retain the
existing provider route during migration, but a handoff must be treated as a
controlled operation because local `/32` preparation is re-established before a
provider mutation.

Migration procedure:

1. Export the existing `FailoverIp` resources and provider credentials.
2. Install the new CRDs and operator release during a maintenance window.
3. Delete the incompatible v0.3.0 `FailoverIp` resources.
4. Recreate them with `spec.ip`, `spec.secretName`, the new safety policy, and
   any reusable `FailoverProbe` references you need.
5. Reapply node annotations and verify `Ready`, `Stabilizing`,
   `TargetPrepared`, `ProviderConverged`, `TrafficVerified`, and
   `OwnershipConverged` Conditions before sending production traffic.

Downtime and a temporary degraded/blocked state are possible if the provider
owner, node annotations, credentials, probes, or local interface preparation
cannot be verified. Do not delete the old resources and the provider route at
the same time without an operator-approved rollback plan.

To roll back the operator binary, first stop new transitions, verify the
provider owner, and restore the previously exported v0.3.0 manifests. The new
resources and status cannot be downgraded in place.
