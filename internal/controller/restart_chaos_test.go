/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

type countingFailoverIPClient struct {
	fakeFailoverIPClient
	routeCalls int
}

func (f *countingFailoverIPClient) RouteFailoverIP(ctx context.Context, foipID, targetServerID int) error {
	f.routeCalls++
	return f.fakeFailoverIPClient.RouteFailoverIP(ctx, foipID, targetServerID)
}

func persistStatus(t *testing.T, status netcupv1.FailoverIpStatus) netcupv1.FailoverIpStatus {
	t.Helper()

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal persisted status: %v", err)
	}
	var restored netcupv1.FailoverIpStatus
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal persisted status: %v", err)
	}
	return restored
}

func TestRestartResume_PreservesEveryMutatingPhase(t *testing.T) {
	now := metav1.NewTime(time.Unix(1_000, 0))
	status := netcupv1.FailoverIpStatus{TargetNode: "node-target"}
	netcupv1.StartTransition(&status, now)

	phases := []netcupv1.FailoverPhase{
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
	}

	for i, phase := range phases {
		if status.Phase != phase {
			t.Fatalf("phase %d = %q, want %q", i, status.Phase, phase)
		}
		if err := netcupv1.ValidateStatus(status); err != nil {
			t.Fatalf("phase %q is not persistable: %v", phase, err)
		}

		status = persistStatus(t, status)
		if status.Phase != phase {
			t.Fatalf("restored phase %d = %q, want %q", i, status.Phase, phase)
		}
		if i == len(phases)-1 {
			break
		}
		if phases[i+1] == netcupv1.FailoverPhaseSucceeded {
			status.LocalOwners = []string{"node-target"}
		}
		if err := netcupv1.AdvanceTransition(&status, phases[i+1], now); err != nil {
			t.Fatalf("advance %q -> %q after restart: %v", phase, phases[i+1], err)
		}
	}
}

func TestRestartResume_RejectsIllegalPhaseSkips(t *testing.T) {
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
	}

	for _, from := range phases {
		for _, to := range phases {
			if netcupv1.CanTransition(from, to) {
				continue
			}
			status := netcupv1.FailoverIpStatus{TransitionID: "restart-test", Phase: from}
			if err := netcupv1.AdvanceTransition(&status, to, metav1.Now()); err == nil {
				t.Errorf("accepted illegal persisted phase skip %q -> %q", from, to)
			}
		}
	}
}

func TestRestartResume_PersistsProviderCooldown(t *testing.T) {
	ctx := context.Background()
	const (
		name       = "cooldown-resource"
		namespace  = "default"
		nodeName   = "cooldown-target"
		secretName = "cooldown-secret"
	)

	fakeProvider := &countingFailoverIPClient{fakeFailoverIPClient: fakeFailoverIPClient{findFOIPID: 17, serverID: 1}}
	previousFactory := newFailoverIPClient
	newFailoverIPClient = func(int, string) failoverIPClient { return fakeProvider }
	t.Cleanup(func() { newFailoverIPClient = previousFactory })

	attempted := metav1.NewTime(time.Now())
	resource := &netcupv1.FailoverIp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       netcupv1.FailoverIpSpec{IP: "192.0.2.10", SecretName: secretName, ProviderCooldownSeconds: 3600},
		Status: netcupv1.FailoverIpStatus{
			TransitionID:                    "cooldown-transition",
			Phase:                           netcupv1.FailoverPhaseRoutingProvider,
			TargetNode:                      nodeName,
			LocalOwners:                     []string{nodeName},
			LastAttemptedProviderMutationAt: &attempted,
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
		Data:       map[string][]byte{"userId": []byte("42"), "refreshToken": []byte("token")},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName, Annotations: map[string]string{netcupv1.ServerIDAnnotation: "2"}}}
	client := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithStatusSubresource(&netcupv1.FailoverIp{}).WithObjects(resource, secret, node).Build()
	reconciler := &FailoverIpReconciler{Client: client, APIReader: client, Scheme: k8sClient.Scheme()}

	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}); err != nil {
		t.Fatalf("reconcile persisted cooldown: %v", err)
	}
	if fakeProvider.routeCalls != 0 {
		t.Fatalf("route calls during persisted cooldown = %d, want 0", fakeProvider.routeCalls)
	}
	var restored netcupv1.FailoverIp
	if err := client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &restored); err != nil {
		t.Fatalf("get restored cooldown status: %v", err)
	}
	if restored.Status.NextEligibleMutationAt == nil {
		t.Fatal("nextEligibleMutationAt was not persisted")
	}
	if !restored.Status.NextEligibleMutationAt.After(attempted.Time) {
		t.Fatalf("nextEligibleMutationAt = %v, want after attempted mutation %v", restored.Status.NextEligibleMutationAt, attempted.Time)
	}
}

func TestRestartResume_DoesNotDuplicateProviderMutation(t *testing.T) {
	ctx := context.Background()
	const (
		name       = "resume-resource"
		namespace  = "default"
		nodeName   = "resume-target"
		secretName = "resume-secret"
	)

	fakeProvider := &countingFailoverIPClient{fakeFailoverIPClient: fakeFailoverIPClient{findFOIPID: 17, serverID: 1, routeTarget: 2}}
	previousFactory := newFailoverIPClient
	newFailoverIPClient = func(int, string) failoverIPClient { return fakeProvider }
	t.Cleanup(func() { newFailoverIPClient = previousFactory })

	resource := &netcupv1.FailoverIp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       netcupv1.FailoverIpSpec{IP: "192.0.2.11", SecretName: secretName},
		Status: netcupv1.FailoverIpStatus{
			TransitionID: "resume-transition",
			Phase:        netcupv1.FailoverPhaseRoutingProvider,
			TargetNode:   nodeName,
			LocalOwners:  []string{nodeName},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
		Data:       map[string][]byte{"userId": []byte("42"), "refreshToken": []byte("token")},
	}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName, Annotations: map[string]string{netcupv1.ServerIDAnnotation: "2"}}}
	client := fake.NewClientBuilder().WithScheme(k8sClient.Scheme()).WithStatusSubresource(&netcupv1.FailoverIp{}).WithObjects(resource, secret, node).Build()
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}

	first := &FailoverIpReconciler{Client: client, APIReader: client, Scheme: k8sClient.Scheme()}
	if _, err := first.Reconcile(ctx, request); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if fakeProvider.routeCalls != 1 {
		t.Fatalf("provider mutations after first reconcile = %d, want 1", fakeProvider.routeCalls)
	}
	var persisted netcupv1.FailoverIp
	if err := client.Get(ctx, request.NamespacedName, &persisted); err != nil {
		t.Fatalf("get persisted phase: %v", err)
	}
	if persisted.Status.Phase != netcupv1.FailoverPhaseVerifyingProvider {
		t.Fatalf("persisted phase = %q, want %q", persisted.Status.Phase, netcupv1.FailoverPhaseVerifyingProvider)
	}

	resumed := &FailoverIpReconciler{Client: client, APIReader: client, Scheme: k8sClient.Scheme()}
	if _, err := resumed.Reconcile(ctx, request); err != nil {
		t.Fatalf("reconcile after restart: %v", err)
	}
	if fakeProvider.routeCalls != 1 {
		t.Fatalf("provider mutations after restart = %d, want exactly 1", fakeProvider.routeCalls)
	}
}

func TestRestartResume_UsesPersistedStatusFromEnvtest(t *testing.T) {
	ctx := context.Background()
	const (
		name       = "envtest-restart-resource"
		namespace  = "default"
		nodeName   = "envtest-restart-target"
		secretName = "envtest-restart-secret"
	)

	fakeProvider := &countingFailoverIPClient{fakeFailoverIPClient: fakeFailoverIPClient{
		findFOIPID:  17,
		serverID:    101,
		routeTarget: 202,
	}}
	previousFactory := newFailoverIPClient
	newFailoverIPClient = func(int, string) failoverIPClient { return fakeProvider }
	t.Cleanup(func() { newFailoverIPClient = previousFactory })

	resource := &netcupv1.FailoverIp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       netcupv1.FailoverIpSpec{IP: "192.0.2.12", SecretName: secretName},
	}
	if err := k8sClient.Create(ctx, resource); err != nil {
		t.Fatalf("create failoverip: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, resource) })

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
		Data:       map[string][]byte{"userId": []byte("42"), "refreshToken": []byte("token")},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, secret) })

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:        nodeName,
		Annotations: map[string]string{netcupv1.ServerIDAnnotation: "202"},
	}}
	if err := k8sClient.Create(ctx, node); err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, node) })

	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}
	var persisted netcupv1.FailoverIp
	if err := k8sClient.Get(ctx, request.NamespacedName, &persisted); err != nil {
		t.Fatalf("get created failoverip: %v", err)
	}
	persisted.Status = netcupv1.FailoverIpStatus{
		TransitionID: "envtest-restart-transition",
		Phase:        netcupv1.FailoverPhaseRoutingProvider,
		TargetNode:   nodeName,
		LocalOwners:  []string{nodeName},
	}
	if err := k8sClient.Status().Update(ctx, &persisted); err != nil {
		t.Fatalf("persist routing status: %v", err)
	}

	first := &FailoverIpReconciler{Client: k8sClient, APIReader: k8sClient, Scheme: k8sClient.Scheme()}
	if _, err := first.Reconcile(ctx, request); err != nil {
		t.Fatalf("reconcile before restart: %v", err)
	}
	if fakeProvider.routeCalls != 1 {
		t.Fatalf("provider mutations before restart = %d, want 1", fakeProvider.routeCalls)
	}
	if err := k8sClient.Get(ctx, request.NamespacedName, &persisted); err != nil {
		t.Fatalf("get status after first reconcile: %v", err)
	}
	if persisted.Status.Phase != netcupv1.FailoverPhaseVerifyingProvider {
		t.Fatalf("persisted phase = %q, want %q", persisted.Status.Phase, netcupv1.FailoverPhaseVerifyingProvider)
	}
	if persisted.Status.TransitionID != "envtest-restart-transition" {
		t.Fatalf("persisted transition ID = %q, want durable transition identity", persisted.Status.TransitionID)
	}

	resumed := &FailoverIpReconciler{Client: k8sClient, APIReader: k8sClient, Scheme: k8sClient.Scheme()}
	if _, err := resumed.Reconcile(ctx, request); err != nil {
		t.Fatalf("reconcile after restart: %v", err)
	}
	if fakeProvider.routeCalls != 1 {
		t.Fatalf("provider mutations after restart = %d, want exactly 1", fakeProvider.routeCalls)
	}
}

func TestLeaderChange_FencesOutOfBandProviderOwnerFromPersistedStatus(t *testing.T) {
	ctx := context.Background()
	const (
		name       = "envtest-fence-resource"
		namespace  = "default"
		nodeName   = "envtest-fence-target"
		secretName = "envtest-fence-secret"
	)

	fakeProvider := &countingFailoverIPClient{fakeFailoverIPClient: fakeFailoverIPClient{
		findFOIPID:  17,
		serverID:    202,
		routeTarget: 303,
	}}
	previousFactory := newFailoverIPClient
	newFailoverIPClient = func(int, string) failoverIPClient { return fakeProvider }
	t.Cleanup(func() { newFailoverIPClient = previousFactory })

	resource := &netcupv1.FailoverIp{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       netcupv1.FailoverIpSpec{IP: "192.0.2.13", SecretName: secretName},
	}
	if err := k8sClient.Create(ctx, resource); err != nil {
		t.Fatalf("create failoverip: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, resource) })

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
		Data:       map[string][]byte{"userId": []byte("42"), "refreshToken": []byte("token")},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, secret) })

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:        nodeName,
		Annotations: map[string]string{netcupv1.ServerIDAnnotation: "303"},
	}}
	if err := k8sClient.Create(ctx, node); err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(ctx, node) })

	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: namespace}}
	var persisted netcupv1.FailoverIp
	if err := k8sClient.Get(ctx, request.NamespacedName, &persisted); err != nil {
		t.Fatalf("get created failoverip: %v", err)
	}
	persisted.Status = netcupv1.FailoverIpStatus{
		TransitionID:          "envtest-fence-transition",
		Phase:                 netcupv1.FailoverPhaseRoutingProvider,
		TargetNode:            nodeName,
		LocalOwners:           []string{nodeName},
		ProviderObservedOwner: "101",
	}
	if err := k8sClient.Status().Update(ctx, &persisted); err != nil {
		t.Fatalf("persist fencing status: %v", err)
	}

	resumed := &FailoverIpReconciler{Client: k8sClient, APIReader: k8sClient, Scheme: k8sClient.Scheme()}
	if _, err := resumed.Reconcile(ctx, request); err == nil {
		t.Fatal("reconcile unexpectedly accepted an out-of-band provider owner")
	}
	if fakeProvider.routeCalls != 0 {
		t.Fatalf("provider mutations after fencing = %d, want 0", fakeProvider.routeCalls)
	}
	if err := k8sClient.Get(ctx, request.NamespacedName, &persisted); err != nil {
		t.Fatalf("get fenced status: %v", err)
	}
	if persisted.Status.Phase != netcupv1.FailoverPhaseBlocked {
		t.Fatalf("fenced phase = %q, want %q", persisted.Status.Phase, netcupv1.FailoverPhaseBlocked)
	}
	if persisted.Status.ProviderObservedOwner != "101" {
		t.Fatalf("fenced observed owner = %q, want persisted owner 101", persisted.Status.ProviderObservedOwner)
	}
}

func TestRestartResume_ReconcilesEveryPersistedPhase(t *testing.T) {
	ctx := context.Background()
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
			name := fmt.Sprintf("phase-%s", strings.ToLower(string(phase)))
			secretName := name + "-secret"
			nodeName := name + "-target"
			resource := &netcupv1.FailoverIp{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec:       netcupv1.FailoverIpSpec{IP: "192.0.2.20", SecretName: secretName},
			}
			if err := k8sClient.Create(ctx, resource); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, resource) })
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"}, Data: map[string][]byte{"userId": []byte("42"), "refreshToken": []byte("token")}}
			if err := k8sClient.Create(ctx, secret); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, secret) })
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName, Annotations: map[string]string{netcupv1.ServerIDAnnotation: "202"}}}
			if err := k8sClient.Create(ctx, node); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, node) })

			providerOwner := 101
			routeTarget := 0
			if phase == netcupv1.FailoverPhaseTargetPrepared || phase == netcupv1.FailoverPhaseRoutingProvider {
				providerOwner = 101
				routeTarget = 202
			} else if phase == netcupv1.FailoverPhaseVerifyingProvider || phase == netcupv1.FailoverPhaseVerifyingTraffic || phase == netcupv1.FailoverPhaseCommitting || phase == netcupv1.FailoverPhaseCleaningStaleOwners || phase == netcupv1.FailoverPhaseSucceeded {
				providerOwner = 202
			}
			provider := &countingFailoverIPClient{fakeFailoverIPClient: fakeFailoverIPClient{findFOIPID: 17, serverID: providerOwner, routeTarget: routeTarget}}
			previousFactory := newFailoverIPClient
			newFailoverIPClient = func(int, string) failoverIPClient { return provider }
			t.Cleanup(func() { newFailoverIPClient = previousFactory })

			var persisted netcupv1.FailoverIp
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: "default"}, &persisted); err != nil {
				t.Fatal(err)
			}
			persisted.Status = netcupv1.FailoverIpStatus{
				TransitionID: "restart-" + name,
				Phase:        phase,
				SourceNode:   "source-" + name,
				TargetNode:   nodeName,
				LocalOwners:  []string{nodeName},
			}
			if phase == netcupv1.FailoverPhaseSucceeded || phase == netcupv1.FailoverPhaseCleaningStaleOwners || phase == netcupv1.FailoverPhaseCommitting || phase == netcupv1.FailoverPhaseIdle {
				persisted.Status.SourceNode = nodeName
			}
			if err := k8sClient.Status().Update(ctx, &persisted); err != nil {
				t.Fatal(err)
			}

			request := reconcile.Request{NamespacedName: client.ObjectKey{Name: name, Namespace: "default"}}
			first := &FailoverIpReconciler{Client: k8sClient, APIReader: k8sClient, Scheme: k8sClient.Scheme()}
			if _, err := first.Reconcile(ctx, request); err != nil {
				t.Fatalf("first reconcile: %v", err)
			}
			second := &FailoverIpReconciler{Client: k8sClient, APIReader: k8sClient, Scheme: k8sClient.Scheme()}
			if _, err := second.Reconcile(ctx, request); err != nil {
				t.Fatalf("reconcile after restart: %v", err)
			}
			if provider.routeCalls > 1 {
				t.Fatalf("provider mutations after restart = %d, want at most one", provider.routeCalls)
			}
			if err := k8sClient.Get(ctx, request.NamespacedName, &persisted); err != nil {
				t.Fatal(err)
			}
			if err := netcupv1.ValidateStatus(persisted.Status); err != nil {
				t.Fatalf("persisted status after restart: %v", err)
			}
		})
	}
}
