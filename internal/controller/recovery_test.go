package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

func TestRecoverPostRouteFailurePolicies(t *testing.T) {
	tests := []struct {
		name           string
		policy         netcupv1.RecoveryPolicy
		status         netcupv1.FailoverIpStatus
		node           *corev1.Node
		providerError  error
		wantCommit     bool
		wantPhase      netcupv1.FailoverPhase
		wantRouteCalls int
		wantRequeue    bool
	}{
		{
			name:        "default holds dual ownership",
			wantPhase:   netcupv1.FailoverPhaseDegraded,
			wantRequeue: true,
		},
		{
			name:        "explicit hold dual ownership",
			policy:      netcupv1.RecoveryPolicyHoldDualOwnership,
			wantPhase:   netcupv1.FailoverPhaseDegraded,
			wantRequeue: true,
		},
		{
			name:       "commits degraded",
			policy:     netcupv1.RecoveryPolicyCommitDegraded,
			wantCommit: true,
		},
		{
			name:      "requires manual intervention",
			policy:    netcupv1.RecoveryPolicyManualIntervention,
			wantPhase: netcupv1.FailoverPhaseBlocked,
		},
		{
			name:      "rolls provider back",
			policy:    netcupv1.RecoveryPolicyRollbackProvider,
			status:    netcupv1.FailoverIpStatus{SourceNode: "source"},
			node:      &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "source", Annotations: map[string]string{netcupv1.ServerIDAnnotation: "101"}}},
			wantPhase: netcupv1.FailoverPhaseDegraded, wantRouteCalls: 1, wantRequeue: true,
		},
		{
			name:      "blocks when rollback source is unavailable",
			policy:    netcupv1.RecoveryPolicyRollbackProvider,
			status:    netcupv1.FailoverIpStatus{SourceNode: "missing"},
			wantPhase: netcupv1.FailoverPhaseBlocked, wantRequeue: true,
		},
		{
			name:          "blocks when rollback provider fails",
			policy:        netcupv1.RecoveryPolicyRollbackProvider,
			status:        netcupv1.FailoverIpStatus{SourceNode: "source"},
			node:          &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "source", Annotations: map[string]string{netcupv1.ServerIDAnnotation: "101"}}},
			providerError: errors.New("provider unavailable"),
			wantPhase:     netcupv1.FailoverPhaseBlocked, wantRouteCalls: 1, wantRequeue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := netcupv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			foip := &netcupv1.FailoverIp{
				ObjectMeta: metav1.ObjectMeta{Name: "recovery", Namespace: "default"},
				Spec:       netcupv1.FailoverIpSpec{RecoveryPolicy: tt.policy, ProviderCooldownSeconds: 1},
				Status:     tt.status,
			}
			objects := []runtime.Object{foip}
			if tt.node != nil {
				objects = append(objects, tt.node)
			}
			client := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(foip).WithRuntimeObjects(objects...).Build()
			provider := &countingFailoverIPClient{fakeFailoverIPClient: fakeFailoverIPClient{routeErr: tt.providerError}}
			reconciler := &FailoverIpReconciler{Client: client, APIReader: client, Scheme: scheme}

			commit, result, err := reconciler.recoverPostRouteFailure(ctx, foip, provider, 17)
			if err != nil {
				t.Fatalf("recover: %v", err)
			}
			if commit != tt.wantCommit {
				t.Fatalf("commit degraded = %v, want %v", commit, tt.wantCommit)
			}
			if provider.routeCalls != tt.wantRouteCalls {
				t.Fatalf("provider route calls = %d, want %d", provider.routeCalls, tt.wantRouteCalls)
			}
			if (result.RequeueAfter > 0) != tt.wantRequeue {
				t.Fatalf("requeue = %s, want requeue=%v", result.RequeueAfter, tt.wantRequeue)
			}
			var stored netcupv1.FailoverIp
			if err := client.Get(ctx, clientKey("recovery", "default"), &stored); err != nil {
				t.Fatal(err)
			}
			if stored.Status.Phase != tt.wantPhase {
				t.Fatalf("phase = %q, want %q", stored.Status.Phase, tt.wantPhase)
			}
		})
	}
}

func clientKey(name, namespace string) client.ObjectKey {
	return client.ObjectKey{Name: name, Namespace: namespace}
}

func TestRecoveryRollbackCooldownAndProviderFailures(t *testing.T) {
	tests := []struct {
		name          string
		attemptedAgo  time.Duration
		providerOwner string
		observedOwner string
		wantAllowed   bool
		wantFenceErr  bool
	}{
		{name: "cooldown delays second mutation", attemptedAgo: 0, wantAllowed: false},
		{name: "cooldown expires at boundary", attemptedAgo: time.Second, wantAllowed: true},
		{name: "unchanged provider owner is safe", providerOwner: "101", observedOwner: "101", wantAllowed: true},
		{name: "out of band provider owner is fenced", providerOwner: "101", observedOwner: "202", wantAllowed: true, wantFenceErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			status := netcupv1.FailoverIpStatus{ProviderObservedOwner: tt.providerOwner}
			if tt.attemptedAgo >= 0 && tt.providerOwner == "" {
				attempted := metav1.NewTime(now.Add(-tt.attemptedAgo))
				status.LastAttemptedProviderMutationAt = &attempted
			}
			_, allowed := providerMutationGate(status, time.Second, now)
			if allowed != tt.wantAllowed {
				t.Fatalf("mutation allowed = %v, want %v", allowed, tt.wantAllowed)
			}
			err := validateProviderFence(status, tt.observedOwner, "202")
			if (err != nil) != tt.wantFenceErr {
				t.Fatalf("fence error = %v, want error=%v", err, tt.wantFenceErr)
			}
		})
	}
}

func TestProbeGatesByPhase(t *testing.T) {
	tests := []struct {
		name       string
		requested  netcupv1.ProbePhase
		probePhase netcupv1.ProbePhase
		ready      bool
		wantErr    bool
	}{
		{name: "pre route passes", requested: netcupv1.ProbePhasePreRoute, probePhase: netcupv1.ProbePhasePreRoute, ready: true},
		{name: "pre route blocks", requested: netcupv1.ProbePhasePreRoute, probePhase: netcupv1.ProbePhasePreRoute, wantErr: true},
		{name: "post route passes", requested: netcupv1.ProbePhasePostRoute, probePhase: netcupv1.ProbePhasePostRoute, ready: true},
		{name: "post route blocks", requested: netcupv1.ProbePhasePostRoute, probePhase: netcupv1.ProbePhasePostRoute, wantErr: true},
		{name: "continuous probe participates", requested: netcupv1.ProbePhasePostRoute, probePhase: netcupv1.ProbePhaseContinuous, ready: true},
		{name: "other phase is skipped", requested: netcupv1.ProbePhasePostRoute, probePhase: netcupv1.ProbePhasePreRoute, ready: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := netcupv1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			objects := []runtime.Object{}
			probe := &netcupv1.FailoverProbe{ObjectMeta: metav1.ObjectMeta{Name: "gate", Namespace: "default"}, Spec: netcupv1.FailoverProbeSpec{Phase: tt.probePhase, Type: netcupv1.ProbeTypeKubernetes, Kubernetes: &netcupv1.KubernetesReadinessTarget{APIVersion: "v1", Kind: "ConfigMap", Name: "gate", Namespace: "default"}}}
			// ConfigMap is intentionally unsupported by the Kubernetes probe executor;
			// use a Pod object for the passing case and omit it for the failing case.
			probe.Spec.Kubernetes.Kind = "Pod"
			objects = append(objects, probe)
			if tt.ready {
				objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "gate", Namespace: "default"}})
			}
			reader := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&netcupv1.FailoverProbe{}).WithRuntimeObjects(objects...).Build()
			foip := netcupv1.FailoverIp{ObjectMeta: metav1.ObjectMeta{Name: "foip", Namespace: "default"}, Spec: netcupv1.FailoverIpSpec{Probes: []corev1.LocalObjectReference{{Name: "gate"}}}}
			err := evaluateProbePhase(context.Background(), reader, foip, tt.requested)
			if (err != nil) != tt.wantErr {
				t.Fatalf("probe gate error = %v, want error=%v", err, tt.wantErr)
			}
		})
	}
}

func TestRecoveryProbeCompositionAllAndQuorum(t *testing.T) {
	// A missing Kubernetes object supplies one failed result; a present object
	// supplies one successful result. This verifies the gate aggregation inputs.
	scheme := runtime.NewScheme()
	if err := netcupv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	probes := []*netcupv1.FailoverProbe{
		{ObjectMeta: metav1.ObjectMeta{Name: "present", Namespace: "default"}, Spec: netcupv1.FailoverProbeSpec{Phase: netcupv1.ProbePhasePostRoute, Type: netcupv1.ProbeTypeKubernetes, Kubernetes: &netcupv1.KubernetesReadinessTarget{APIVersion: "v1", Kind: "Pod", Name: "present", Namespace: "default"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "missing", Namespace: "default"}, Spec: netcupv1.FailoverProbeSpec{Phase: netcupv1.ProbePhasePostRoute, Type: netcupv1.ProbeTypeKubernetes, Kubernetes: &netcupv1.KubernetesReadinessTarget{APIVersion: "v1", Kind: "Pod", Name: "missing", Namespace: "default"}}},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&netcupv1.FailoverProbe{}).WithRuntimeObjects(probes[0], probes[1], &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "present", Namespace: "default"}}).Build()
	for _, tt := range []struct {
		name        string
		composition netcupv1.ProbeComposition
		quorum      int32
		wantErr     bool
	}{
		{name: "all rejects one failed probe", composition: netcupv1.ProbeCompositionAll, wantErr: true},
		{name: "quorum of one accepts one success", composition: netcupv1.ProbeCompositionQuorum, quorum: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			foip := netcupv1.FailoverIp{ObjectMeta: metav1.ObjectMeta{Name: "foip", Namespace: "default"}, Spec: netcupv1.FailoverIpSpec{ProbeComposition: tt.composition, ProbeQuorum: tt.quorum, Probes: []corev1.LocalObjectReference{{Name: "present"}, {Name: "missing"}}}}
			err := evaluateProbePhase(context.Background(), reader, foip, netcupv1.ProbePhasePostRoute)
			if (err != nil) != tt.wantErr {
				t.Fatalf("gate error = %v, want error=%v", err, tt.wantErr)
			}
		})
	}
}
