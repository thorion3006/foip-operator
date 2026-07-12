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
	corev1 "k8s.io/api/core/v1"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

// nodeScore captures the health issues of a node in priority order.
// Fields are ordered from most to least severe; false means "no issue".
type nodeScore struct {
	networkUnavailable bool
	readyFalse         bool
	readyUnknown       bool
	unschedulable      bool
	pidPressure        bool
	memoryPressure     bool
	diskPressure       bool
	name               string
}

// worseThan returns true if s has a more severe issue than other,
// or the same issues but a later name (tie-break).
func (s nodeScore) worseThan(other nodeScore) bool {
	if s.networkUnavailable != other.networkUnavailable {
		return s.networkUnavailable
	}
	if s.readyFalse != other.readyFalse {
		return s.readyFalse
	}
	if s.readyUnknown != other.readyUnknown {
		return s.readyUnknown
	}
	if s.unschedulable != other.unschedulable {
		return s.unschedulable
	}
	if s.pidPressure != other.pidPressure {
		return s.pidPressure
	}
	if s.memoryPressure != other.memoryPressure {
		return s.memoryPressure
	}
	if s.diskPressure != other.diskPressure {
		return s.diskPressure
	}
	return s.name > other.name
}

// sameIssues returns true if s and other have the same set of issues
// (ignoring the name tie-breaker).
func (s nodeScore) sameIssues(other nodeScore) bool {
	return s.networkUnavailable == other.networkUnavailable &&
		s.readyFalse == other.readyFalse &&
		s.readyUnknown == other.readyUnknown &&
		s.unschedulable == other.unschedulable &&
		s.pidPressure == other.pidPressure &&
		s.memoryPressure == other.memoryPressure &&
		s.diskPressure == other.diskPressure
}

func conditionIs(node corev1.Node, condType corev1.NodeConditionType, status corev1.ConditionStatus) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == condType {
			return c.Status == status
		}
	}
	return false
}

func scoreNode(node corev1.Node) nodeScore {
	return nodeScore{
		networkUnavailable: conditionIs(node, corev1.NodeNetworkUnavailable, corev1.ConditionTrue),
		readyFalse:         conditionIs(node, corev1.NodeReady, corev1.ConditionFalse),
		readyUnknown:       conditionIs(node, corev1.NodeReady, corev1.ConditionUnknown),
		unschedulable:      node.Spec.Unschedulable,
		pidPressure:        conditionIs(node, corev1.NodePIDPressure, corev1.ConditionTrue),
		memoryPressure:     conditionIs(node, corev1.NodeMemoryPressure, corev1.ConditionTrue),
		diskPressure:       conditionIs(node, corev1.NodeDiskPressure, corev1.ConditionTrue),
		name:               node.Name,
	}
}

// candidateNodes filters a node list to those annotated for failover IP use.
func candidateNodes(nodes []corev1.Node) []corev1.Node {
	var out []corev1.Node
	for _, n := range nodes {
		if n.Annotations[netcupv1.MACAnnotation] != "" &&
			n.Annotations[netcupv1.ServerIDAnnotation] != "" {
			out = append(out, n)
		}
	}
	return out
}

// betterNode returns the healthiest candidate node, but only if it is
// strictly healthier than currentName (differs on at least one issue field).
// Returns nil when:
//   - there are no candidates, or
//   - currentName is already as healthy as the best candidate.
func betterNode(nodes []corev1.Node, currentName string) *corev1.Node {
	if len(nodes) == 0 {
		return nil
	}
	best := &nodes[0]
	for i := range nodes[1:] {
		n := &nodes[1:][i]
		if scoreNode(*n).worseThan(scoreNode(*best)) {
			// best is still better
		} else if scoreNode(*best).worseThan(scoreNode(*n)) {
			best = n
		}
		// equal score: worseThan already handles name tie-break
	}

	// No node currently assignment, take best
	if currentName == "" {
		return best
	}

	bestScore := scoreNode(*best)
	for i := range nodes {
		if nodes[i].Name == currentName {
			if bestScore.sameIssues(scoreNode(nodes[i])) {
				// Current node is as healthy as the best; don't trigger an unnecessary switch.
				return nil
			}
			return best
		}
	}
	// currentName is not in the candidate list (deleted / lost annotations)
	return best
}
