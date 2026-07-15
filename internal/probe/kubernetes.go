package probe

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

var supportedKinds = map[string]bool{
	"Pod": true, "Deployment": true, "DaemonSet": true, "StatefulSet": true,
	"Service": true, "EndpointSlice": true,
}

// ExecuteKubernetes checks a supported object without polling faster than the
// caller's reconciliation interval. It reads only the referenced namespace.
func ExecuteKubernetes(ctx context.Context, reader client.Reader, target *netcupv1.KubernetesReadinessTarget) Result {
	if target == nil || !supportedKinds[target.Kind] || target.Name == "" {
		return Result{Reason: "unsupported Kubernetes readiness target"}
	}
	gv, err := schema.ParseGroupVersion(target.APIVersion)
	if err != nil {
		return Result{Reason: "invalid Kubernetes apiVersion"}
	}
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(gv.WithKind(target.Kind))
	if err := reader.Get(ctx, client.ObjectKey{Name: target.Name, Namespace: target.Namespace}, object); err != nil {
		return Result{Reason: "Kubernetes object is not readable"}
	}
	if target.JSONPath == "" {
		return Result{Success: true}
	}
	value, found, err := unstructured.NestedFieldNoCopy(object.Object, strings.Split(strings.TrimPrefix(target.JSONPath, "."), ".")...)
	if err != nil || !found {
		return Result{Reason: "readiness field is absent"}
	}
	if target.Expected != "" && fmt.Sprint(value) != target.Expected {
		return Result{Reason: "readiness field has unexpected value"}
	}
	return Result{Success: true}
}
