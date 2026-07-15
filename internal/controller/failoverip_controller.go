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
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
	"github.com/thorion3006/foip-operator/internal/netcup"
	"github.com/thorion3006/foip-operator/internal/observability"
)

const (
	defaultRequeueTime      = 24 * time.Hour
	preparationPollInterval = 2 * time.Second
	providerVerifyInterval  = 2 * time.Second
	providerVerifyAttempts  = 15
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

type failoverIPClient interface {
	FindFailoverIP(ctx context.Context, ip string) (foipID int, serverID int, err error)
	RouteFailoverIP(ctx context.Context, foipID, targetServerID int) error
}

var newFailoverIPClient = func(userID int, refreshToken string) failoverIPClient {
	return netcup.New(userID, refreshToken)
}

// +kubebuilder:rbac:groups=foip.noshoes.xyz,resources=failoverips;failoverprobes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=foip.noshoes.xyz,resources=failoverips/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
func (r *FailoverIpReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) { //nolint:gocyclo // this method coordinates persisted safety gates
	start := time.Now()
	ctx, span := observability.StartSpan(ctx, "foip-operator.failoverip", "Reconcile",
		attribute.String("k8s.namespace", req.Namespace),
		attribute.String("k8s.name", req.Name),
	)
	defer func() {
		observability.RecordSpanError(span, err)
		span.End()
		result := "success"
		switch {
		case err != nil:
			result = "error"
		case res.RequeueAfter > 0:
			result = "requeue_after"
		}
		observability.ObserveReconcile("failoverip", result, time.Since(start))
	}()

	log := observability.Logger(ctx, logf.FromContext(ctx))

	var foip netcupv1.FailoverIp
	if err := r.Get(ctx, req.NamespacedName, &foip); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if foip.Status.TransitionID == "" {
		patch := client.MergeFrom(foip.DeepCopy())
		now := metav1.Now()
		netcupv1.StartTransition(&foip.Status, now)
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionReady, metav1.ConditionFalse, "Selecting", "Selecting a failover target", now)
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}

	var secret corev1.Secret
	secretStart := time.Now()
	if err := r.APIReader.Get(ctx, types.NamespacedName{Name: foip.Spec.SecretName, Namespace: req.Namespace}, &secret); err != nil {
		observability.ObserveProviderCall("kubernetes", "get_secret", time.Since(secretStart), err)
		return ctrl.Result{}, fmt.Errorf("fetching secret %s: %w", foip.Spec.SecretName, err)
	}
	observability.ObserveProviderCall("kubernetes", "get_secret", time.Since(secretStart), nil)
	refreshToken := string(secret.Data["refreshToken"])
	userIDStr := string(secret.Data["userId"])
	if refreshToken == "" || userIDStr == "" {
		return ctrl.Result{}, fmt.Errorf("secret %s missing refreshToken or userId", foip.Spec.SecretName)
	}
	var userID int
	if _, err := fmt.Sscanf(userIDStr, "%d", &userID); err != nil {
		return ctrl.Result{}, fmt.Errorf("secret %s: userId is not an integer: %w", foip.Spec.SecretName, err)
	}

	nc := newFailoverIPClient(userID, refreshToken)
	findStart := time.Now()
	foipID, currentServerID, err := nc.FindFailoverIP(ctx, foip.Spec.IP)
	observability.ObserveProviderCall("netcup", "find_failover_ip", time.Since(findStart), err)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("findFailoverIP: %w", err)
	}
	if err := validateProviderFence(foip.Status, strconv.Itoa(currentServerID), ""); err != nil {
		patch := client.MergeFrom(foip.DeepCopy())
		foip.Status.Phase = netcupv1.FailoverPhaseBlocked
		foip.Status.LastError = err.Error()
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionBlocked, metav1.ConditionTrue, "ProviderOwnershipChanged", "Provider ownership changed out of band", metav1.Now())
		if patchErr := r.Status().Patch(ctx, &foip, patch); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		return ctrl.Result{}, err
	}
	foip.Status.ProviderObservedOwner = strconv.Itoa(currentServerID)

	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList); err != nil {
		return ctrl.Result{}, err
	}
	candidates := candidateNodes(nodeList.Items)
	better := betterNode(candidates, foip.Status.TargetNode)

	if better != nil && better.Name != foip.Status.TargetNode {
		if foip.Status.Phase == netcupv1.FailoverPhaseSucceeded || foip.Status.Phase == netcupv1.FailoverPhaseDegraded {
			patch := client.MergeFrom(foip.DeepCopy())
			now := metav1.Now()
			if err := netcupv1.AdvanceTransition(&foip.Status, netcupv1.FailoverPhaseSelecting, now); err != nil {
				return ctrl.Result{}, err
			}
			foip.Status.CandidateSince = nil
			if err := r.Status().Patch(ctx, &foip, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
		}
		candidate := *better
		if foip.Status.CandidateSince == nil {
			patch := client.MergeFrom(foip.DeepCopy())
			now := metav1.Now()
			foip.Status.CandidateSince = &now
			foip.Status.CandidateReason = "healthiest candidate selected"
			if foip.Status.Phase == netcupv1.FailoverPhaseSelecting {
				if err := netcupv1.AdvanceTransition(&foip.Status, netcupv1.FailoverPhaseStabilizing, now); err != nil {
					return ctrl.Result{}, err
				}
				netcupv1.SetCondition(&foip.Status, netcupv1.ConditionStabilizing, metav1.ConditionTrue, "CandidateSelected", "Candidate is within the stabilization window", now)
			}
			if err := r.Status().Patch(ctx, &foip, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: stabilizationWindow(foip.Spec)}, nil
		}
		if !candidateReadyForHandoff(foip.Spec, foip.Status, candidate, time.Now()) {
			remaining := time.Until(foip.Status.CandidateSince.Add(stabilizationWindow(foip.Spec) + minHealthyWindow(foip.Spec)))
			remaining = max(remaining, time.Second)
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
		patch := client.MergeFrom(foip.DeepCopy())
		foip.Status.SourceNode = foip.Status.TargetNode
		foip.Status.TargetNode = better.Name
		foip.Status.LocalOwners = nil
		foip.Status.CandidateSince = nil
		foip.Status.CandidateReason = ""
		if foip.Status.Phase == netcupv1.FailoverPhaseStabilizing {
			now := metav1.Now()
			if err := netcupv1.AdvanceTransition(&foip.Status, netcupv1.FailoverPhasePreparingTarget, now); err != nil {
				return ctrl.Result{}, err
			}
			netcupv1.SetCondition(&foip.Status, netcupv1.ConditionStabilizing, metav1.ConditionFalse, "WindowElapsed", "Candidate remained healthy through stabilization", now)
		}
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Selected failover target", "node", better.Name, "previousSourceNode", foip.Status.SourceNode)
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}

	if foip.Status.TargetNode == "" {
		return ctrl.Result{RequeueAfter: r.requeueAfter}, nil
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseSucceeded {
		return ctrl.Result{RequeueAfter: r.requeueAfter}, nil
	}

	var targetNode corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: foip.Status.TargetNode}, &targetNode); err != nil {
		return ctrl.Result{}, err
	}
	serverIDStr := targetNode.Annotations[netcupv1.ServerIDAnnotation]
	if serverIDStr == "" {
		return ctrl.Result{}, fmt.Errorf("node %s missing annotation %s", foip.Status.TargetNode, netcupv1.ServerIDAnnotation)
	}
	var targetServerID int
	if _, err := fmt.Sscanf(serverIDStr, "%d", &targetServerID); err != nil {
		return ctrl.Result{}, fmt.Errorf("node %s annotation %s is not an integer: %w", foip.Status.TargetNode, netcupv1.ServerIDAnnotation, err)
	}

	// Never change the provider route until the target node agent confirms the
	// address is already present on the target public interface.
	if !containsNode(foip.Status.LocalOwners, foip.Status.TargetNode) {
		log.Info("Waiting for target node to prepare failover IP", "targetNode", foip.Status.TargetNode)
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}
	if foip.Status.Phase == netcupv1.FailoverPhasePreparingTarget {
		patch := client.MergeFrom(foip.DeepCopy())
		now := metav1.Now()
		if err := netcupv1.AdvanceTransition(&foip.Status, netcupv1.FailoverPhaseTargetPrepared, now); err != nil {
			return ctrl.Result{}, err
		}
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionTargetPrepared, metav1.ConditionTrue, "NodeReportedOwnership", "Target node reported local /32 ownership", now)
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseTargetPrepared || foip.Status.Phase == netcupv1.FailoverPhaseRoutingProvider {
		if err := evaluateProbePhase(ctx, r.APIReader, foip, netcupv1.ProbePhasePreRoute); err != nil {
			patch := client.MergeFrom(foip.DeepCopy())
			foip.Status.LastError = err.Error()
			if patchErr := r.Status().Patch(ctx, &foip, patch); patchErr != nil {
				return ctrl.Result{}, patchErr
			}
			return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
		}
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseTargetPrepared {
		patch := client.MergeFrom(foip.DeepCopy())
		now := metav1.Now()
		if err := netcupv1.AdvanceTransition(&foip.Status, netcupv1.FailoverPhaseRoutingProvider, now); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}

	if currentServerID != targetServerID {
		if foip.Status.Phase != netcupv1.FailoverPhaseRoutingProvider {
			return ctrl.Result{RequeueAfter: providerVerifyInterval}, nil
		}
		now := time.Now()
		nextMutation, allowed := providerMutationGate(foip.Status, providerCooldown(foip.Spec), now)
		if !allowed {
			patch := client.MergeFrom(foip.DeepCopy())
			eligible := metav1.NewTime(nextMutation)
			foip.Status.NextEligibleMutationAt = &eligible
			if err := r.Status().Patch(ctx, &foip, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Until(nextMutation)}, nil
		}
		handoffStart := time.Now()
		patch := client.MergeFrom(foip.DeepCopy())
		attemptedAt := metav1.Now()
		foip.Status.LastAttemptedProviderMutationAt = &attemptedAt
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}

		log.Info("Routing failover IP through Netcup", "ip", foip.Spec.IP, "serverID", targetServerID, "node", foip.Status.TargetNode)
		routeStart := time.Now()
		if err := nc.RouteFailoverIP(ctx, foipID, targetServerID); err != nil {
			observability.ObserveProviderCall("netcup", "route_failover_ip", time.Since(routeStart), err)
			patch := client.MergeFrom(foip.DeepCopy())
			foip.Status.RetryCount++
			foip.Status.LastError = err.Error()
			if patchErr := r.Status().Patch(ctx, &foip, patch); patchErr != nil {
				return ctrl.Result{}, patchErr
			}
			return ctrl.Result{}, fmt.Errorf("routeFailoverIP: %w", err)
		}
		observability.ObserveProviderCall("netcup", "route_failover_ip", time.Since(routeStart), nil)

		// Netcup operations are asynchronous. Re-read the authoritative provider
		// state before advancing assignedNode and allowing stale-owner cleanup.
		verified := false
		for range providerVerifyAttempts {
			verifyStart := time.Now()
			_, observedServerID, verifyErr := nc.FindFailoverIP(ctx, foip.Spec.IP)
			observability.ObserveProviderCall("netcup", "verify_failover_ip", time.Since(verifyStart), verifyErr)
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
		observability.ObserveHandoffDuration(time.Since(handoffStart))
		confirmedAt := metav1.Now()
		foip.Status.LastConfirmedProviderMutationAt = &confirmedAt
		foip.Status.ProviderObservedOwner = strconv.Itoa(targetServerID)
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseRoutingProvider {
		now := metav1.Now()
		if err := netcupv1.AdvanceTransition(&foip.Status, netcupv1.FailoverPhaseVerifyingProvider, now); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Status().Update(ctx, &foip); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: providerVerifyInterval}, nil
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseVerifyingProvider {
		if currentServerID != targetServerID {
			return ctrl.Result{RequeueAfter: providerVerifyInterval}, nil
		}
		patch := client.MergeFrom(foip.DeepCopy())
		now := metav1.Now()
		if err := netcupv1.AdvanceTransition(&foip.Status, netcupv1.FailoverPhaseVerifyingTraffic, now); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}
	if err := evaluateProbePhase(ctx, r.APIReader, foip, netcupv1.ProbePhasePostRoute); err != nil {
		commitDegraded, recoveryResult, recoveryErr := r.recoverPostRouteFailure(ctx, &foip, nc, foipID)
		if recoveryErr != nil {
			return ctrl.Result{}, recoveryErr
		}
		if !commitDegraded {
			return recoveryResult, nil
		}
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseVerifyingTraffic {
		patch := client.MergeFrom(foip.DeepCopy())
		now := metav1.Now()
		if err := netcupv1.AdvanceTransition(&foip.Status, netcupv1.FailoverPhaseCommitting, now); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseCommitting {
		patch := client.MergeFrom(foip.DeepCopy())
		now := metav1.Now()
		foip.Status.SourceNode = foip.Status.TargetNode
		if err := netcupv1.AdvanceTransition(&foip.Status, netcupv1.FailoverPhaseCleaningStaleOwners, now); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseCleaningStaleOwners {
		if len(foip.Status.LocalOwners) != 1 || !containsNode(foip.Status.LocalOwners, foip.Status.TargetNode) {
			return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
		}
		patch := client.MergeFrom(foip.DeepCopy())
		now := metav1.Now()
		if err := netcupv1.AdvanceTransition(&foip.Status, netcupv1.FailoverPhaseSucceeded, now); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.requeueAfter}, nil
	}

	patch := client.MergeFrom(foip.DeepCopy())
	previousPhase := foip.Status.Phase
	foip.Status.Phase = netcupv1.FailoverPhaseBlocked
	foip.Status.LastError = fmt.Sprintf("unexpected persisted phase %q", previousPhase)
	if err := r.Status().Patch(ctx, &foip, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, fmt.Errorf("unexpected persisted phase")
}

func containsNode(nodes []string, name string) bool {
	return slices.Contains(nodes, name)
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

func (r *FailoverIpReconciler) probeToFoips(ctx context.Context, obj client.Object) []reconcile.Request {
	var list netcupv1.FailoverIpList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		foip := &list.Items[i]
		for _, ref := range foip.Spec.Probes {
			if ref.Name == obj.GetName() && foip.Namespace == obj.GetNamespace() {
				reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: foip.Name, Namespace: foip.Namespace}})
				break
			}
		}
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
		Watches(
			&netcupv1.FailoverProbe{},
			handler.EnqueueRequestsFromMapFunc(r.probeToFoips),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Named("failoverip").
		Complete(r)
}
