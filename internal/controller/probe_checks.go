package controller

import (
	"context"
	"fmt"
	"strings"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
	"github.com/thorion3006/foip-operator/internal/probe"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func evaluateProbePhase(ctx context.Context, reader client.Reader, foip netcupv1.FailoverIp, phase netcupv1.ProbePhase) error {
	if len(foip.Spec.Probes) == 0 {
		return nil
	}
	results := make([]probe.Result, 0, len(foip.Spec.Probes))
	composition := foip.Spec.ProbeComposition
	if composition == "" {
		composition = netcupv1.ProbeCompositionAll
	}
	quorum := foip.Spec.ProbeQuorum
	for _, ref := range foip.Spec.Probes {
		var resource netcupv1.FailoverProbe
		if err := reader.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: foip.Namespace}, &resource); err != nil {
			return fmt.Errorf("loading probe %s: %w", ref.Name, err)
		}
		if resource.Spec.Phase != phase && resource.Spec.Phase != netcupv1.ProbePhaseContinuous {
			continue
		}
		resolvedSpec, err := resolveProbeSpec(ctx, reader, foip, resource.Spec)
		if err != nil {
			return fmt.Errorf("resolving probe %s target: %w", ref.Name, err)
		}
		if foip.Spec.ProbeComposition == "" && resource.Spec.Composition != "" {
			composition = resource.Spec.Composition
			quorum = resource.Spec.Quorum
		}
		var result probe.Result
		if resolvedSpec.Type == netcupv1.ProbeTypeKubernetes {
			result = probe.ExecuteKubernetes(ctx, reader, resolvedSpec.Kubernetes)
		} else if resolvedSpec.CredentialSecretRef == nil {
			result = probe.Execute(ctx, resolvedSpec)
		} else {
			var secret corev1.Secret
			if err := reader.Get(ctx, client.ObjectKey{Name: resource.Spec.CredentialSecretRef.Name, Namespace: foip.Namespace}, &secret); err != nil {
				return fmt.Errorf("loading probe credential Secret: %w", err)
			}
			credential := secret.Data[resource.Spec.CredentialSecretRef.Key]
			if len(credential) == 0 {
				return fmt.Errorf("probe credential Secret key is empty")
			}
			result = probe.ExecuteWithCredential(ctx, resolvedSpec, string(credential))
		}
		result, successCount, failureCount := applyProbeThresholds(resource.Spec, resource.Status, resource.Name, result)
		results = append(results, result)
		if writer, ok := reader.(client.Client); ok {
			if err := persistProbeObservation(ctx, writer, &resource, result, successCount, failureCount); err != nil {
				return err
			}
		}
	}
	if len(results) == 0 {
		return nil
	}
	result := probe.Aggregate(composition, quorum, results)
	if !result.Success {
		return fmt.Errorf("%s probe gate failed: %s", phase, result.Reason)
	}
	return nil
}

func applyProbeThresholds(spec netcupv1.FailoverProbeSpec, status netcupv1.FailoverProbeStatus, name string, result probe.Result) (probe.Result, int32, int32) {
	var previous netcupv1.ProbeObservation
	for _, observation := range status.Observations {
		if observation.Name == name {
			previous = observation
			break
		}
	}
	successThreshold := max(spec.SuccessThreshold, 1)
	failureThreshold := max(spec.FailureThreshold, 1)
	if result.Success {
		previous.ConsecutiveSuccesses++
		previous.ConsecutiveFailures = 0
		if previous.ConsecutiveSuccesses < successThreshold {
			return probe.Result{Reason: "success threshold not reached"}, previous.ConsecutiveSuccesses, previous.ConsecutiveFailures
		}
		return result, previous.ConsecutiveSuccesses, previous.ConsecutiveFailures
	}
	previous.ConsecutiveFailures++
	previous.ConsecutiveSuccesses = 0
	if previous.ConsecutiveFailures < failureThreshold && previous.Success {
		return probe.Result{Success: true, Reason: "failure threshold not reached"}, previous.ConsecutiveSuccesses, previous.ConsecutiveFailures
	}
	return result, previous.ConsecutiveSuccesses, previous.ConsecutiveFailures
}

func resolveProbeSpec(ctx context.Context, reader client.Reader, foip netcupv1.FailoverIp, spec netcupv1.FailoverProbeSpec) (netcupv1.FailoverProbeSpec, error) {
	resolved := spec
	resolved.Target.Address = strings.ReplaceAll(resolved.Target.Address, "${failoverIP}", foip.Spec.IP)
	resolved.Target.Host = strings.ReplaceAll(resolved.Target.Host, "${failoverIP}", foip.Spec.IP)
	resolved.Target.SNI = strings.ReplaceAll(resolved.Target.SNI, "${failoverIP}", foip.Spec.IP)
	if strings.Contains(resolved.Target.Address, "${targetNodeIP}") || strings.Contains(resolved.Target.Host, "${targetNodeIP}") || strings.Contains(resolved.Target.SNI, "${targetNodeIP}") {
		if foip.Status.TargetNode == "" {
			return netcupv1.FailoverProbeSpec{}, fmt.Errorf("target node is not selected")
		}
		var node corev1.Node
		if err := reader.Get(ctx, client.ObjectKey{Name: foip.Status.TargetNode}, &node); err != nil {
			return netcupv1.FailoverProbeSpec{}, fmt.Errorf("loading target node %s: %w", foip.Status.TargetNode, err)
		}
		nodeIP := ""
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP || address.Type == corev1.NodeExternalIP {
				nodeIP = address.Address
				if address.Type == corev1.NodeInternalIP {
					break
				}
			}
		}
		if nodeIP == "" {
			return netcupv1.FailoverProbeSpec{}, fmt.Errorf("target node %s has no usable address", foip.Status.TargetNode)
		}
		resolved.Target.Address = strings.ReplaceAll(resolved.Target.Address, "${targetNodeIP}", nodeIP)
		resolved.Target.Host = strings.ReplaceAll(resolved.Target.Host, "${targetNodeIP}", nodeIP)
		resolved.Target.SNI = strings.ReplaceAll(resolved.Target.SNI, "${targetNodeIP}", nodeIP)
	}
	for _, value := range []string{resolved.Target.Address, resolved.Target.Host, resolved.Target.SNI} {
		if strings.Contains(value, "${") {
			return netcupv1.FailoverProbeSpec{}, fmt.Errorf("unsupported target placeholder")
		}
	}
	return resolved, nil
}

func persistProbeObservation(ctx context.Context, writer client.Client, resource *netcupv1.FailoverProbe, result probe.Result, successCount, failureCount int32) error {
	patch := client.MergeFrom(resource.DeepCopy())
	now := metav1.Now()
	observation := netcupv1.ProbeObservation{Name: resource.Name, Success: result.Success, Reason: result.Reason, ObservedAt: now, ConsecutiveSuccesses: successCount, ConsecutiveFailures: failureCount}
	found := false
	for i := range resource.Status.Observations {
		if resource.Status.Observations[i].Name == resource.Name {
			resource.Status.Observations[i] = observation
			found = true
			break
		}
	}
	if !found {
		resource.Status.Observations = append(resource.Status.Observations, observation)
	}
	conditionStatus := metav1.ConditionTrue
	reason := "ProbeSucceeded"
	message := "Probe execution succeeded"
	if !result.Success {
		conditionStatus = metav1.ConditionFalse
		reason = "ProbeFailed"
		message = "Probe execution failed"
	}
	setProbeCondition(&resource.Status, conditionStatus, reason, message, now)
	return writer.Status().Patch(ctx, resource, patch)
}

func setProbeCondition(status *netcupv1.FailoverProbeStatus, conditionStatus metav1.ConditionStatus, reason, message string, now metav1.Time) {
	condition := metav1.Condition{Type: "Ready", Status: conditionStatus, Reason: reason, Message: message, LastTransitionTime: now}
	for i := range status.Conditions {
		if status.Conditions[i].Type == "Ready" {
			status.Conditions[i] = condition
			return
		}
	}
	status.Conditions = append(status.Conditions, condition)
}
