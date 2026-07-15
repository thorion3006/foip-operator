package controller

import (
	"context"
	"fmt"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
	"github.com/thorion3006/foip-operator/internal/probe"
	corev1 "k8s.io/api/core/v1"
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
		if foip.Spec.ProbeComposition == "" && resource.Spec.Composition != "" {
			composition = resource.Spec.Composition
			quorum = resource.Spec.Quorum
		}
		if resource.Spec.Type == netcupv1.ProbeTypeKubernetes {
			results = append(results, probe.ExecuteKubernetes(ctx, reader, resource.Spec.Kubernetes))
		} else {
			if resource.Spec.CredentialSecretRef == nil {
				results = append(results, probe.Execute(ctx, resource.Spec))
				continue
			}
			var secret corev1.Secret
			if err := reader.Get(ctx, client.ObjectKey{Name: resource.Spec.CredentialSecretRef.Name, Namespace: foip.Namespace}, &secret); err != nil {
				return fmt.Errorf("loading probe credential Secret: %w", err)
			}
			credential := secret.Data[resource.Spec.CredentialSecretRef.Key]
			if len(credential) == 0 {
				return fmt.Errorf("probe credential Secret key is empty")
			}
			results = append(results, probe.ExecuteWithCredential(ctx, resource.Spec, string(credential)))
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
