package controller

import (
	"context"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
	"github.com/thorion3006/foip-operator/internal/observability"
)

// recoverPostRouteFailure applies the configured policy. The boolean result
// means the caller may continue to commit ownership in degraded mode.
func (r *FailoverIpReconciler) recoverPostRouteFailure(ctx context.Context, foip *netcupv1.FailoverIp, nc failoverIPClient, foipID int) (bool, ctrl.Result, error) {
	policy := foip.Spec.RecoveryPolicy
	if policy == "" {
		policy = netcupv1.RecoveryPolicyHoldDualOwnership
	}
	observability.ObserveRecoveryAction(string(policy))
	r.emitEvent(foip, corev1.EventTypeWarning, "RecoveryAction", "Applying configured post-route recovery policy")
	foip.Status.RecoveryAction = policy
	foip.Status.RecoveryAttempts++
	now := metav1.Now()
	conditionMessage := "Post-route traffic verification failed"

	switch policy {
	case netcupv1.RecoveryPolicyCommitDegraded:
		patch := client.MergeFrom(foip.DeepCopy())
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionDegraded, metav1.ConditionTrue, "CommitDegraded", conditionMessage, now)
		if err := r.Status().Patch(ctx, foip, patch); err != nil {
			return false, ctrl.Result{}, err
		}
		return true, ctrl.Result{}, nil

	case netcupv1.RecoveryPolicyManualIntervention:
		patch := client.MergeFrom(foip.DeepCopy())
		foip.Status.Phase = netcupv1.FailoverPhaseBlocked
		foip.Status.LastError = "post-route probe failed; manual intervention required"
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionBlocked, metav1.ConditionTrue, "ManualIntervention", conditionMessage, now)
		if err := r.Status().Patch(ctx, foip, patch); err != nil {
			return false, ctrl.Result{}, err
		}
		return false, ctrl.Result{}, nil

	case netcupv1.RecoveryPolicyRollbackProvider:
		if foip.Status.SourceNode == "" {
			return r.persistRecoveryState(ctx, foip, netcupv1.FailoverPhaseBlocked, "RollbackProvider has no source node", "RollbackUnavailable", now)
		}
		var sourceNode corev1.Node
		if err := r.Get(ctx, client.ObjectKey{Name: foip.Status.SourceNode}, &sourceNode); err != nil {
			return r.persistRecoveryState(ctx, foip, netcupv1.FailoverPhaseBlocked, "RollbackProvider source node is unavailable", "RollbackUnavailable", now)
		}
		sourceServerID, err := strconv.Atoi(sourceNode.Annotations[netcupv1.ServerIDAnnotation])
		if err != nil {
			return r.persistRecoveryState(ctx, foip, netcupv1.FailoverPhaseBlocked, "RollbackProvider source annotation is invalid", "RollbackUnavailable", now)
		}
		next, allowed := providerMutationGate(foip.Status, providerCooldown(foip.Spec), time.Now())
		if !allowed {
			patch := client.MergeFrom(foip.DeepCopy())
			eligible := metav1.NewTime(next)
			foip.Status.NextEligibleMutationAt = &eligible
			foip.Status.LastError = "rollback deferred until provider cooldown expires"
			netcupv1.SetCondition(&foip.Status, netcupv1.ConditionDegraded, metav1.ConditionTrue, "RollbackDeferred", foip.Status.LastError, now)
			if err := r.Status().Patch(ctx, foip, patch); err != nil {
				return false, ctrl.Result{}, err
			}
			return false, ctrl.Result{RequeueAfter: time.Until(next)}, nil
		}
		patch := client.MergeFrom(foip.DeepCopy())
		foip.Status.LastAttemptedProviderMutationAt = &now
		if err := r.Status().Patch(ctx, foip, patch); err != nil {
			return false, ctrl.Result{}, err
		}
		if err := nc.RouteFailoverIP(ctx, foipID, sourceServerID); err != nil {
			return r.persistRecoveryState(ctx, foip, netcupv1.FailoverPhaseBlocked, "provider rollback failed", "RollbackFailed", now)
		}
		return r.persistRecoveryState(ctx, foip, netcupv1.FailoverPhaseDegraded, "provider rolled back; traffic still requires intervention", "RollbackSucceeded", now)

	case netcupv1.RecoveryPolicyHoldDualOwnership:
		fallthrough
	default:
		return r.persistRecoveryState(ctx, foip, netcupv1.FailoverPhaseDegraded, conditionMessage, "HoldDualOwnership", now)
	}
}

func (r *FailoverIpReconciler) persistRecoveryState(ctx context.Context, foip *netcupv1.FailoverIp, phase netcupv1.FailoverPhase, message, reason string, now metav1.Time) (bool, ctrl.Result, error) {
	patch := client.MergeFrom(foip.DeepCopy())
	foip.Status.Phase = phase
	foip.Status.LastError = message
	if phase == netcupv1.FailoverPhaseBlocked {
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionBlocked, metav1.ConditionTrue, reason, message, now)
	} else {
		netcupv1.SetCondition(&foip.Status, netcupv1.ConditionDegraded, metav1.ConditionTrue, reason, message, now)
	}
	if err := r.Status().Patch(ctx, foip, patch); err != nil {
		return false, ctrl.Result{}, err
	}
	return false, ctrl.Result{RequeueAfter: time.Minute}, nil
}
