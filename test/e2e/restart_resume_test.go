package e2e

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

// TestPersistedStateResumesAtEveryPhase verifies the durable contract used by
// a restarted controller. It deliberately does not call Netcup: the provider
// mutation is outside this deterministic contract test.
func TestPersistedStateResumesAtEveryPhase(t *testing.T) {
	phases := []netcupv1.FailoverPhase{
		netcupv1.FailoverPhaseIdle,
		netcupv1.FailoverPhaseSelecting,
		netcupv1.FailoverPhaseStabilizing,
		netcupv1.FailoverPhasePreparingTarget,
		netcupv1.FailoverPhaseTargetPrepared,
		netcupv1.FailoverPhaseRoutingProvider,
		netcupv1.FailoverPhaseVerifyingProvider,
		netcupv1.FailoverPhaseVerifyingTraffic,
		netcupv1.FailoverPhaseCommitting,
		netcupv1.FailoverPhaseCleaningStaleOwners,
		netcupv1.FailoverPhaseSucceeded,
		netcupv1.FailoverPhaseDegraded,
		netcupv1.FailoverPhaseBlocked,
	}

	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			beforeRestart := resumableStatus(phase)
			encoded, err := json.Marshal(beforeRestart)
			if err != nil {
				t.Fatalf("marshal persisted status: %v", err)
			}

			var afterRestart netcupv1.FailoverIpStatus
			if err := json.Unmarshal(encoded, &afterRestart); err != nil {
				t.Fatalf("unmarshal persisted status: %v", err)
			}
			if err := netcupv1.ValidateStatus(afterRestart); err != nil {
				t.Fatalf("restarted status is invalid: %v", err)
			}
			if afterRestart.TransitionID != beforeRestart.TransitionID {
				t.Fatalf("transition ID changed across restart: before=%q after=%q", beforeRestart.TransitionID, afterRestart.TransitionID)
			}
			if afterRestart.Phase != phase {
				t.Fatalf("phase changed across restart: before=%q after=%q", phase, afterRestart.Phase)
			}
		})
	}
}

func TestPersistedStateCannotSkipSafetyGatesAfterRestart(t *testing.T) {
	status := resumableStatus(netcupv1.FailoverPhaseTargetPrepared)
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal persisted status: %v", err)
	}
	var restarted netcupv1.FailoverIpStatus
	if err := json.Unmarshal(encoded, &restarted); err != nil {
		t.Fatalf("unmarshal persisted status: %v", err)
	}

	if netcupv1.CanTransition(restarted.Phase, netcupv1.FailoverPhaseSucceeded) {
		t.Fatal("restarted target-prepared state can skip provider routing and verification")
	}
}

func resumableStatus(phase netcupv1.FailoverPhase) netcupv1.FailoverIpStatus {
	now := metav1.Now()
	status := netcupv1.FailoverIpStatus{
		TransitionID:     "transition-restart-fixture",
		Phase:            phase,
		SourceNode:       "node-a",
		TargetNode:       "node-b",
		PhaseStartedAt:   &now,
		LastTransitionAt: &now,
	}
	if phase == netcupv1.FailoverPhaseSucceeded {
		status.LocalOwners = []string{"node-b"}
	}
	return status
}
