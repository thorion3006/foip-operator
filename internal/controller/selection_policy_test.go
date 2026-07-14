package controller

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

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
