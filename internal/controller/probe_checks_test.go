package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

func TestResolveProbeSpecSubstitutesFailoverAndTargetNodeIP(t *testing.T) {
	reader := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithObjects(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "target"},
		Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeExternalIP, Address: "203.0.113.20"}}},
	}).Build()
	foip := netcupv1.FailoverIp{Spec: netcupv1.FailoverIpSpec{IP: "198.51.100.10"}, Status: netcupv1.FailoverIpStatus{TargetNode: "target"}}
	spec, err := resolveProbeSpec(context.Background(), reader, foip, netcupv1.FailoverProbeSpec{Phase: netcupv1.ProbePhasePreRoute, Type: netcupv1.ProbeTypeTCP, Target: netcupv1.ProbeTarget{Address: "${targetNodeIP}", Port: 443, Host: "${failoverIP}"}})
	if err != nil {
		t.Fatalf("resolve probe: %v", err)
	}
	if spec.Target.Address != "203.0.113.20" || spec.Target.Host != "198.51.100.10" {
		t.Fatalf("resolved target = %#v", spec.Target)
	}
}

func TestResolveProbeSpecRejectsUnresolvedPlaceholder(t *testing.T) {
	foip := netcupv1.FailoverIp{Spec: netcupv1.FailoverIpSpec{IP: "198.51.100.10"}}
	_, err := resolveProbeSpec(context.Background(), fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).Build(), foip, netcupv1.FailoverProbeSpec{Phase: netcupv1.ProbePhasePreRoute, Type: netcupv1.ProbeTypeTCP, Target: netcupv1.ProbeTarget{Address: "${dnsName}", Port: 443}})
	if err == nil {
		t.Fatal("expected unresolved placeholder to be rejected")
	}
}

func TestEvaluateProbePhasePersistsRedactedObservation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := netcupv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	probeResource := &netcupv1.FailoverProbe{
		ObjectMeta: metav1.ObjectMeta{Name: "blocked-target", Namespace: "default"},
		Spec: netcupv1.FailoverProbeSpec{
			Phase:  netcupv1.ProbePhasePreRoute,
			Type:   netcupv1.ProbeTypeTCP,
			Target: netcupv1.ProbeTarget{Address: "127.0.0.1", Port: 443},
		},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(probeResource).WithRuntimeObjects(probeResource).Build()
	foip := netcupv1.FailoverIp{ObjectMeta: metav1.ObjectMeta{Name: "foip", Namespace: "default"}, Spec: netcupv1.FailoverIpSpec{Probes: []corev1.LocalObjectReference{{Name: probeResource.Name}}}}
	err := evaluateProbePhase(context.Background(), reader, foip, netcupv1.ProbePhasePreRoute)
	if err == nil {
		t.Fatal("blocked probe unexpectedly passed")
	}
	var observed netcupv1.FailoverProbe
	if err := reader.Get(context.Background(), client.ObjectKey{Name: probeResource.Name, Namespace: probeResource.Namespace}, &observed); err != nil {
		t.Fatal(err)
	}
	if len(observed.Status.Observations) != 1 || observed.Status.Observations[0].Reason == "" {
		t.Fatalf("observation was not persisted: %#v", observed.Status.Observations)
	}
}
