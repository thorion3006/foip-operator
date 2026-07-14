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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

type fakeFailoverIPClient struct {
	findFOIPID  int
	serverID    int
	routeTarget int
	findErr     error
	routeErr    error
	routeCalled bool
}

func (f *fakeFailoverIPClient) FindFailoverIP(context.Context, string) (int, int, error) {
	return f.findFOIPID, f.serverID, f.findErr
}

func (f *fakeFailoverIPClient) RouteFailoverIP(context.Context, int, int) error {
	f.routeCalled = true
	if f.routeErr == nil && f.routeTarget != 0 {
		f.serverID = f.routeTarget
	}
	return f.routeErr
}

func TestFailoverIpReconciler_SelectsNodeAndUpdatesStatus(t *testing.T) {
	t.Helper()

	const (
		resourceName = "test-resource"
		namespace    = "default"
		nodeName     = "node-1"
		secretName   = "netcup-scp-credentials"
		failoverIP   = "1.2.3.4"
		serverID     = "12345"
		macAddress   = "de:ad:be:ef:00:01"
	)

	ctx := context.Background()
	fakeClient := &fakeFailoverIPClient{findFOIPID: 17, serverID: 0}
	originalNewFailoverIPClient := newFailoverIPClient
	newFailoverIPClient = func(int, string) failoverIPClient {
		return fakeClient
	}
	t.Cleanup(func() {
		newFailoverIPClient = originalNewFailoverIPClient
	})

	resource := &netcupv1.FailoverIp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: namespace,
		},
		Spec: netcupv1.FailoverIpSpec{
			IP:         failoverIP,
			SecretName: secretName,
		},
	}
	if err := k8sClient.Create(ctx, resource); err != nil {
		t.Fatalf("create failoverip: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, &netcupv1.FailoverIp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
		})
	})

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"userId":       []byte("42"),
			"refreshToken": []byte("refresh-token"),
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
		})
	})

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				netcupv1.ServerIDAnnotation: serverID,
				netcupv1.MACAnnotation:      macAddress,
			},
		},
	}
	if err := k8sClient.Create(ctx, node); err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}})
	})

	controllerReconciler := &FailoverIpReconciler{
		Client:    k8sClient,
		APIReader: k8sClient,
		Scheme:    k8sClient.Scheme(),
	}

	_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: resourceName, Namespace: namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: resourceName, Namespace: namespace},
	})
	if err != nil {
		t.Fatalf("select target: %v", err)
	}
	if fakeClient.routeCalled {
		t.Fatalf("route client should not have been called during target selection")
	}

	var updated netcupv1.FailoverIp
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: namespace}, &updated); err != nil {
		t.Fatalf("get updated failoverip: %v", err)
	}
	if updated.Status.TargetNode != nodeName {
		t.Fatalf("targetNode = %q, want %q", updated.Status.TargetNode, nodeName)
	}
	if len(updated.Status.LocalOwners) != 0 {
		t.Fatalf("localOwners = %v, want empty", updated.Status.LocalOwners)
	}
}

func TestFailoverIpReconciler_CompletesMakeBeforeBreakHandoff(t *testing.T) {
	t.Helper()

	const (
		resourceName = "handoff-resource"
		namespace    = "default"
		nodeName     = "node-1"
		secretName   = "netcup-scp-credentials"
		failoverIP   = "1.2.3.4"
		serverID     = "12345"
		macAddress   = "de:ad:be:ef:00:01"
	)

	ctx := context.Background()
	fakeClient := &fakeFailoverIPClient{findFOIPID: 17, serverID: 0, routeTarget: 12345}
	originalNewFailoverIPClient := newFailoverIPClient
	newFailoverIPClient = func(int, string) failoverIPClient {
		return fakeClient
	}
	t.Cleanup(func() {
		newFailoverIPClient = originalNewFailoverIPClient
	})

	resource := &netcupv1.FailoverIp{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: namespace,
		},
		Spec: netcupv1.FailoverIpSpec{
			IP:         failoverIP,
			SecretName: secretName,
		},
		Status: netcupv1.FailoverIpStatus{
			TransitionID: "transition-1",
			Phase:        netcupv1.FailoverPhaseTargetPrepared,
			TargetNode:   nodeName,
			LocalOwners:  []string{nodeName},
		},
	}
	if err := k8sClient.Create(ctx, resource); err != nil {
		t.Fatalf("create failoverip: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, &netcupv1.FailoverIp{
			ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: namespace},
		})
	})

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"userId":       []byte("42"),
			"refreshToken": []byte("refresh-token"),
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
		})
	})

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Annotations: map[string]string{
				netcupv1.ServerIDAnnotation: serverID,
				netcupv1.MACAnnotation:      macAddress,
			},
		},
	}
	if err := k8sClient.Create(ctx, node); err != nil {
		t.Fatalf("create node: %v", err)
	}
	t.Cleanup(func() {
		_ = k8sClient.Delete(ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}})
	})

	controllerReconciler := &FailoverIpReconciler{
		Client:    k8sClient,
		APIReader: k8sClient,
		Scheme:    k8sClient.Scheme(),
	}

	_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: resourceName, Namespace: namespace},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !fakeClient.routeCalled {
		t.Fatalf("route client should have been called for committed handoff")
	}

	var updated netcupv1.FailoverIp
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: namespace}, &updated); err != nil {
		t.Fatalf("get updated failoverip: %v", err)
	}
	if updated.Status.SourceNode != nodeName {
		t.Fatalf("sourceNode = %q, want %q", updated.Status.SourceNode, nodeName)
	}
	if updated.Status.LastConfirmedProviderMutationAt == nil {
		t.Fatalf("lastConfirmedProviderMutationAt = nil, want timestamp")
	}
}
