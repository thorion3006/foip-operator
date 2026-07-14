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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// MACAnnotation identifies the node's primary MAC address.
	MACAnnotation = "foip.noshoes.xyz/primary-mac"
	// ServerIDAnnotation identifies the netcup server ID for the node.
	ServerIDAnnotation = "foip.noshoes.xyz/server-id"
)

// FailoverPhase is the persisted phase of a failover transition.
type FailoverPhase string

const (
	FailoverPhaseIdle                FailoverPhase = "Idle"
	FailoverPhaseSelecting           FailoverPhase = "Selecting"
	FailoverPhaseStabilizing         FailoverPhase = "Stabilizing"
	FailoverPhasePreparingTarget     FailoverPhase = "PreparingTarget"
	FailoverPhaseTargetPrepared      FailoverPhase = "TargetPrepared"
	FailoverPhaseRoutingProvider     FailoverPhase = "RoutingProvider"
	FailoverPhaseVerifyingProvider   FailoverPhase = "VerifyingProvider"
	FailoverPhaseVerifyingTraffic    FailoverPhase = "VerifyingTraffic"
	FailoverPhaseCommitting          FailoverPhase = "Committing"
	FailoverPhaseCleaningStaleOwners FailoverPhase = "CleaningStaleOwners"
	FailoverPhaseSucceeded           FailoverPhase = "Succeeded"
	FailoverPhaseDegraded            FailoverPhase = "Degraded"
	FailoverPhaseBlocked             FailoverPhase = "Blocked"
)

// FailoverIpSpec defines the desired state of FailoverIp.
type FailoverIpSpec struct {
	// IP is the failover IP address to manage, without a prefix length.
	// +kubebuilder:validation:Required
	IP string `json:"ip"`

	// SecretName names the Secret containing refreshToken and userId.
	// +kubebuilder:validation:Required
	SecretName string `json:"secretName"`
}

// FailoverIpStatus defines the observed state of FailoverIp.
type FailoverIpStatus struct {
	// TransitionID identifies the logical handoff and fences stale reconciles.
	TransitionID string `json:"transitionID,omitempty"`
	// Phase is the durable state-machine phase.
	// +kubebuilder:validation:Enum=Idle;Selecting;Stabilizing;PreparingTarget;TargetPrepared;RoutingProvider;VerifyingProvider;VerifyingTraffic;Committing;CleaningStaleOwners;Succeeded;Degraded;Blocked
	Phase FailoverPhase `json:"phase,omitempty"`
	// SourceNode is the committed owner at transition start.
	SourceNode string `json:"sourceNode,omitempty"`
	// TargetNode is the selected destination for this transition.
	TargetNode string `json:"targetNode,omitempty"`
	// ProviderObservedOwner is the server ID observed from the provider.
	ProviderObservedOwner string `json:"providerObservedOwner,omitempty"`
	// LocalOwners is the set of nodes reporting local /32 ownership.
	LocalOwners []string `json:"localOwners,omitempty"`
	// PhaseStartedAt records when the current phase began.
	PhaseStartedAt *metav1.Time `json:"phaseStartedAt,omitempty"`
	// LastSuccessfulPhase is the last phase completed successfully.
	LastSuccessfulPhase             FailoverPhase `json:"lastSuccessfulPhase,omitempty"`
	RetryCount                      int32         `json:"retryCount,omitempty"`
	LastError                       string        `json:"lastError,omitempty"`
	LastAttemptedProviderMutationAt *metav1.Time  `json:"lastAttemptedProviderMutationAt,omitempty"`
	LastConfirmedProviderMutationAt *metav1.Time  `json:"lastConfirmedProviderMutationAt,omitempty"`
	NextEligibleMutationAt          *metav1.Time  `json:"nextEligibleMutationAt,omitempty"`
	LastTransitionAt                *metav1.Time  `json:"lastTransitionAt,omitempty"`

	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=foip;foips
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=".status.targetNode"
// +kubebuilder:printcolumn:name="Transition",type=string,JSONPath=".status.transitionID"
// FailoverIp is the Schema for the failoverips API.
type FailoverIp struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FailoverIpSpec   `json:"spec"`
	Status FailoverIpStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FailoverIpList contains a list of FailoverIp.
type FailoverIpList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FailoverIp `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FailoverIp{}, &FailoverIpList{})
}
