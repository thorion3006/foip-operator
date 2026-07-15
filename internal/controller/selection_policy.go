package controller

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

func stabilizationWindow(spec netcupv1.FailoverIpSpec) time.Duration {
	if spec.StabilizationSeconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(spec.StabilizationSeconds) * time.Second
}

func minHealthyWindow(spec netcupv1.FailoverIpSpec) time.Duration {
	if spec.MinHealthySeconds <= 0 {
		return 0
	}
	return time.Duration(spec.MinHealthySeconds) * time.Second
}

func failureThreshold(spec netcupv1.FailoverIpSpec) int32 {
	if spec.FailureThreshold <= 0 {
		return 3
	}
	return spec.FailureThreshold
}

func recoveryThreshold(spec netcupv1.FailoverIpSpec) int32 {
	if spec.RecoveryThreshold <= 0 {
		return 2
	}
	return spec.RecoveryThreshold
}

// candidateReadyForHandoff makes the persisted candidate timer the gate for a
// move; callers must retain the current owner while this returns false.
func candidateReadyForHandoff(spec netcupv1.FailoverIpSpec, status netcupv1.FailoverIpStatus, candidate corev1.Node, now time.Time) bool {
	if !nodeAcceptable(candidate) || status.CandidateSince == nil {
		return false
	}
	return !now.Before(status.CandidateSince.Add(stabilizationWindow(spec) + minHealthyWindow(spec)))
}

func nodeAcceptable(node corev1.Node) bool {
	if node.Spec.Unschedulable || conditionIs(node, corev1.NodeNetworkUnavailable, corev1.ConditionTrue) ||
		conditionIs(node, corev1.NodePIDPressure, corev1.ConditionTrue) ||
		conditionIs(node, corev1.NodeMemoryPressure, corev1.ConditionTrue) ||
		conditionIs(node, corev1.NodeDiskPressure, corev1.ConditionTrue) {
		return false
	}
	return conditionIs(node, corev1.NodeReady, corev1.ConditionTrue)
}
