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
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	netcupv1 "github.com/niklasbeierl/foip-operator/api/v1"
	"github.com/niklasbeierl/foip-operator/internal/netcup"
)

// FailoverIpReconciler reconciles FailoverIp objects and drives the netcup API.
// It runs as a Deployment with leader election; only one instance is active at a time.
type FailoverIpReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=foip.noshoes.xyz,resources=failoverips,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=foip.noshoes.xyz,resources=failoverips/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

func (r *FailoverIpReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var foip netcupv1.FailoverIp
	if err := r.Get(ctx, req.NamespacedName, &foip); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList); err != nil {
		return ctrl.Result{}, err
	}
	candidates := candidateNodes(nodeList.Items)

	better := betterNode(candidates, foip.Status.DesiredNode)
	if better != nil {
		patch := client.MergeFrom(foip.DeepCopy())
		foip.Status.DesiredNode = better.Name
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Updating desiredNode", "node", better.Name)
	}

	if foip.Status.DesiredNode == "" || foip.Status.DesiredNode == foip.Status.AssignedNode {
		return ctrl.Result{}, nil
	}

	// Resolve the desired node's annotations.
	var targetNode corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: foip.Status.DesiredNode}, &targetNode); err != nil {
		return ctrl.Result{}, err
	}
	mac := targetNode.Annotations[netcupv1.MACAnnotation]
	vserverName := targetNode.Annotations[netcupv1.ServerNameAnnotation]
	if mac == "" || vserverName == "" {
		return ctrl.Result{}, fmt.Errorf("node %s is missing required annotations", foip.Status.DesiredNode)
	}

	// Fetch netcup credentials.
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: foip.Spec.SecretName, Namespace: req.Namespace}, &secret); err != nil {
		return ctrl.Result{}, fmt.Errorf("fetching secret %s: %w", foip.Spec.SecretName, err)
	}
	login := string(secret.Data["loginName"])
	password := string(secret.Data["password"])
	if login == "" || password == "" {
		return ctrl.Result{}, fmt.Errorf("secret %s missing loginName or password", foip.Spec.SecretName)
	}

	// Record the attempt before touching the API.
	patch := client.MergeFrom(foip.DeepCopy())
	foip.Status.LastSyncAttempt = time.Now().UTC().Format(time.RFC3339)
	if err := r.Status().Patch(ctx, &foip, patch); err != nil {
		return ctrl.Result{}, err
	}

	nc := netcup.New(login, password)

	ips, err := nc.GetVServerIPs(ctx, vserverName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getVServerIPs: %w", err)
	}

	if !slices.Contains(ips, foip.Spec.IP) {
		log.Info("Routing IP via netcup API", "ip", foip.Spec.IP, "node", foip.Status.DesiredNode)
		if err := nc.ChangeIPRouting(ctx, foip.Spec.IP, 32, vserverName, mac); err != nil {
			return ctrl.Result{}, fmt.Errorf("changeIPRouting: %w", err)
		}
	} else {
		log.Info("IP already routed to target node in netcup", "ip", foip.Spec.IP, "node", foip.Status.DesiredNode)
	}

	patch = client.MergeFrom(foip.DeepCopy())
	foip.Status.AssignedNode = foip.Status.DesiredNode
	foip.Status.LastSyncSuccess = time.Now().UTC().Format(time.RFC3339)
	if err := r.Status().Patch(ctx, &foip, patch); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// nodeChangePredicate filters Node update events to only those that affect scoring
// (conditions, unschedulable, or the required annotations). This avoids reconciling
// all FailoverIps on every kubelet heartbeat.
type nodeChangePredicate struct {
	predicate.Funcs
}

func (nodeChangePredicate) Update(e event.UpdateEvent) bool {
	oldNode, ok1 := e.ObjectOld.(*corev1.Node)
	newNode, ok2 := e.ObjectNew.(*corev1.Node)
	if !ok1 || !ok2 {
		return true
	}
	if oldNode.Spec.Unschedulable != newNode.Spec.Unschedulable {
		return true
	}
	if oldNode.Annotations[netcupv1.MACAnnotation] != newNode.Annotations[netcupv1.MACAnnotation] ||
		oldNode.Annotations[netcupv1.ServerNameAnnotation] != newNode.Annotations[netcupv1.ServerNameAnnotation] {
		return true
	}
	for _, condType := range []corev1.NodeConditionType{
		corev1.NodeNetworkUnavailable, corev1.NodeReady,
		corev1.NodePIDPressure, corev1.NodeMemoryPressure, corev1.NodeDiskPressure,
	} {
		if conditionIs(*oldNode, condType, corev1.ConditionTrue) != conditionIs(*newNode, condType, corev1.ConditionTrue) ||
			conditionIs(*oldNode, condType, corev1.ConditionUnknown) != conditionIs(*newNode, condType, corev1.ConditionUnknown) {
			return true
		}
	}
	return false
}

// nodeToFoips maps a Node event to reconcile requests for every FailoverIp.
func (r *FailoverIpReconciler) nodeToFoips(ctx context.Context, _ client.Object) []reconcile.Request {
	var list netcupv1.FailoverIpList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, len(list.Items))
	for i, foip := range list.Items {
		reqs[i] = reconcile.Request{NamespacedName: types.NamespacedName{
			Name:      foip.Name,
			Namespace: foip.Namespace,
		}}
	}
	return reqs
}

func (r *FailoverIpReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&netcupv1.FailoverIp{}).
		Watches(
			&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(r.nodeToFoips),
			builder.WithPredicates(nodeChangePredicate{}),
		).
		Named("failoverip").
		Complete(r)
}
