package v1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCanTransition(t *testing.T) {
	tests := []struct {
		name string
		from FailoverPhase
		to   FailoverPhase
		want bool
	}{
		{name: "select after idle", from: FailoverPhaseIdle, to: FailoverPhaseSelecting, want: true},
		{name: "prepare after selecting is illegal", from: FailoverPhaseSelecting, to: FailoverPhasePreparingTarget, want: false},
		{name: "stabilize can cancel", from: FailoverPhaseStabilizing, to: FailoverPhaseSelecting, want: true},
		{name: "route skips preparation is illegal", from: FailoverPhaseTargetPrepared, to: FailoverPhaseSucceeded, want: false},
		{name: "blocked can be retried", from: FailoverPhaseBlocked, to: FailoverPhaseSelecting, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanTransition(tt.from, tt.to); got != tt.want {
				t.Fatalf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestStartAndAdvanceTransition(t *testing.T) {
	now := metav1.Now()
	status := FailoverIpStatus{}
	StartTransition(&status, now)
	if status.TransitionID == "" || status.Phase != FailoverPhaseSelecting {
		t.Fatalf("started status = %#v", status)
	}
	if err := AdvanceTransition(&status, FailoverPhaseStabilizing, now); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if status.LastSuccessfulPhase != FailoverPhaseSelecting {
		t.Fatalf("last successful phase = %q", status.LastSuccessfulPhase)
	}
	if err := AdvanceTransition(&status, FailoverPhaseSucceeded, now); err == nil {
		t.Fatal("expected illegal transition error")
	}
}

func TestValidateStatusRejectsContradictoryOwnership(t *testing.T) {
	status := FailoverIpStatus{
		TransitionID: "transition-1",
		Phase:        FailoverPhaseSucceeded,
		TargetNode:   "node-a",
		LocalOwners:  []string{"node-a", "node-b"},
	}
	if err := ValidateStatus(status); err == nil {
		t.Fatal("expected multiple-owner status to be rejected")
	}
}

func TestValidateFailoverIpSpecRejectsUnsafeTiming(t *testing.T) {
	for name, spec := range map[string]FailoverIpSpec{
		"negative threshold": {FailureThreshold: -1},
		"negative cooldown":  {ProviderCooldownSeconds: -1},
		"retry range":        {RetryBaseSeconds: 10, RetryMaxSeconds: 2},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateFailoverIpSpec(spec); err == nil {
				t.Fatal("expected invalid specification")
			}
		})
	}
}

func TestValidateProbeSpecRejectsUnknownType(t *testing.T) {
	err := ValidateProbeSpec(FailoverProbeSpec{Phase: ProbePhasePreRoute, Type: ProbeType("Unknown"), Target: ProbeTarget{Address: "example.com", Port: 443}})
	if err == nil {
		t.Fatal("expected unknown probe type to be rejected")
	}
}

func FuzzValidateStatusNeverPanics(f *testing.F) {
	f.Add("transition-1", string(FailoverPhaseSucceeded), "node-a", "node-a")
	f.Add("", "unknown", "", "")
	f.Fuzz(func(t *testing.T, transitionID, phase, target, owner string) {
		status := FailoverIpStatus{TransitionID: transitionID, Phase: FailoverPhase(phase), TargetNode: target}
		if owner != "" {
			status.LocalOwners = []string{owner}
		}
		_ = ValidateStatus(status)
	})
}

func FuzzValidateStatusRejectsImpossibleSucceededOwnership(f *testing.F) {
	f.Add("transition", "node-a", "node-b")
	f.Add("transition", "node-a", "node-a")
	f.Fuzz(func(t *testing.T, transitionID, target, owner string) {
		status := FailoverIpStatus{TransitionID: transitionID, Phase: FailoverPhaseSucceeded, TargetNode: target}
		if owner != "" {
			status.LocalOwners = []string{owner}
		}
		valid := ValidateStatus(status) == nil
		wantValid := transitionID != "" && target != "" && owner == target
		if valid != wantValid {
			t.Fatalf("ValidateStatus(%#v) valid=%v, want %v", status, valid, wantValid)
		}
	})
}
