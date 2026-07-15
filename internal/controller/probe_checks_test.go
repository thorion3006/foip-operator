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
	"github.com/thorion3006/foip-operator/internal/probe"
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

func TestEvaluateProbePhaseAllowsNodeHealthOnlyMode(t *testing.T) {
	foip := netcupv1.FailoverIp{Spec: netcupv1.FailoverIpSpec{Probes: nil}}
	if err := evaluateProbePhase(context.Background(), nil, foip, netcupv1.ProbePhasePreRoute); err != nil {
		t.Fatalf("node-health-only mode failed: %v", err)
	}
}

func TestResolveProbeSpecRejectsUnresolvedPlaceholder(t *testing.T) {
	foip := netcupv1.FailoverIp{Spec: netcupv1.FailoverIpSpec{IP: "198.51.100.10"}}
	_, err := resolveProbeSpec(context.Background(), fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).Build(), foip, netcupv1.FailoverProbeSpec{Phase: netcupv1.ProbePhasePreRoute, Type: netcupv1.ProbeTypeTCP, Target: netcupv1.ProbeTarget{Address: "${dnsName}", Port: 443}})
	if err == nil {
		t.Fatal("expected unresolved placeholder to be rejected")
	}
}

func TestApplyProbeThresholdsPreservesHealthyStateUntilFailureThreshold(t *testing.T) {
	spec := netcupv1.FailoverProbeSpec{SuccessThreshold: 2, FailureThreshold: 3}
	status := netcupv1.FailoverProbeStatus{Observations: []netcupv1.ProbeObservation{{Name: "probe", Success: true, ConsecutiveSuccesses: 2}}}
	result, successes, failures := applyProbeThresholds(spec, status, "probe", probe.Result{Reason: "connection failed"})
	if !result.Success || successes != 0 || failures != 1 {
		t.Fatalf("result = %#v, counts = (%d, %d)", result, successes, failures)
	}
	result, _, failures = applyProbeThresholds(spec, netcupv1.FailoverProbeStatus{Observations: []netcupv1.ProbeObservation{{Name: "probe", Success: true, ConsecutiveSuccesses: 0, ConsecutiveFailures: 2}}}, "probe", probe.Result{Reason: "connection failed"})
	if result.Success || failures != 3 {
		t.Fatalf("threshold result = %#v, failures = %d", result, failures)
	}
}

func TestApplyProbeThresholdsRequiresConsecutiveSuccesses(t *testing.T) {
	spec := netcupv1.FailoverProbeSpec{SuccessThreshold: 2}
	result, successes, _ := applyProbeThresholds(spec, netcupv1.FailoverProbeStatus{}, "probe", probe.Result{Success: true})
	if result.Success || successes != 1 {
		t.Fatalf("first success = %#v, count = %d", result, successes)
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

func TestProbeChangeRequeuesEveryReferencingFailoverIP(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := netcupv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(
		&netcupv1.FailoverIp{ObjectMeta: metav1.ObjectMeta{Name: "first", Namespace: "default"}, Spec: netcupv1.FailoverIpSpec{Probes: []corev1.LocalObjectReference{{Name: "shared"}}}},
		&netcupv1.FailoverIp{ObjectMeta: metav1.ObjectMeta{Name: "second", Namespace: "default"}, Spec: netcupv1.FailoverIpSpec{Probes: []corev1.LocalObjectReference{{Name: "shared"}}}},
		&netcupv1.FailoverIp{ObjectMeta: metav1.ObjectMeta{Name: "other-namespace", Namespace: "other"}, Spec: netcupv1.FailoverIpSpec{Probes: []corev1.LocalObjectReference{{Name: "shared"}}}},
	).Build()
	reconciler := &FailoverIpReconciler{Client: fakeClient, APIReader: fakeClient, Scheme: scheme}
	requests := reconciler.probeToFoips(context.Background(), &netcupv1.FailoverProbe{ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "default"}})
	if len(requests) != 2 {
		t.Fatalf("requeue requests = %d, want 2", len(requests))
	}
	if requests[0].Namespace != "default" || requests[1].Namespace != "default" {
		t.Fatalf("cross-namespace probe reference leaked: %#v", requests)
	}
}

func TestSecretChangeRequeuesProviderAndReferencedProbeFailoverIPs(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := netcupv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(
		&netcupv1.FailoverIp{ObjectMeta: metav1.ObjectMeta{Name: "provider", Namespace: "default"}, Spec: netcupv1.FailoverIpSpec{SecretName: "shared-secret"}},
		&netcupv1.FailoverIp{ObjectMeta: metav1.ObjectMeta{Name: "probe-user", Namespace: "default"}, Spec: netcupv1.FailoverIpSpec{Probes: []corev1.LocalObjectReference{{Name: "shared-probe"}}}},
		&netcupv1.FailoverIp{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "other"}, Spec: netcupv1.FailoverIpSpec{SecretName: "shared-secret"}},
		&netcupv1.FailoverProbe{ObjectMeta: metav1.ObjectMeta{Name: "shared-probe", Namespace: "default"}, Spec: netcupv1.FailoverProbeSpec{Phase: netcupv1.ProbePhasePreRoute, Type: netcupv1.ProbeTypeHTTPS, Target: netcupv1.ProbeTarget{Address: "example.com", Port: 443}, CredentialSecretRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "shared-secret"}, Key: "token"}}},
	).Build()
	reconciler := &FailoverIpReconciler{Client: fakeClient, APIReader: fakeClient, Scheme: scheme}
	requests := reconciler.secretToFoips(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "shared-secret", Namespace: "default"}})
	if len(requests) != 2 || requests[0].Name != "probe-user" || requests[1].Name != "provider" {
		t.Fatalf("secret requeue requests = %#v", requests)
	}
}

func TestEvaluateProbePhaseRejectsConflictingReferencedCompositions(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := netcupv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	probe := func(name string, composition netcupv1.ProbeComposition) *netcupv1.FailoverProbe {
		return &netcupv1.FailoverProbe{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: netcupv1.FailoverProbeSpec{Phase: netcupv1.ProbePhasePreRoute, Type: netcupv1.ProbeTypeKubernetes, Composition: composition, Kubernetes: &netcupv1.KubernetesReadinessTarget{APIVersion: "v1", Kind: "ConfigMap", Name: name}}}
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(probe("a", netcupv1.ProbeCompositionAny), probe("b", netcupv1.ProbeCompositionAll)).Build()
	foip := netcupv1.FailoverIp{ObjectMeta: metav1.ObjectMeta{Name: "foip", Namespace: "default"}, Spec: netcupv1.FailoverIpSpec{Probes: []corev1.LocalObjectReference{{Name: "b"}, {Name: "a"}}}}
	if err := evaluateProbePhase(context.Background(), reader, foip, netcupv1.ProbePhasePreRoute); err == nil {
		t.Fatal("conflicting probe compositions were accepted")
	}
}
