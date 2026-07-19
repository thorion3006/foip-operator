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
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
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

// FailoverIpReconciler implements a persisted, make-before-break state machine:
// candidate selection and stabilization precede local preparation, provider
// routing, traffic verification, commit, and stale-owner cleanup.
type FailoverIpReconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	APIReader client.Reader
	Recorder  events.EventRecorder
	Events    *observability.EventDeduper

	requeueAfter time.Duration
}

type failoverIPClient interface {
	FindFailoverIP(ctx context.Context, ip string) (foipID int, serverID int, err error)
	RouteFailoverIP(ctx context.Context, foipID, targetServerID int) error
}

var newFailoverIPClient = func(userID int, refreshToken string) failoverIPClient {
	return netcup.NewFromEnvironment(userID, refreshToken)
}

// +kubebuilder:rbac:groups=foip.noshoes.xyz,resources=failoverips;failoverprobes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=foip.noshoes.xyz,resources=failoverips/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
func (r *FailoverIpReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) { //nolint:gocyclo // this method coordinates persisted safety gates
	start := time.Now()
	currentPhase := ""
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
		observability.ObservePhase(currentPhase, time.Since(start))
	}()

	log := observability.Logger(ctx, logf.FromContext(ctx))

	var foip netcupv1.FailoverIp
	if err := r.Get(ctx, req.NamespacedName, &foip); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	currentPhase = string(foip.Status.Phase)
	observability.ObservePhaseState(currentPhase)
	span.SetAttributes(attribute.String("foip.transition_id", foip.Status.TransitionID), attribute.String("foip.phase", currentPhase))
	log = log.WithValues("transitionID", foip.Status.TransitionID, "phase", foip.Status.Phase, "sourceNode", foip.Status.SourceNode, "targetNode", foip.Status.TargetNode)
	if foip.Status.TransitionID == "" {
		patch := client.MergeFrom(foip.DeepCopy())
		now := metav1.Now()
		netcupv1.StartTransition(&foip.Status, now)
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionReady, metav1.ConditionFalse, "Selecting", "Selecting a failover target", now)
		r.emitEvent(&foip, corev1.EventTypeNormal, "TransitionStarted", "Started failover transition")
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}
	if err := netcupv1.ValidateFailoverIpSpec(foip.Spec); err != nil {
		return ctrl.Result{}, r.persistInvalidSpec(ctx, &foip, err)
	}
	if err := netcupv1.ValidateStatus(foip.Status); err != nil {
		patch := client.MergeFrom(foip.DeepCopy())
		now := metav1.Now()
		foip.Status.Phase = netcupv1.FailoverPhaseBlocked
		foip.Status.LastError = "invalid persisted failover status"
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionBlocked, metav1.ConditionTrue, "InvalidStatus", "Persisted failover status is contradictory", now)
		r.emitEvent(&foip, corev1.EventTypeWarning, "InvalidStatus", "Blocked contradictory persisted failover status")
		if patchErr := r.Status().Patch(ctx, &foip, patch); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		return ctrl.Result{}, err
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseBlocked || foip.Status.Phase == netcupv1.FailoverPhaseDegraded {
		token := foip.Annotations[netcupv1.ManualReconcileAnnotation]
		if token != "" && token != foip.Status.ManualReconcileToken {
			patch := client.MergeFrom(foip.DeepCopy())
			now := metav1.Now()
			if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhaseSelecting, now); err != nil {
				return ctrl.Result{}, err
			}
			foip.Status.ManualReconcileToken = token
			foip.Status.LastError = ""
			foip.Status.RecoveryAction = ""
			foip.Status.RecoveryAttempts = 0
			foip.Status.CandidateSince = nil
			netcupv1.SetCondition(&foip.Status, netcupv1.ConditionReady, metav1.ConditionFalse, "ManualRetry", "Manual reconciliation requested", now)
			r.emitEvent(&foip, corev1.EventTypeNormal, "ManualRetry", "Resuming blocked or degraded transition")
			if err := r.Status().Patch(ctx, &foip, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
		}
		return ctrl.Result{RequeueAfter: r.requeueAfter}, nil
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
	findCtx, findSpan := observability.StartSpan(ctx, "foip-operator.provider", "FindFailoverIP", attribute.String("foip.transition_id", foip.Status.TransitionID), attribute.String("foip.provider", "netcup"))
	foipID, currentServerID, err := nc.FindFailoverIP(findCtx, foip.Spec.IP)
	observability.RecordSpanError(findSpan, err)
	findSpan.End()
	observability.ObserveProviderCall("netcup", "find_failover_ip", time.Since(findStart), err)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("findFailoverIP: %w", err)
	}
	if err := validateProviderFence(foip.Status, strconv.Itoa(currentServerID), ""); err != nil {
		patch := client.MergeFrom(foip.DeepCopy())
		foip.Status.Phase = netcupv1.FailoverPhaseBlocked
		foip.Status.LastError = err.Error()
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionBlocked, metav1.ConditionTrue, "ProviderOwnershipChanged", "Provider ownership changed out of band", metav1.Now())
		r.emitEvent(&foip, corev1.EventTypeWarning, "ProviderOwnershipChanged", "Blocked transition after provider ownership changed")
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
	if foip.Status.TargetNode == "" {
		if owner := nodeForServerID(nodeList.Items, currentServerID); owner != nil {
			foip.Status.TargetNode = owner.Name
			foip.Status.CandidateReason = "current provider owner observed"
		}
	}
	better := betterNode(candidates, foip.Status.TargetNode)
	if better == nil && foip.Status.Phase == netcupv1.FailoverPhaseStabilizing && foip.Status.CandidateSince != nil {
		patch := client.MergeFrom(foip.DeepCopy())
		foip.Status.CandidateRecoveryCount++
		if foip.Status.CandidateRecoveryCount < recoveryThreshold(foip.Spec) {
			if err := r.Status().Patch(ctx, &foip, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
		}
		patch = client.MergeFrom(foip.DeepCopy())
		now := metav1.Now()
		if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhaseSelecting, now); err != nil {
			return ctrl.Result{}, err
		}
		foip.Status.CandidateSince = nil
		foip.Status.CandidateFailureCount = 0
		foip.Status.CandidateRecoveryCount = 0
		foip.Status.CandidateReason = "current owner recovered during stabilization"
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionStabilizing, metav1.ConditionFalse, "CandidateRecovered", "Canceled candidate after current owner recovery", now)
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}

	if better != nil && better.Name != foip.Status.TargetNode {
		if foip.Status.Phase == netcupv1.FailoverPhaseSucceeded || foip.Status.Phase == netcupv1.FailoverPhaseDegraded {
			patch := client.MergeFrom(foip.DeepCopy())
			now := metav1.Now()
			if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhaseSelecting, now); err != nil {
				return ctrl.Result{}, err
			}
			foip.Status.CandidateSince = nil
			if err := r.Status().Patch(ctx, &foip, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
		}
		candidate := *better
		patch := client.MergeFrom(foip.DeepCopy())
		foip.Status.CandidateFailureCount++
		if foip.Status.CandidateFailureCount < failureThreshold(foip.Spec) {
			if err := r.Status().Patch(ctx, &foip, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
		}
		if foip.Status.CandidateSince == nil {
			patch := client.MergeFrom(foip.DeepCopy())
			now := metav1.Now()
			foip.Status.CandidateSince = &now
			foip.Status.CandidateReason = "healthiest candidate selected"
			if foip.Status.Phase == netcupv1.FailoverPhaseSelecting {
				if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhaseStabilizing, now); err != nil {
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
		patch = client.MergeFrom(foip.DeepCopy())
		foip.Status.SourceNode = foip.Status.TargetNode
		foip.Status.CleanupAttempts = 0
		foip.Status.NextCleanupAt = nil
		foip.Status.TargetNode = better.Name
		foip.Status.LocalOwners = nil
		foip.Status.CandidateSince = nil
		foip.Status.CandidateReason = ""
		foip.Status.CandidateFailureCount = 0
		foip.Status.CandidateRecoveryCount = 0
		if foip.Status.Phase == netcupv1.FailoverPhaseStabilizing {
			now := metav1.Now()
			if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhasePreparingTarget, now); err != nil {
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
		if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhaseTargetPrepared, now); err != nil {
			return ctrl.Result{}, err
		}
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionTargetPrepared, metav1.ConditionTrue, "NodeReportedOwnership", "Target node reported local /32 ownership", now)
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseTargetPrepared || foip.Status.Phase == netcupv1.FailoverPhaseRoutingProvider {
		if err := evaluateProbePhase(ctx, r.Client, foip, netcupv1.ProbePhasePreRoute); err != nil {
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
		if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhaseRoutingProvider, now); err != nil {
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
			observability.ObserveCooldownBlock()
			patch := client.MergeFrom(foip.DeepCopy())
			eligible := metav1.NewTime(nextMutation)
			foip.Status.NextEligibleMutationAt = &eligible
			netcupv1.SetCondition(&foip.Status, netcupv1.ConditionCooldown, metav1.ConditionTrue, "ProviderCooldown", "Provider mutation is waiting for the persisted cooldown", metav1.Now())
			if err := r.Status().Patch(ctx, &foip, patch); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Until(nextMutation)}, nil
		}
		handoffStart := time.Now()
		patch := client.MergeFrom(foip.DeepCopy())
		attemptedAt := metav1.Now()
		foip.Status.LastAttemptedProviderMutationAt = &attemptedAt
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionCooldown, metav1.ConditionFalse, "MutationEligible", "Provider mutation cooldown has elapsed", attemptedAt)
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}

		log.Info("Routing failover IP through Netcup", "ip", foip.Spec.IP, "serverID", targetServerID, "node", foip.Status.TargetNode)
		routeStart := time.Now()
		routeCtx, routeSpan := observability.StartSpan(ctx, "foip-operator.provider", "RouteFailoverIP", attribute.String("foip.transition_id", foip.Status.TransitionID), attribute.String("foip.provider", "netcup"))
		if err := nc.RouteFailoverIP(routeCtx, foipID, targetServerID); err != nil {
			observability.RecordSpanError(routeSpan, err)
			routeSpan.End()
			observability.ObserveProviderCall("netcup", "route_failover_ip", time.Since(routeStart), err)
			patch := client.MergeFrom(foip.DeepCopy())
			foip.Status.RetryCount++
			foip.Status.LastError = err.Error()
			if patchErr := r.Status().Patch(ctx, &foip, patch); patchErr != nil {
				return ctrl.Result{}, patchErr
			}
			delay := retryDelay(foip.Spec, foip.Status.RetryCount)
			var providerErr *netcup.ProviderError
			if errors.As(err, &providerErr) && providerErr.RetryAfter > delay {
				delay = providerErr.RetryAfter
			}
			return ctrl.Result{RequeueAfter: delay}, nil
		}
		routeSpan.End()
		observability.ObserveProviderCall("netcup", "route_failover_ip", time.Since(routeStart), nil)

		// Netcup operations are asynchronous. Re-read the authoritative provider
		// state before advancing the persisted provider-verification phase.
		verified := false
		for range providerVerifyAttempts {
			verifyStart := time.Now()
			verifyCtx, verifySpan := observability.StartSpan(ctx, "foip-operator.provider", "VerifyFailoverIP", attribute.String("foip.transition_id", foip.Status.TransitionID), attribute.String("foip.provider", "netcup"))
			_, observedServerID, verifyErr := nc.FindFailoverIP(verifyCtx, foip.Spec.IP)
			observability.RecordSpanError(verifySpan, verifyErr)
			verifySpan.End()
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
		if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhaseVerifyingProvider, now); err != nil {
			return ctrl.Result{}, err
		}
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionProviderConverged, metav1.ConditionTrue, "ProviderOwnerConfirmed", "Provider reports the selected target owner", now)
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
		if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhaseVerifyingTraffic, now); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}
	if err := evaluateProbePhase(ctx, r.Client, foip, netcupv1.ProbePhasePostRoute); err != nil {
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
		if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhaseCommitting, now); err != nil {
			return ctrl.Result{}, err
		}
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionTrafficVerified, metav1.ConditionTrue, "ProbeGatePassed", "Post-route traffic verification passed", now)
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseCommitting {
		patch := client.MergeFrom(foip.DeepCopy())
		now := metav1.Now()
		foip.Status.SourceNode = foip.Status.TargetNode
		if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhaseCleaningStaleOwners, now); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Status().Patch(ctx, &foip, patch); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
	}
	if foip.Status.Phase == netcupv1.FailoverPhaseCleaningStaleOwners {
		observability.ObserveOwnerCount(string(foip.Status.Phase), len(foip.Status.LocalOwners))
		now := time.Now()
		if foip.Status.NextCleanupAt != nil && now.Before(foip.Status.NextCleanupAt.Time) {
			return ctrl.Result{RequeueAfter: time.Until(foip.Status.NextCleanupAt.Time)}, nil
		}
		if len(foip.Status.LocalOwners) != 1 || !containsNode(foip.Status.LocalOwners, foip.Status.TargetNode) {
			patch := client.MergeFrom(foip.DeepCopy())
			foip.Status.CleanupAttempts++
			if foip.Status.CleanupAttempts >= cleanupMaxAttempts(foip.Spec) {
				degradedAt := metav1.Now()
				foip.Status.Phase = netcupv1.FailoverPhaseDegraded
				foip.Status.LastError = "stale local owners did not converge within the cleanup budget"
				netcupv1.SetCondition(&foip.Status, netcupv1.ConditionDegraded, metav1.ConditionTrue, "CleanupTimeout", "Stale local owners did not converge within the cleanup budget", degradedAt)
			} else {
				next := metav1.NewTime(now.Add(cleanupRetryDelay(foip.Spec, foip.Status.CleanupAttempts)))
				foip.Status.NextCleanupAt = &next
				netcupv1.SetCondition(&foip.Status, netcupv1.ConditionOwnershipConverged, metav1.ConditionFalse, "OwnersPending", "Waiting for exactly one local target owner", metav1.Now())
			}
			if err := r.Status().Patch(ctx, &foip, patch); err != nil {
				return ctrl.Result{}, err
			}
			if foip.Status.Phase == netcupv1.FailoverPhaseDegraded {
				return ctrl.Result{RequeueAfter: r.requeueAfter}, nil
			}
			return ctrl.Result{RequeueAfter: preparationPollInterval}, nil
		}
		patch := client.MergeFrom(foip.DeepCopy())
		phaseNow := metav1.Now()
		if err := r.advanceTransition(&foip.Status, netcupv1.FailoverPhaseSucceeded, phaseNow); err != nil {
			return ctrl.Result{}, err
		}
		r.emitEvent(&foip, corev1.EventTypeNormal, "HandoffSucceeded", "Failover transition converged to one local owner")
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionOwnershipConverged, metav1.ConditionTrue, "SingleOwner", "Exactly one local target owner is confirmed", phaseNow)
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionReady, metav1.ConditionTrue, "Succeeded", "Failover transition completed successfully", phaseNow)
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

func (r *FailoverIpReconciler) persistInvalidSpec(ctx context.Context, foip *netcupv1.FailoverIp, cause error) error {
	patch := client.MergeFrom(foip.DeepCopy())
	now := metav1.Now()
	foip.Status.Phase = netcupv1.FailoverPhaseBlocked
	foip.Status.LastError = "invalid failover specification"
	netcupv1.SetCondition(&foip.Status, netcupv1.ConditionBlocked, metav1.ConditionTrue, "InvalidSpec", "Failover specification is invalid", now)
	r.emitEvent(foip, corev1.EventTypeWarning, "InvalidSpec", "Blocked invalid failover specification")
	if err := r.Status().Patch(ctx, foip, patch); err != nil {
		return err
	}
	return cause
}

func (r *FailoverIpReconciler) emitEvent(foip *netcupv1.FailoverIp, eventType, reason, message string) {
	if r.Recorder != nil && r.eventAllowed(foip, eventType, reason, message) {
		r.Recorder.Eventf(foip, nil, eventType, reason, "", "%s", message)
	}
}

func (r *FailoverIpReconciler) advanceTransition(status *netcupv1.FailoverIpStatus, to netcupv1.FailoverPhase, now metav1.Time) error {
	from := status.Phase
	if err := netcupv1.AdvanceTransition(status, to, now); err != nil {
		return err
	}
	observability.ObservePhaseTransition(string(from), string(to))
	return nil
}

func (r *FailoverIpReconciler) eventAllowed(foip *netcupv1.FailoverIp, eventType, reason, message string) bool {
	if r.Events == nil {
		r.Events = observability.NewEventDeduper(time.Minute)
	}
	key := string(foip.UID) + "|" + foip.Namespace + "/" + foip.Name + "|" + foip.Status.TransitionID + "|" + eventType + "|" + reason + "|" + message
	return r.Events.Allow(key, time.Now())
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

func (r *FailoverIpReconciler) secretToFoips(ctx context.Context, obj client.Object) []reconcile.Request {
	var foips netcupv1.FailoverIpList
	if err := r.List(ctx, &foips, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	var probes netcupv1.FailoverProbeList
	if err := r.List(ctx, &probes, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	probeNames := make(map[string]struct{})
	for i := range probes.Items {
		p := &probes.Items[i]
		if (p.Spec.CredentialSecretRef != nil && p.Spec.CredentialSecretRef.Name == obj.GetName()) ||
			(p.Spec.CABundleSecretRef != nil && p.Spec.CABundleSecretRef.Name == obj.GetName()) {
			probeNames[p.Name] = struct{}{}
		}
	}
	requests := make([]reconcile.Request, 0)
	for i := range foips.Items {
		foip := &foips.Items[i]
		match := foip.Spec.SecretName == obj.GetName()
		if !match {
			for _, ref := range foip.Spec.Probes {
				if _, ok := probeNames[ref.Name]; ok {
					match = true
					break
				}
			}
		}
		if match {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: foip.Name, Namespace: foip.Namespace}})
		}
	}
	slices.SortFunc(requests, func(a, b reconcile.Request) int {
		if a.Namespace != b.Namespace {
			return slices.Compare([]string{a.Namespace}, []string{b.Namespace})
		}
		return slices.Compare([]string{a.Name}, []string{b.Name})
	})
	return requests
}

func (r *FailoverIpReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.APIReader = mgr.GetAPIReader()
	r.Recorder = mgr.GetEventRecorder("foip-controller")
	r.Events = observability.NewEventDeduper(time.Minute)
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
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.secretToFoips),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Named("failoverip").
		Complete(r)
}
