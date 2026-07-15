package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

func TestSingleOwnerOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		phase     netcupv1.FailoverPhase
		target    string
		owners    []string
		wantValid bool
	}{
		{name: "succeeded with target as sole owner", phase: netcupv1.FailoverPhaseSucceeded, target: "node-a", owners: []string{"node-a"}, wantValid: true},
		{name: "succeeded with zero owners is invalid", phase: netcupv1.FailoverPhaseSucceeded, target: "node-a", wantValid: false},
		{name: "succeeded with multiple owners is invalid", phase: netcupv1.FailoverPhaseSucceeded, target: "node-a", owners: []string{"node-a", "node-b"}, wantValid: false},
		{name: "succeeded with wrong sole owner is invalid", phase: netcupv1.FailoverPhaseSucceeded, target: "node-a", owners: []string{"node-b"}, wantValid: false},
		{name: "degraded may retain dual ownership", phase: netcupv1.FailoverPhaseDegraded, target: "node-a", owners: []string{"node-a", "node-b"}, wantValid: true},
		{name: "degraded may have zero owners", phase: netcupv1.FailoverPhaseDegraded, target: "node-a", wantValid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := netcupv1.FailoverIpStatus{TransitionID: "transition-1", Phase: tt.phase, TargetNode: tt.target, LocalOwners: tt.owners}
			err := netcupv1.ValidateStatus(status)
			if (err == nil) != tt.wantValid {
				t.Fatalf("ValidateStatus() error = %v, want valid=%v", err, tt.wantValid)
			}
		})
	}
}

func TestOwnershipConvergenceRequiresExactlyOneTargetOwner(t *testing.T) {
	tests := []struct {
		name   string
		owners []string
		want   bool
	}{
		{name: "zero owners", want: false},
		{name: "one target owner", owners: []string{"node-a"}, want: true},
		{name: "one different owner", owners: []string{"node-b"}, want: false},
		{name: "two owners", owners: []string{"node-a", "node-b"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := netcupv1.FailoverIpStatus{TransitionID: "transition-1", Phase: netcupv1.FailoverPhaseCleaningStaleOwners, TargetNode: "node-a", LocalOwners: tt.owners}
			if got := len(status.LocalOwners) == 1 && containsNode(status.LocalOwners, status.TargetNode); got != tt.want {
				t.Fatalf("ownership converged = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSupersededTransitionFencing(t *testing.T) {
	now := metav1.NewTime(time.Unix(100, 0))
	status := netcupv1.FailoverIpStatus{}
	netcupv1.StartTransition(&status, now)
	firstID := status.TransitionID
	// A stale/new-start attempt must not replace the durable transition identity.
	netcupv1.StartTransition(&status, metav1.NewTime(time.Unix(200, 0)))
	if status.TransitionID != firstID {
		t.Fatalf("transition ID changed from %q to %q", firstID, status.TransitionID)
	}
	if !status.PhaseStartedAt.Equal(&now) {
		t.Fatalf("phase start changed to %v, want %v", status.PhaseStartedAt, now)
	}

	// A superseded persisted phase cannot jump over the ownership gates.
	if err := netcupv1.AdvanceTransition(&status, netcupv1.FailoverPhaseSucceeded, now); err == nil {
		t.Fatal("superseded transition advanced directly to succeeded")
	}
	if err := netcupv1.AdvanceTransition(&status, netcupv1.FailoverPhaseStabilizing, now); err != nil {
		t.Fatalf("advance current transition: %v", err)
	}
	if err := netcupv1.AdvanceTransition(&status, netcupv1.FailoverPhaseSucceeded, now); err == nil {
		t.Fatal("superseded transition skipped stabilization and ownership fencing")
	}
}
