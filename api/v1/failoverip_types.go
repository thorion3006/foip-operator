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
	// AssignedNode is the node confirmed as the provider-routed owner.
	AssignedNode string `json:"assignedNode,omitempty"`

	// DesiredNode is the target selected for the next or current ownership state.
	DesiredNode string `json:"desiredNode,omitempty"`

	// PreparedNode is the desired node that confirmed the /32 is present locally.
	PreparedNode string `json:"preparedNode,omitempty"`

	// LastSyncAttempt is the RFC3339 timestamp of the last provider route attempt.
	LastSyncAttempt string `json:"lastSyncAttempt,omitempty"`

	// LastSyncSuccess is the RFC3339 timestamp of the last verified route handoff.
	LastSyncSuccess string `json:"lastSyncSuccess,omitempty"`

	// +kubebuilder:validation:Optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=foip;foips
// +kubebuilder:printcolumn:name="Desired",type=string,JSONPath=".status.desiredNode"
// +kubebuilder:printcolumn:name="Assigned",type=string,JSONPath=".status.assignedNode"
// +kubebuilder:printcolumn:name="Prepared",type=string,JSONPath=".status.preparedNode"
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
