package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netcupv1 "github.com/thorion3006/foip-operator/api/v1"
)

func TestBetterNodeDeterministicTieBreakingAndCurrentOwnerPreference(t *testing.T) {
	healthy := func(name string) corev1.Node {
		return schedulingNode(name, corev1.ConditionTrue, nil)
	}

	tests := []struct {
		name        string
		nodes       []corev1.Node
		currentName string
		want        string
	}{
		{
			name:        "lexically first equal candidate wins",
			nodes:       []corev1.Node{healthy("node-z"), healthy("node-a")},
			currentName: "",
			want:        "node-a",
		},
		{
			name:        "tie remains deterministic when input order changes",
			nodes:       []corev1.Node{healthy("node-a"), healthy("node-z")},
			currentName: "",
			want:        "node-a",
		},
		{
			name:        "equally healthy current owner is retained",
			nodes:       []corev1.Node{healthy("node-a"), healthy("node-z")},
			currentName: "node-z",
			want:        "",
		},
		{
			name: "strictly healthier candidate wins over current owner",
			nodes: []corev1.Node{
				schedulingNode("current", corev1.ConditionTrue, func(n *corev1.Node) { n.Spec.Unschedulable = true }),
				healthy("candidate"),
			},
			currentName: "current",
			want:        "candidate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := betterNode(tt.nodes, tt.currentName)
			if tt.want == "" {
				if got != nil {
					t.Fatalf("betterNode() = %q, want no switch", got.Name)
				}
				return
			}
			if got == nil {
				t.Fatalf("betterNode() = nil, want %q", tt.want)
			}
			if got.Name != tt.want {
				t.Fatalf("betterNode() = %q, want %q", got.Name, tt.want)
			}
		})
	}
}

func TestNodeAcceptableRequiresReadyAndNoPressureOrCordon(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*corev1.Node)
		want   bool
	}{
		{name: "ready", want: true},
		{name: "unknown readiness", mutate: func(n *corev1.Node) { n.Status.Conditions[0].Status = corev1.ConditionUnknown }},
		{name: "cordoned", mutate: func(n *corev1.Node) { n.Spec.Unschedulable = true }},
		{name: "network unavailable", mutate: func(n *corev1.Node) {
			n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeNetworkUnavailable, Status: corev1.ConditionTrue})
		}},
		{name: "pid pressure", mutate: func(n *corev1.Node) {
			n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: corev1.NodePIDPressure, Status: corev1.ConditionTrue})
		}},
		{name: "memory pressure", mutate: func(n *corev1.Node) {
			n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue})
		}},
		{name: "disk pressure", mutate: func(n *corev1.Node) {
			n.Status.Conditions = append(n.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := schedulingNode("candidate", corev1.ConditionTrue, tt.mutate)
			if got := nodeAcceptable(node); got != tt.want {
				t.Fatalf("nodeAcceptable() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestNodeForServerIDFindsObservedProviderOwner(t *testing.T) {
	nodes := []corev1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "other", Annotations: map[string]string{netcupv1.ServerIDAnnotation: "2"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "owner", Annotations: map[string]string{netcupv1.ServerIDAnnotation: "7"}}},
	}
	owner := nodeForServerID(nodes, 7)
	if owner == nil || owner.Name != "owner" {
		t.Fatalf("owner = %#v, want owner", owner)
	}
	if nodeForServerID(nodes, 99) != nil {
		t.Fatal("unexpected node found for unknown server ID")
	}
}
