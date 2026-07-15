package controller

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

type schedulingClock struct {
	now time.Time
}

func (c *schedulingClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func schedulingNode(name string, ready corev1.ConditionStatus, mutate func(*corev1.Node)) corev1.Node {
	node := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: ready},
		}},
	}
	if mutate != nil {
		mutate(&node)
	}
	return node
}

func TestCandidateReadyForHandoffHonorsPersistedWindow(t *testing.T) {
	spec := netcupv1.FailoverIpSpec{StabilizationSeconds: 30, MinHealthySeconds: 10}
	since := metav1.NewTime(time.Unix(100, 0))
	status := netcupv1.FailoverIpStatus{CandidateSince: &since}
	node := corev1.Node{Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	if candidateReadyForHandoff(spec, status, node, time.Unix(139, 0)) {
		t.Fatal("candidate passed before stabilization window")
	}
	if !candidateReadyForHandoff(spec, status, node, time.Unix(140, 0)) {
		t.Fatal("candidate did not pass at stabilization boundary")
	}
}

func TestSchedulingThresholds(t *testing.T) {
	tests := []struct {
		name         string
		spec         netcupv1.FailoverIpSpec
		wantFailure  int32
		wantRecovery int32
	}{
		{name: "defaults", wantFailure: 3, wantRecovery: 2},
		{name: "configured", spec: netcupv1.FailoverIpSpec{FailureThreshold: 5, RecoveryThreshold: 4}, wantFailure: 5, wantRecovery: 4},
		{name: "non-positive values use defaults", spec: netcupv1.FailoverIpSpec{FailureThreshold: -1, RecoveryThreshold: -1}, wantFailure: 3, wantRecovery: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := failureThreshold(tt.spec); got != tt.wantFailure {
				t.Fatalf("failureThreshold() = %d, want %d", got, tt.wantFailure)
			}
			if got := recoveryThreshold(tt.spec); got != tt.wantRecovery {
				t.Fatalf("recoveryThreshold() = %d, want %d", got, tt.wantRecovery)
			}
		})
	}
}

func TestCandidateReadinessUsesFakeClockAndRejectsUnsafeNodes(t *testing.T) {
	start := time.Unix(100, 0)
	since := metav1.NewTime(start)
	status := netcupv1.FailoverIpStatus{CandidateSince: &since}
	spec := netcupv1.FailoverIpSpec{StabilizationSeconds: 30, MinHealthySeconds: 10}
	clock := &schedulingClock{now: start}

	tests := []struct {
		name     string
		node     corev1.Node
		advance  time.Duration
		wantPass bool
	}{
		{name: "before stabilization and healthy window", node: schedulingNode("candidate", corev1.ConditionTrue, nil), advance: 39 * time.Second},
		{name: "at persisted timer boundary", node: schedulingNode("candidate", corev1.ConditionTrue, nil), advance: 40 * time.Second, wantPass: true},
		{name: "cordoned", node: schedulingNode("candidate", corev1.ConditionTrue, func(n *corev1.Node) { n.Spec.Unschedulable = true }), advance: 40 * time.Second},
		{name: "memory pressure", node: schedulingNode("candidate", corev1.ConditionTrue, func(n *corev1.Node) {
			n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue})
		}), advance: 40 * time.Second},
		{name: "disk pressure", node: schedulingNode("candidate", corev1.ConditionTrue, func(n *corev1.Node) {
			n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue})
		}), advance: 40 * time.Second},
		{name: "pid pressure", node: schedulingNode("candidate", corev1.ConditionTrue, func(n *corev1.Node) {
			n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: corev1.NodePIDPressure, Status: corev1.ConditionTrue})
		}), advance: 40 * time.Second},
		{name: "network unavailable", node: schedulingNode("candidate", corev1.ConditionTrue, func(n *corev1.Node) {
			n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeNetworkUnavailable, Status: corev1.ConditionTrue})
		}), advance: 40 * time.Second},
		{name: "ready unknown", node: schedulingNode("candidate", corev1.ConditionUnknown, nil), advance: 40 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock.now = start
			clock.Advance(tt.advance)
			if got := candidateReadyForHandoff(spec, status, tt.node, clock.now); got != tt.wantPass {
				t.Fatalf("candidateReadyForHandoff() = %t, want %t", got, tt.wantPass)
			}
		})
	}
}

func TestCandidateTimerPersistsAcrossRestart(t *testing.T) {
	start := time.Unix(500, 0)
	since := metav1.NewTime(start)
	statusBeforeRestart := netcupv1.FailoverIpStatus{CandidateSince: &since}
	spec := netcupv1.FailoverIpSpec{StabilizationSeconds: 20, MinHealthySeconds: 5}
	node := schedulingNode("candidate", corev1.ConditionTrue, nil)

	clock := &schedulingClock{now: start.Add(24 * time.Hour)}
	// A fresh policy evaluation has no in-memory state; it uses only the
	// persisted CandidateSince timestamp.
	if !candidateReadyForHandoff(spec, statusBeforeRestart, node, clock.now) {
		t.Fatal("persisted candidate timer was not honored after restart")
	}
}

func TestCandidateRecoveryDuringStabilizationRetainsCurrentOwner(t *testing.T) {
	current := schedulingNode("current", corev1.ConditionTrue, nil)
	candidate := schedulingNode("candidate", corev1.ConditionTrue, nil)

	if got := betterNode([]corev1.Node{current, candidate}, current.Name); got != nil {
		t.Fatalf("betterNode() = %q, want no switch after current owner recovery", got.Name)
	}
}
