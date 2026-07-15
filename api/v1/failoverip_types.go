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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// MACAnnotation identifies the node's primary MAC address.
	MACAnnotation = "foip.noshoes.xyz/primary-mac"
	// ServerIDAnnotation identifies the netcup server ID for the node.
	ServerIDAnnotation = "foip.noshoes.xyz/server-id"
	// ManualReconcileAnnotation is a user-controlled token for retrying a
	// blocked or degraded transition after the cause has been corrected.
	ManualReconcileAnnotation = "foip.noshoes.xyz/reconcile"
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

// RecoveryPolicy defines the action after provider routing succeeds but
// post-route traffic verification fails.
type RecoveryPolicy string

const (
	RecoveryPolicyHoldDualOwnership  RecoveryPolicy = "HoldDualOwnership"
	RecoveryPolicyRollbackProvider   RecoveryPolicy = "RollbackProvider"
	RecoveryPolicyCommitDegraded     RecoveryPolicy = "CommitDegraded"
	RecoveryPolicyManualIntervention RecoveryPolicy = "ManualIntervention"
)

// FailoverIpSpec defines the desired state of FailoverIp.
type FailoverIpSpec struct {
	// IP is the failover IP address to manage, without a prefix length.
	// +kubebuilder:validation:Required
	IP string `json:"ip"`

	// SecretName names the Secret containing refreshToken and userId.
	// +kubebuilder:validation:Required
	SecretName string `json:"secretName"`

	// ProviderCooldownSeconds is the minimum interval between route mutations.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=60
	ProviderCooldownSeconds int32 `json:"providerCooldownSeconds,omitempty"`
	// RetryBaseSeconds and RetryMaxSeconds bound transient retry delays.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	RetryBaseSeconds int32 `json:"retryBaseSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=60
	RetryMaxSeconds int32 `json:"retryMaxSeconds,omitempty"`
	// FailureThreshold and RecoveryThreshold prevent transient health changes
	// from starting or immediately reversing a handoff.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=3
	FailureThreshold int32 `json:"failureThreshold,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=2
	RecoveryThreshold int32 `json:"recoveryThreshold,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=30
	StabilizationSeconds int32 `json:"stabilizationSeconds,omitempty"`
	// +kubebuilder:validation:Minimum=0
	MinHealthySeconds  int32 `json:"minHealthySeconds,omitempty"`
	PreferCurrentOwner bool  `json:"preferCurrentOwner,omitempty"`
	// +kubebuilder:validation:Enum=HoldDualOwnership;RollbackProvider;CommitDegraded;ManualIntervention
	// +kubebuilder:default=HoldDualOwnership
	RecoveryPolicy RecoveryPolicy `json:"recoveryPolicy,omitempty"`
	// +kubebuilder:validation:Enum=All;Any;Quorum
	ProbeComposition ProbeComposition `json:"probeComposition,omitempty"`
	// +kubebuilder:validation:Minimum=1
	ProbeQuorum int32 `json:"probeQuorum,omitempty"`

	// Probes is optional; an empty list enables node-health-only operation.
	// +kubebuilder:validation:MaxItems=32
	Probes []corev1.LocalObjectReference `json:"probes,omitempty"`
}

// ProbePhase controls when a probe participates in a transition.
type ProbePhase string

const (
	ProbePhasePreRoute   ProbePhase = "PreRoute"
	ProbePhasePostRoute  ProbePhase = "PostRoute"
	ProbePhaseContinuous ProbePhase = "Continuous"
)

// ProbeComposition determines how a set of probe results is aggregated.
type ProbeComposition string

const (
	ProbeCompositionAll    ProbeComposition = "All"
	ProbeCompositionAny    ProbeComposition = "Any"
	ProbeCompositionQuorum ProbeComposition = "Quorum"
)

// ProbeType identifies a provider-neutral executor.
type ProbeType string

const (
	ProbeTypeTCP        ProbeType = "TCP"
	ProbeTypeTLS        ProbeType = "TLS"
	ProbeTypeHTTP       ProbeType = "HTTP"
	ProbeTypeHTTPS      ProbeType = "HTTPS"
	ProbeTypeKubernetes ProbeType = "Kubernetes"
)

// ProbeTarget describes the endpoint independently of any edge product.
type ProbeTarget struct {
	// Address may contain ${targetNodeIP} or ${failoverIP}, or be an explicit DNS name.
	Address string `json:"address,omitempty"`
	Port    int32  `json:"port,omitempty"`
	Path    string `json:"path,omitempty"`
	Host    string `json:"host,omitempty"`
	SNI     string `json:"sni,omitempty"`
}

// ProbeNetworkPolicy controls which resolved destinations a probe may reach.
type ProbeNetworkPolicy struct {
	AllowPrivateNetworks bool     `json:"allowPrivateNetworks,omitempty"`
	AllowedCIDRs         []string `json:"allowedCIDRs,omitempty"`
	DeniedCIDRs          []string `json:"deniedCIDRs,omitempty"`
}

type ProbeHeader struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// KubernetesReadinessTarget describes a Kubernetes object readiness check.
type KubernetesReadinessTarget struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
	JSONPath   string `json:"jsonPath,omitempty"`
	Expected   string `json:"expected,omitempty"`
}

// FailoverProbeSpec defines a reusable, composable health check.
type FailoverProbeSpec struct {
	// +kubebuilder:validation:Enum=PreRoute;PostRoute;Continuous
	Phase ProbePhase `json:"phase"`
	// +kubebuilder:validation:Enum=All;Any;Quorum
	Composition ProbeComposition `json:"composition,omitempty"`
	// Quorum is required when composition is Quorum.
	// +kubebuilder:validation:Minimum=1
	Quorum int32 `json:"quorum,omitempty"`
	// +kubebuilder:validation:Enum=TCP;TLS;HTTP;HTTPS;Kubernetes
	Type                ProbeType                  `json:"type"`
	Target              ProbeTarget                `json:"target,omitempty"`
	NetworkPolicy       ProbeNetworkPolicy         `json:"networkPolicy,omitempty"`
	Kubernetes          *KubernetesReadinessTarget `json:"kubernetes,omitempty"`
	TimeoutSeconds      int32                      `json:"timeoutSeconds,omitempty"`
	IntervalSeconds     int32                      `json:"intervalSeconds,omitempty"`
	SuccessThreshold    int32                      `json:"successThreshold,omitempty"`
	FailureThreshold    int32                      `json:"failureThreshold,omitempty"`
	InitialDelaySeconds int32                      `json:"initialDelaySeconds,omitempty"`
	FollowRedirects     bool                       `json:"followRedirects,omitempty"`
	InsecureSkipVerify  bool                       `json:"insecureSkipVerify,omitempty"`
	CredentialSecretRef *corev1.SecretKeySelector  `json:"credentialSecretRef,omitempty"`
	CredentialHeader    string                     `json:"credentialHeader,omitempty"`
	CABundleSecretRef   *corev1.SecretKeySelector  `json:"caBundleSecretRef,omitempty"`
	Method              string                     `json:"method,omitempty"`
	ExpectedStatusMin   int32                      `json:"expectedStatusMin,omitempty"`
	ExpectedStatusMax   int32                      `json:"expectedStatusMax,omitempty"`
	BodyMatch           string                     `json:"bodyMatch,omitempty"`
	Headers             []ProbeHeader              `json:"headers,omitempty"`
}

// ProbeObservation contains only non-sensitive result metadata.
type ProbeObservation struct {
	Name                 string      `json:"name"`
	Success              bool        `json:"success"`
	Reason               string      `json:"reason,omitempty"`
	ObservedAt           metav1.Time `json:"observedAt"`
	ConsecutiveSuccesses int32       `json:"consecutiveSuccesses,omitempty"`
	ConsecutiveFailures  int32       `json:"consecutiveFailures,omitempty"`
}

type FailoverProbeStatus struct {
	Conditions   []metav1.Condition `json:"conditions,omitempty"`
	Observations []ProbeObservation `json:"observations,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fprobe;fprobes
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".spec.type"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".spec.phase"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
type FailoverProbe struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              FailoverProbeSpec   `json:"spec"`
	Status            FailoverProbeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type FailoverProbeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FailoverProbe `json:"items"`
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
	LastSuccessfulPhase             FailoverPhase  `json:"lastSuccessfulPhase,omitempty"`
	RetryCount                      int32          `json:"retryCount,omitempty"`
	LastError                       string         `json:"lastError,omitempty"`
	LastAttemptedProviderMutationAt *metav1.Time   `json:"lastAttemptedProviderMutationAt,omitempty"`
	LastConfirmedProviderMutationAt *metav1.Time   `json:"lastConfirmedProviderMutationAt,omitempty"`
	NextEligibleMutationAt          *metav1.Time   `json:"nextEligibleMutationAt,omitempty"`
	LastTransitionAt                *metav1.Time   `json:"lastTransitionAt,omitempty"`
	CandidateSince                  *metav1.Time   `json:"candidateSince,omitempty"`
	CandidateReason                 string         `json:"candidateReason,omitempty"`
	CandidateFailureCount           int32          `json:"candidateFailureCount,omitempty"`
	CandidateRecoveryCount          int32          `json:"candidateRecoveryCount,omitempty"`
	RecoveryAction                  RecoveryPolicy `json:"recoveryAction,omitempty"`
	RecoveryAttempts                int32          `json:"recoveryAttempts,omitempty"`
	ManualReconcileToken            string         `json:"manualReconcileToken,omitempty"`

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
	SchemeBuilder.Register(&FailoverIp{}, &FailoverIpList{}, &FailoverProbe{}, &FailoverProbeList{})
}
