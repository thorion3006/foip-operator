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

func schemaFrom(version, kind string) schema.GroupVersionKind {
	gv, _ := schema.ParseGroupVersion(version)
	return gv.WithKind(kind)
}
