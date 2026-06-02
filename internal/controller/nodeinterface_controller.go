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

	"github.com/vishvananda/netlink"
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

	netcupv1 "github.com/niklasbeierl/foip-operator/api/v1"
)

// NodeInterfaceReconciler runs on every node (DaemonSet) and assigns failover IPs
// to the local network interface when desiredNode points to this node.
// Requires NET_ADMIN capability.
type NodeInterfaceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	NodeName string
}

// +kubebuilder:rbac:groups=foip.noshoes.xyz,resources=failoverips,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch

func (r *NodeInterfaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var foip netcupv1.FailoverIp
	if err := r.Get(ctx, req.NamespacedName, &foip); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if foip.Status.DesiredNode != r.NodeName {
		return ctrl.Result{}, nil
	}

	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: r.NodeName}, &node); err != nil {
		return ctrl.Result{}, err
	}

	mac := node.Annotations[netcupv1.MACAnnotation]
	if mac == "" {
		return ctrl.Result{}, fmt.Errorf("node %s missing annotation %s", r.NodeName, netcupv1.MACAnnotation)
	}

	if err := ensureIPAssigned(mac, foip.Spec.IP); err != nil {
		return ctrl.Result{}, fmt.Errorf("assigning %s to interface with MAC %s: %w", foip.Spec.IP, mac, err)
	}
	log.Info("IP assigned to local interface", "ip", foip.Spec.IP, "mac", mac)

	return ctrl.Result{}, nil
}

func ensureIPAssigned(mac, ipStr string) error {
	link, err := findLinkByMAC(mac)
	if err != nil {
		return err
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return fmt.Errorf("invalid IP address: %s", ipStr)
	}
	addr := &netlink.Addr{
		IPNet: &net.IPNet{
			IP:   ip,
			Mask: net.CIDRMask(32, 32),
		},
	}

	existing, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("listing addresses on %s: %w", link.Attrs().Name, err)
	}
	for _, a := range existing {
		if a.IP.Equal(ip) {
			return nil
		}
	}

	return netlink.AddrAdd(link, addr)
}

func findLinkByMAC(mac string) (netlink.Link, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("listing network interfaces: %w", err)
	}
	for _, l := range links {
		if strings.EqualFold(l.Attrs().HardwareAddr.String(), mac) {
			return l, nil
		}
	}
	return nil, fmt.Errorf("no interface found with MAC %s", mac)
}

// localNodeMACChangedPredicate passes Node events only when they concern our node
// and (for updates) only when the MAC annotation actually changed.
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

// nodeToAllFoips enqueues all FailoverIps when this node's MAC annotation changes.
func (r *NodeInterfaceReconciler) nodeToAllFoips(ctx context.Context, _ client.Object) []reconcile.Request {
	var list netcupv1.FailoverIpList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, len(list.Items))
	for i, foip := range list.Items {
		reqs[i] = reconcile.Request{NamespacedName: types.NamespacedName{
			Name:      foip.Name,
			Namespace: foip.Namespace,
		}}
	}
	return reqs
}

func (r *NodeInterfaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Trigger on all FailoverIp changes, including status updates where desiredNode changes.
		For(&netcupv1.FailoverIp{}, builder.WithPredicates(predicate.ResourceVersionChangedPredicate{})).
		// Trigger when this node's MAC annotation changes (e.g. interface swap).
		Watches(
			&corev1.Node{},
			handler.EnqueueRequestsFromMapFunc(r.nodeToAllFoips),
			builder.WithPredicates(localNodeMACChangedPredicate{nodeName: r.NodeName}),
		).
		Named("nodeinterface").
		Complete(r)
}
