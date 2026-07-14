# PRD: Failover Safety and Pluggable Health Verification

- **Document ID:** PRD-FOIP-SAFETY-001
- **Status:** Proposed
- **Target:** Post-v0.3.0 production-safety release
- **Repository:** `thorion3006/foip-operator`
- **Last updated:** 2026-07-14

## 1. Executive summary

`foip-operator` v0.3.0 implements make-before-break routing: select a target, add the `/32`, confirm preparation, move and verify the Netcup route, commit ownership, then remove stale addresses.

The next release must add a provider-neutral and ingress-neutral safety framework. It must not depend on Coraza, APISIX, NGINX, Traefik, HAProxy, Istio, Cilium ingress, or any other specific edge product. Consumers may use any of those, a custom service, a TCP application, or no L7 proxy.

Health verification must therefore be optional, composable and expressed through generic probe types.

## 2. Goals

- Prevent route flapping and Netcup cooldown violations.
- Persist an explicit, restart-safe failover state machine.
- Support optional node-local and externally observed health checks.
- Verify that the target is ready before routing and reachable after routing.
- Confirm that exactly one node owns the failover `/32` after convergence.
- Expose actionable Conditions, events, metrics and traces.
- Preserve safe behaviour when probes are disabled.

## 3. Non-goals

- Shipping or configuring an ingress controller, WAF or application proxy.
- Embedding Coraza-specific logic.
- Acting as a general Kubernetes Service load balancer.
- Replacing external monitoring systems.
- Supporting arbitrary executable hooks in the first release.

## 4. Personas

- **Minimal user:** wants failover based only on Kubernetes node health.
- **Service user:** wants TCP or HTTP readiness checks on each candidate node.
- **Edge user:** wants an HTTPS check with Host header and certificate validation.
- **Advanced operator:** wants several probes combined with quorum logic.
- **Platform integrator:** wants external verification from a separately deployed probe agent.

## 5. Functional requirements

### FR-1: Explicit transition state

Add persisted status fields sufficient to resume safely after controller restart:

```yaml
status:
  phase: PreparingTarget
  transitionID: 7c262f2b-...
  sourceNode: karna
  desiredNode: vikram
  preparedNode: vikram
  providerNode: karna
  assignedNode: karna
  transitionStartedAt: 2026-07-14T20:00:00Z
  lastRouteChangeAt: 2026-07-14T19:45:00Z
```

Supported phases:

`Stable`, `SelectingTarget`, `PreparingTarget`, `WaitingForCooldown`, `RoutingProvider`, `VerifyingProvider`, `VerifyingTraffic`, `CommittingOwnership`, `CleaningStaleOwners`, `Degraded`, `Blocked`.

**Acceptance criteria**

- Given a controller restart during any phase, when reconciliation resumes, then it continues or safely rolls back without losing source ownership.
- Given an unknown or inconsistent state, then the operator enters `Blocked` and does not issue another provider route change.

### FR-2: Provider cooldown and rate-limit policy

Add configurable policy fields:

```yaml
spec:
  failoverPolicy:
    minimumRouteChangeInterval: 5m
    stabilizationWindow: 30s
    failureThreshold: 3
    successThreshold: 2
```

The default minimum interval must match the provider-safe value documented by the project. Provider-reported retry information must take precedence when available.

**Acceptance criteria**

- Given a route change inside the minimum interval, then no provider mutation occurs and phase becomes `WaitingForCooldown`.
- Given repeated node flapping, then the operator performs at most one provider route change per configured interval.

### FR-3: Hysteresis and target stability

A candidate must remain strictly better than the current owner for `stabilizationWindow` and satisfy `successThreshold` before handoff begins. A transient failure must not immediately move the route.

Optional policy:

```yaml
spec:
  failoverPolicy:
    preferCurrentOwner: true
    minimumScoreImprovement: 1
```

### FR-4: Generic probe model

Add optional probes under the `FailoverIp` resource or through a referenced reusable resource.

Initial built-in probe types:

- `KubernetesNode`: Ready, NetworkUnavailable, pressure and cordon state.
- `TCP`: connect to address and port.
- `HTTP`: HTTP or HTTPS request with method, path, headers and expected status.
- `TLS`: handshake, server name and certificate validation.
- `KubernetesObject`: readiness or Conditions of a named object.
- `ExternalObservation`: consume signed or authenticated results from an optional probe agent.

Example:

```yaml
spec:
  probes:
    preRoute:
      - name: local-edge
        type: HTTP
        target:
          addressSource: CandidateNodeInternalIP
          port: 8443
        http:
          scheme: https
          path: /readyz
          host: edge.example.com
          expectedStatusCodes: [200]
          tls:
            serverName: edge.example.com
            insecureSkipVerify: false
        timeout: 3s
        period: 2s
        successThreshold: 2
        failureThreshold: 3
    postRoute:
      - name: public-service
        type: HTTP
        target:
          addressSource: FailoverIP
          port: 443
        http:
          scheme: https
          path: /healthz
          host: service.example.com
          expectedStatusCodes: [200, 204]
```

No probe type may assume a particular ingress or WAF implementation.

### FR-5: Probe composition

Support `All`, `Any` and `AtLeast` combination modes:

```yaml
spec:
  probePolicy:
    preRoute:
      mode: All
    postRoute:
      mode: AtLeast
      minimumSuccesses: 2
```

Empty probe lists must be valid and preserve the current Kubernetes-health-only behaviour.

### FR-6: Safe pre-route verification

Before changing the provider route, the node-interface controller must confirm local `/32` ownership and every required pre-route probe must pass.

On failure, the old owner remains assigned and the provider route is not changed.

### FR-7: Post-route verification and bounded recovery

After provider routing is verified, required post-route probes must pass before ownership is committed and stale addresses are removed.

Configurable failure action:

```yaml
spec:
  failoverPolicy:
    postRouteFailureAction: RetainDualOwnership
```

Allowed actions:

- `RetainDualOwnership` — default and safest.
- `RollbackProvider` — only when cooldown and provider policy permit.
- `CommitDegraded` — explicit opt-in.

The operator must never loop route changes indefinitely.

### FR-8: Single-owner convergence

After commit, each node-interface agent reports whether it currently owns the `/32`. Status must expose observed owners.

```yaml
status:
  observedOwners: [vikram]
```

`Stable=True` requires exactly one observed owner matching `assignedNode`. Multiple or zero owners produce a degraded Condition and periodic repair attempts without unnecessary provider route changes.

### FR-9: Conditions and events

Expose standard Conditions including:

- `TargetSelected`
- `TargetPrepared`
- `CooldownSatisfied`
- `ProviderRouteVerified`
- `TrafficVerified`
- `SingleOwnerConfirmed`
- `Stable`
- `Degraded`

Every transition and blocked action must create a Kubernetes event with a stable reason code.

### FR-10: Observability

Add metrics for:

- transitions by phase and result;
- suppressed route changes;
- cooldown wait duration;
- probe latency and results by probe type;
- provider convergence duration;
- dual-ownership duration;
- rollback attempts and results.

Trace spans must include transition ID, source node, target node, probe name and phase. Logs must not expose refresh tokens, authorization headers or secret payloads.

### FR-11: Reusable probe definitions

Support an optional namespaced `FailoverProbe` resource so multiple failover IPs can reference the same probe configuration. Inline probes remain supported for simple installations.

Secrets used for probe authentication must be referenced through `SecretKeySelector`; plaintext credentials must not appear in CRDs, status, logs or events.

### FR-12: Backward compatibility

Existing v0.3.0 `FailoverIp` objects must continue working without modification. New safety defaults must not create route churn during upgrade.

CRD conversion is required if a future API version changes field structure incompatibly.

## 6. Security requirements

- HTTP probes must support bounded response bodies and must not log bodies by default.
- Redirect following must default to disabled.
- Probe destinations must be validated to reduce SSRF risk; cluster administrators must be able to restrict allowed CIDRs and schemes.
- TLS verification defaults to enabled.
- Arbitrary command execution is excluded from the first implementation.
- RBAC for reusable probe secrets must remain namespace-scoped and least privilege.

## 7. Helm configuration

The chart must expose controller-wide safety ceilings without forcing probe configuration:

```yaml
safety:
  maximumConcurrentTransitions: 1
  defaultMinimumRouteChangeInterval: 5m
  maximumProbeTimeout: 10s
  allowedProbeSchemes: [http, https, tcp, tls]
  allowInsecureTLS: false
```

CRD-level settings may tighten but must not exceed administrator-defined ceilings.

## 8. Failure cases

Tests must cover:

- controller restart in every transition phase;
- node-interface agent restart after adding the `/32`;
- provider route mutation succeeds but verification times out;
- target passes pre-route probes but fails post-route probes;
- old owner disappears during transition;
- both source and target temporarily own the `/32`;
- no node reports ownership after provider movement;
- API throttling and retry responses;
- malformed probe configuration;
- TLS expiration and hostname mismatch;
- HTTP redirects, oversized bodies and slow responses;
- Kubernetes API outage during handoff;
- rapid alternating node health.

## 9. Testing requirements

- Unit tests for state transitions and policy evaluation.
- Property tests asserting no unsafe provider mutation sequence.
- Envtest coverage for CRDs, Conditions and RBAC.
- Kind end-to-end tests using a fake provider and controllable probe servers.
- Restart tests that terminate controllers at each phase.
- Upgrade test from a v0.3.0 object and status.
- Multi-architecture image and Helm rendering tests.

## 10. Delivery plan

1. Add API types, validation and Conditions without changing routing behaviour.
2. Implement persisted phases and cooldown/hysteresis.
3. Implement TCP, HTTP, TLS and KubernetesObject probes.
4. Implement post-route verification and recovery actions.
5. Implement ownership reporting and convergence repair.
6. Add reusable `FailoverProbe` resources and external-observation interface.
7. Complete security review, upgrade tests and documentation.

## 11. Definition of done

The feature is complete when:

- legacy objects remain compatible;
- route changes respect cooldown and hysteresis;
- generic probes work without any named ingress/WAF dependency;
- restarts cannot cause duplicate provider mutations;
- failed post-route verification has a bounded, documented outcome;
- stable status requires provider confirmation and exactly one local owner;
- metrics, traces, Conditions and events explain every transition;
- all acceptance, failure, upgrade and multi-arch tests pass.
