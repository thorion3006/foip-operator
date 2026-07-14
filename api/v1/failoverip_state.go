package v1

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
)

// legalPhaseTransitions is deliberately explicit: a persisted status must not
// be able to skip a safety gate during restart recovery.
var legalPhaseTransitions = map[FailoverPhase]map[FailoverPhase]struct{}{
	FailoverPhaseIdle:                {FailoverPhaseSelecting: {}},
	FailoverPhaseSelecting:           {FailoverPhaseStabilizing: {}, FailoverPhaseBlocked: {}},
	FailoverPhaseStabilizing:         {FailoverPhasePreparingTarget: {}, FailoverPhaseSelecting: {}, FailoverPhaseBlocked: {}},
	FailoverPhasePreparingTarget:     {FailoverPhaseTargetPrepared: {}, FailoverPhaseBlocked: {}},
	FailoverPhaseTargetPrepared:      {FailoverPhaseRoutingProvider: {}, FailoverPhaseBlocked: {}},
	FailoverPhaseRoutingProvider:     {FailoverPhaseVerifyingProvider: {}, FailoverPhaseDegraded: {}, FailoverPhaseBlocked: {}},
	FailoverPhaseVerifyingProvider:   {FailoverPhaseVerifyingTraffic: {}, FailoverPhaseDegraded: {}, FailoverPhaseBlocked: {}},
	FailoverPhaseVerifyingTraffic:    {FailoverPhaseCommitting: {}, FailoverPhaseDegraded: {}, FailoverPhaseBlocked: {}},
	FailoverPhaseCommitting:          {FailoverPhaseCleaningStaleOwners: {}, FailoverPhaseDegraded: {}, FailoverPhaseBlocked: {}},
	FailoverPhaseCleaningStaleOwners: {FailoverPhaseSucceeded: {}, FailoverPhaseDegraded: {}, FailoverPhaseBlocked: {}},
	FailoverPhaseSucceeded:           {FailoverPhaseSelecting: {}},
	FailoverPhaseDegraded:            {FailoverPhaseSelecting: {}, FailoverPhaseBlocked: {}},
	FailoverPhaseBlocked:             {FailoverPhaseSelecting: {}},
}

// CanTransition reports whether moving between persisted phases is legal.
func CanTransition(from, to FailoverPhase) bool {
	_, ok := legalPhaseTransitions[from][to]
	return ok
}

// ValidateStatus checks invariants that must hold whenever a status is stored.
// It intentionally rejects contradictory ownership rather than guessing which
// field is authoritative after a restart.
func ValidateStatus(status FailoverIpStatus) error {
	if status.Phase == "" {
		return nil
	}
	if status.TransitionID == "" {
		return fmt.Errorf("phase %s has no transition ID", status.Phase)
	}
	if status.TargetNode != "" && status.SourceNode != "" && status.TargetNode == status.SourceNode &&
		status.Phase != FailoverPhaseSucceeded && status.Phase != FailoverPhaseIdle {
		return fmt.Errorf("source and target node are identical: %s", status.TargetNode)
	}
	if status.Phase == FailoverPhaseSucceeded && len(status.LocalOwners) != 1 {
		return fmt.Errorf("succeeded transition has %d local owners, want exactly one", len(status.LocalOwners))
	}
	if status.Phase == FailoverPhaseSucceeded && status.TargetNode != status.LocalOwners[0] {
		return fmt.Errorf("succeeded transition target %q is not local owner %q", status.TargetNode, status.LocalOwners[0])
	}
	return nil
}

// StartTransition creates the durable identity and initial phase for a new
// resource. It is safe to call only when no transition has started.
func StartTransition(status *FailoverIpStatus, now metav1.Time) {
	if status.TransitionID != "" {
		return
	}
	status.TransitionID = string(uuid.NewUUID())
	status.Phase = FailoverPhaseSelecting
	status.PhaseStartedAt = &now
	status.LastTransitionAt = &now
}

// AdvanceTransition updates phase bookkeeping after the caller has completed
// the work guarded by the destination phase.
func AdvanceTransition(status *FailoverIpStatus, to FailoverPhase, now metav1.Time) error {
	if status.Phase == "" {
		return fmt.Errorf("cannot advance an uninitialized status")
	}
	if !CanTransition(status.Phase, to) {
		return fmt.Errorf("illegal failover phase transition %s -> %s", status.Phase, to)
	}
	status.LastSuccessfulPhase = status.Phase
	status.Phase = to
	status.PhaseStartedAt = &now
	status.LastTransitionAt = &now
	status.LastError = ""
	return nil
}

// ValidateProbeSpec rejects ambiguous probe definitions before execution.
func ValidateProbeSpec(spec FailoverProbeSpec) error {
	if spec.Phase == "" || spec.Type == "" {
		return fmt.Errorf("probe phase and type are required")
	}
	if spec.Composition == ProbeCompositionQuorum && spec.Quorum < 1 {
		return fmt.Errorf("quorum must be at least one")
	}
	if spec.Composition != "" && spec.Composition != ProbeCompositionAll &&
		spec.Composition != ProbeCompositionAny && spec.Composition != ProbeCompositionQuorum {
		return fmt.Errorf("unsupported probe composition %q", spec.Composition)
	}
	if spec.Type == ProbeTypeKubernetes {
		if spec.Kubernetes == nil || spec.Kubernetes.Kind == "" || spec.Kubernetes.Name == "" {
			return fmt.Errorf("kubernetes probes require kind and name")
		}
		return nil
	}
	if spec.Target.Address == "" {
		return fmt.Errorf("network probes require a target address")
	}
	if spec.Type == ProbeTypeTCP || spec.Type == ProbeTypeTLS || spec.Type == ProbeTypeHTTP || spec.Type == ProbeTypeHTTPS {
		if spec.Target.Port < 1 || spec.Target.Port > 65535 {
			return fmt.Errorf("network probe port must be between 1 and 65535")
		}
	}
	if spec.InsecureSkipVerify && spec.Type != ProbeTypeTLS && spec.Type != ProbeTypeHTTPS {
		return fmt.Errorf("insecureSkipVerify is only valid for TLS and HTTPS probes")
	}
	return nil
}
