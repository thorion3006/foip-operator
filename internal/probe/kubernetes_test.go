package probe

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

func TestExecuteKubernetes(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "default"},
		"status":   map[string]any{"readyReplicas": int64(2)},
	}}
	object.SetGroupVersionKind(schemaFrom("apps/v1", "Deployment"))
	reader := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithRuntimeObjects(object).Build()
	result := ExecuteKubernetes(context.Background(), reader, &netcupv1.KubernetesReadinessTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "default", JSONPath: ".status.readyReplicas", Expected: "2"})
	if !result.Success {
		t.Fatalf("readiness failed: %s", result.Reason)
	}
}

func TestExecuteKubernetesReadinessFailures(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "default"},
		"status":   map[string]any{"readyReplicas": int64(2)},
	}}
	object.SetGroupVersionKind(schemaFrom("apps/v1", "Deployment"))
	reader := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithRuntimeObjects(object).Build()

	tests := []struct {
		name   string
		target *netcupv1.KubernetesReadinessTarget
		reason string
	}{
		{name: "unsupported kind", target: &netcupv1.KubernetesReadinessTarget{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web"}, reason: "unsupported Kubernetes readiness target"},
		{name: "invalid api version", target: &netcupv1.KubernetesReadinessTarget{APIVersion: "apps/v1/extra", Kind: "Deployment", Name: "web"}, reason: "invalid Kubernetes apiVersion"},
		{name: "missing object", target: &netcupv1.KubernetesReadinessTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "missing", Namespace: "default"}, reason: "Kubernetes object is not readable"},
		{name: "wrong namespace", target: &netcupv1.KubernetesReadinessTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "other"}, reason: "Kubernetes object is not readable"},
		{name: "missing readiness field", target: &netcupv1.KubernetesReadinessTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "default", JSONPath: ".status.availableReplicas"}, reason: "readiness field is absent"},
		{name: "unexpected readiness value", target: &netcupv1.KubernetesReadinessTarget{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", Namespace: "default", JSONPath: ".status.readyReplicas", Expected: "3"}, reason: "readiness field has unexpected value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExecuteKubernetes(context.Background(), reader, tt.target)
			if result.Success || result.Reason != tt.reason {
				t.Fatalf("result = %#v, want failure reason %q", result, tt.reason)
			}
		})
	}
}

func TestExecuteKubernetesReadinessWithoutJSONPath(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "web", "namespace": "default"},
	}}
	object.SetGroupVersionKind(schemaFrom("v1", "Pod"))
	reader := fake.NewClientBuilder().WithScheme(runtime.NewScheme()).WithRuntimeObjects(object).Build()

	result := ExecuteKubernetes(context.Background(), reader, &netcupv1.KubernetesReadinessTarget{
		APIVersion: "v1", Kind: "Pod", Name: "web", Namespace: "default",
	})
	if !result.Success {
		t.Fatalf("object existence readiness failed: %s", result.Reason)
	}
}

func schemaFrom(version, kind string) schema.GroupVersionKind {
	gv, _ := schema.ParseGroupVersion(version)
	return gv.WithKind(kind)
}
