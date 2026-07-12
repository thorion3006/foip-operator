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

const (
	defaultRequeueTime       = 24 * time.Hour
	preparationPollInterval  = 2 * time.Second
	providerVerifyInterval   = 2 * time.Second
	providerVerifyAttempts   = 15
)

// FailoverIpReconciler implements a two-phase, make-before-break handoff:
//  1. desiredNode is selected while assignedNode remains the old owner.
//  2. the target node agent adds the /32 and reports preparedNode.
//  3. the controller changes and verifies the Netcup route.
//  4. assignedNode advances to desiredNode, allowing old nodes to clean up.
type FailoverIpReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	APIReader client.Reader

	requeueAfter time.Duration
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

	var secret corev1.Secret
	if err := r.APIReader.Get(ctx, types.NamespacedName{Name: foip.Spec.SecretName, Namespace: req.Namespace}, &secret); err != nil {
		return ctrl.Result{}, fmt.Errorf("fetching secret %s: %w", foip.Spec.SecretName, err)
	}
	refreshToken := string(secret.Data["refreshToken"])
	userIDStr := string(secret.Data["userId"])
	if refreshToken == "" || userIDStr == "" {
		return ctrl.Result{}, fmt.Errorf("secret %s missing refreshToken or userId", foip.Spec.SecretName)
	}
	var userID int
	if _, err := fmt.Sscanf(userIDStr, "%d", &userID); err != nil {
		return ctrl.Result{}, fmt.Errorf("secret %s: userId is not an integer: %w", foip.Spec.SecretName, err)
	}

	nc := netcup.New(userID, refreshToken)
	foipID, currentServerID, err := nc.FindFailoverIP(ctx, foip.Spec.IP)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("findFailoverIP: %w", err)
	}

	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList); err != nil {
		return ctrl.Result{}, err
	}
	candidates := candidateNodes(nodeList.Items)
	better := betterNode(candidates, foip.Status.DesiredNode)

	if better != nil && better.Name != foip.Status.DesiredNode {
		patch := client.MergeFrom(foip.DeepCopy())
		foip.Status.DesiredNode = better.Name
		foip.Status.PreparedNode = ""
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("selected failover target", "node", better.Name, "previousAssignedNode", foip.Status.AssignedNode)
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}

	if foip.Status.DesiredNode == "" {
		return ctrl.Result{RequeueAfter: r.requeueAfter}, nil
	}

	var targetNode corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: foip.Status.DesiredNode}, &targetNode); err != nil {
		return ctrl.Result{}, err
	}
	serverIDStr := targetNode.Annotations[netcupv1.ServerIDAnnotation]
	if serverIDStr == "" {
		return ctrl.Result{}, fmt.Errorf("node %s missing annotation %s", foip.Status.DesiredNode, netcupv1.ServerIDAnnotation)
	}
	var targetServerID int
	if _, err := fmt.Sscanf(serverIDStr, "%d", &targetServerID); err != nil {
		return ctrl.Result{}, fmt.Errorf("node %s annotation %s is not an integer: %w", foip.Status.DesiredNode, netcupv1.ServerIDAnnotation, err)
	}

	// Never change the provider route until the target node agent confirms the
	// address is already present on the target public interface.
	if foip.Status.PreparedNode != foip.Status.DesiredNode {
		log.Info("waiting for target node to prepare failover IP", "desiredNode", foip.Status.DesiredNode, "preparedNode", foip.Status.PreparedNode)
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}

	if currentServerID != targetServerID {
		patch := client.MergeFrom(foip.DeepCopy())
		foip.Status.LastSyncAttempt = time.Now().UTC().Format(time.RFC3339)
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}

		log.Info("routing failover IP through Netcup", "ip", foip.Spec.IP, "serverID", targetServerID, "node", foip.Status.DesiredNode)
		if err := nc.RouteFailoverIP(ctx, foipID, targetServerID); err != nil {
			return ctrl.Result{}, fmt.Errorf("routeFailoverIP: %w", err)
		}
	}

	// Netcup operations are asynchronous. Re-read the authoritative provider
	// state before advancing assignedNode and allowing stale-owner cleanup.
	verified := false
	for attempt := 0; attempt < providerVerifyAttempts; attempt++ {
		_, observedServerID, verifyErr := nc.FindFailoverIP(ctx, foip.Spec.IP)
		if verifyErr != nil {
			return ctrl.Result{}, fmt.Errorf("verifying failover route: %w", verifyErr)
		}
		if observedServerID == targetServerID {
			verified = true
			break
		}
		select {
		case <-ctx.Done():
			return ctrl.Result{}, ctx.Err()
		case <-time.After(providerVerifyInterval):
		}
	}
	if !verified {
		// Keep both old and new local owners in place. This avoids breaking the
		// old path while provider convergence is uncertain.
		return ctrl.Result{}, fmt.Errorf("provider route did not converge to server %d; retaining old and new local ownership", targetServerID)
	}

	patch := client.MergeFrom(foip.DeepCopy())
	foip.Status.AssignedNode = foip.Status.DesiredNode
	foip.Status.LastSyncSuccess = time.Now().UTC().Format(time.RFC3339)
	if err := r.Status().Patch(ctx, &foip, patch); err != nil {
		return ctrl.Result{}, err
	}
	log.Info("failover handoff committed; stale owners may clean up", "assignedNode", foip.Status.AssignedNode, "serverID", targetServerID)

	return ctrl.Result{RequeueAfter: r.requeueAfter}, nil
}

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
		oldNode.Annotations[netcupv1.ServerIDAnnotation] != newNode.Annotations[netcupv1.ServerIDAnnotation] {
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

func (r *FailoverIpReconciler) nodeToFoips(ctx context.Context, _ client.Object) []reconcile.Request {
	var list netcupv1.FailoverIpList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, len(list.Items))
	for i, foip := range list.Items {
		reqs[i] = reconcile.Request{NamespacedName: types.NamespacedName{Name: foip.Name, Namespace: foip.Namespace}}
	}
	return reqs
}

func (r *FailoverIpReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.APIReader = mgr.GetAPIReader()
	r.requeueAfter = defaultRequeueTime
	return ctrl.NewControllerManagedBy(mgr).
		For(&netcupv1.FailoverIp{}, builder.WithPredicates(predicate.ResourceVersionChangedPredicate{})).
		Watches(
			&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(r.nodeToFoips),
			builder.WithPredicates(nodeChangePredicate{}),
		).
		Named("failoverip").
		Complete(r)
}
