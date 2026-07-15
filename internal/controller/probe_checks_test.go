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
