# v1.0.0

This v1.0.0 release replaces the v0.3.0 implicit ownership fields with a persisted
failover transition state machine. It is not compatible with v0.3.0 custom
resources or Helm values; recreate those resources as described in
[MIGRATION.md](MIGRATION.md).

Highlights:

- Durable phases cover selection, stabilization, local preparation, provider
  routing, provider and traffic verification, commit, and stale-owner cleanup.
- Provider mutations are fenced by observed ownership, persisted timestamps,
  cooldown, and bounded jittered retry delays.
- FailoverProbe resources support optional provider-neutral TCP, TLS, HTTP(S),
  and Kubernetes readiness checks with `All`, `Any`, and quorum composition.
- Probe execution fails closed for sensitive destinations by default, validates
  DNS results and redirects, limits response bodies, and keeps Secret values
  out of status, events, metrics, traces, and errors.
- Post-route failures support `HoldDualOwnership`, `RollbackProvider`,
  `CommitDegraded`, and `ManualIntervention` recovery policies.
- Conditions, events, OpenTelemetry correlation, and low-cardinality metrics
  expose transition, provider, probe, recovery, and ownership state.

The operator remains ingress-neutral. No Coraza, WAF, reverse proxy, or
application-specific integration is required, and node-health-only operation
remains supported by leaving `spec.probes` empty.
