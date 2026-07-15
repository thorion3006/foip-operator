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
	"net"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"go.opentelemetry.io/otel/attribute"
	corev1 "k8s.io/api/core/v1"
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
	"github.com/thorion3006/foip-operator/internal/observability"
)

// NodeInterfaceReconciler runs on every node and reports local /32 ownership
// for each persisted failover transition. During a handoff the source and
// target may both retain the address until the controller commits cleanup.
type NodeInterfaceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	NodeName string
}

// +kubebuilder:rbac:groups=foip.noshoes.xyz,resources=failoverips,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=foip.noshoes.xyz,resources=failoverips/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=foip.noshoes.xyz,resources=failoverprobes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

func (r *NodeInterfaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (res ctrl.Result, err error) {
	start := time.Now()
	ctx, span := observability.StartSpan(ctx, "foip-operator.nodeinterface", "Reconcile",
		attribute.String("k8s.namespace", req.Namespace),
		attribute.String("k8s.name", req.Name),
		attribute.String("node.name", r.NodeName),
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
		observability.ObserveReconcile("nodeinterface", result, time.Since(start))
	}()

	log := observability.Logger(ctx, logf.FromContext(ctx))

	var foip netcupv1.FailoverIp
	if err := r.Get(ctx, req.NamespacedName, &foip); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: r.NodeName}, &node); err != nil {
		return ctrl.Result{}, err
	}

	mac := node.Annotations[netcupv1.MACAnnotation]
	if mac == "" {
		return ctrl.Result{}, fmt.Errorf("node %s missing annotation %s", r.NodeName, netcupv1.MACAnnotation)
	}

	shouldOwn := r.NodeName == foip.Status.TargetNode || r.NodeName == foip.Status.SourceNode
	if shouldOwn {
		assignStart := time.Now()
		if err := ensureIPAssigned(mac, foip.Spec.IP); err != nil {
			observability.ObserveInterfaceOperation("nodeinterface", "assign", time.Since(assignStart), err)
			return ctrl.Result{}, fmt.Errorf("assigning %s to interface with MAC %s: %w", foip.Spec.IP, mac, err)
		}
		observability.ObserveInterfaceOperation("nodeinterface", "assign", time.Since(assignStart), nil)
		log.Info("failover IP present on local interface", "ip", foip.Spec.IP, "mac", mac)

		if !containsNode(foip.Status.LocalOwners, r.NodeName) {
			patch := client.MergeFrom(foip.DeepCopy())
			foip.Status.LocalOwners = append(foip.Status.LocalOwners, r.NodeName)
			if err := r.Status().Patch(ctx, &foip, patch); err != nil {
				return ctrl.Result{}, fmt.Errorf("marking node %s prepared: %w", r.NodeName, err)
			}
		}
		return ctrl.Result{}, nil
	}

	removeStart := time.Now()
	removed, err := ensureIPRemoved(mac, foip.Spec.IP)
	if err != nil {
		observability.ObserveInterfaceOperation("nodeinterface", "remove", time.Since(removeStart), err)
		return ctrl.Result{}, fmt.Errorf("removing stale %s from interface with MAC %s: %w", foip.Spec.IP, mac, err)
	}
	observability.ObserveInterfaceOperation("nodeinterface", "remove", time.Since(removeStart), nil)
	if removed {
		log.Info("removed stale failover IP from local interface", "ip", foip.Spec.IP, "mac", mac)
		if containsNode(foip.Status.LocalOwners, r.NodeName) {
			patch := client.MergeFrom(foip.DeepCopy())
			foip.Status.LocalOwners = removeNode(foip.Status.LocalOwners, r.NodeName)
			if err := r.Status().Patch(ctx, &foip, patch); err != nil {
				return ctrl.Result{}, fmt.Errorf("reporting stale owner removal: %w", err)
			}
		}
	}

	return ctrl.Result{}, nil
}

func removeNode(nodes []string, name string) []string {
	filtered := nodes[:0]
	for _, node := range nodes {
		if node != name {
			filtered = append(filtered, node)
		}
	}
	return filtered
}

func ensureIPAssigned(mac, ipStr string) error {
	link, addr, err := linkAndAddress(mac, ipStr)
	if err != nil {
		return err
	}

	existing, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("listing addresses on %s: %w", link.Attrs().Name, err)
	}
	for _, current := range existing {
		if current.IP.Equal(addr.IP) {
			return nil
		}
	}

	return netlink.AddrAdd(link, addr)
}

func ensureIPRemoved(mac, ipStr string) (bool, error) {
	link, addr, err := linkAndAddress(mac, ipStr)
	if err != nil {
		return false, err
	}

	existing, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return false, fmt.Errorf("listing addresses on %s: %w", link.Attrs().Name, err)
	}
	for _, current := range existing {
		if current.IP.Equal(addr.IP) {
			if err := netlink.AddrDel(link, &current); err != nil {
				return false, err
			}
			return true, nil
		}
	}

	return false, nil
}

func linkAndAddress(mac, ipStr string) (netlink.Link, *netlink.Addr, error) {
	link, err := findLinkByMAC(mac)
	if err != nil {
		return nil, nil, err
	}

	ip := net.ParseIP(ipStr)
	if ip == nil || ip.To4() == nil {
		return nil, nil, fmt.Errorf("invalid IPv4 address: %s", ipStr)
	}

	return link, &netlink.Addr{IPNet: &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)}}, nil
}

func findLinkByMAC(mac string) (netlink.Link, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("listing network interfaces: %w", err)
	}
	for _, link := range links {
		if strings.EqualFold(link.Attrs().HardwareAddr.String(), mac) {
			return link, nil
		}
	}
	return nil, fmt.Errorf("no interface found with MAC %s", mac)
}

type localNodeMACChangedPredicate struct {
	predicate.Funcs
	nodeName string
}

func (p localNodeMACChangedPredicate) Create(e event.CreateEvent) bool {
	return e.Object.GetName() == p.nodeName
}
func (p localNodeMACChangedPredicate) Delete(e event.DeleteEvent) bool {
	return e.Object.GetName() == p.nodeName
}
func (p localNodeMACChangedPredicate) Update(e event.UpdateEvent) bool {
	if e.ObjectNew.GetName() != p.nodeName {
		return false
	}
	oldNode, ok1 := e.ObjectOld.(*corev1.Node)
	newNode, ok2 := e.ObjectNew.(*corev1.Node)
	if !ok1 || !ok2 {
		return true
	}
	return oldNode.Annotations[netcupv1.MACAnnotation] != newNode.Annotations[netcupv1.MACAnnotation]
}

func (r *NodeInterfaceReconciler) nodeToAllFoips(ctx context.Context, _ client.Object) []reconcile.Request {
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

func (r *NodeInterfaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&netcupv1.FailoverIp{}, builder.WithPredicates(predicate.ResourceVersionChangedPredicate{})).
		Watches(
			&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(r.nodeToAllFoips),
			builder.WithPredicates(localNodeMACChangedPredicate{nodeName: r.NodeName}),
		).
		Named("nodeinterface").
		Complete(r)
}
