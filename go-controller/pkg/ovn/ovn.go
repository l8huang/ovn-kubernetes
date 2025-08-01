package ovn

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	listers "k8s.io/client-go/listers/core/v1"
	ref "k8s.io/client-go/tools/reference"
	"k8s.io/klog/v2"
	v1pod "k8s.io/kubernetes/pkg/api/v1/pod"

	libovsdbclient "github.com/ovn-kubernetes/libovsdb/client"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kubevirt"
	libovsdbutil "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/util"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/metrics"
	addressset "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/address_set"
	anpcontroller "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/controller/admin_network_policy"
	egresssvc_zone "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/controller/egressservice"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

const egressFirewallDNSDefaultDuration = 30 * time.Minute

const (
	// TCP is the constant string for the string "TCP"
	TCP = "TCP"

	// UDP is the constant string for the string "UDP"
	UDP = "UDP"

	// SCTP is the constant string for the string "SCTP"
	SCTP = "SCTP"
)

// getPodNamespacedName returns <namespace>_<podname> for the provided pod
func getPodNamespacedName(pod *corev1.Pod) string {
	return util.GetLogicalPortName(pod.Namespace, pod.Name)
}

// syncPeriodic adds a goroutine that periodically does some work
// right now there is only one ticker registered
// for syncNodesPeriodic which deletes chassis records from the sbdb
// every 5 minutes
func (oc *DefaultNetworkController) syncPeriodic() {
	go func() {
		nodeSyncTicker := time.NewTicker(5 * time.Minute)
		defer nodeSyncTicker.Stop()
		for {
			select {
			case <-nodeSyncTicker.C:
				oc.syncNodesPeriodic()
			case <-oc.stopChan:
				return
			}
		}
	}()
}

func (oc *DefaultNetworkController) getPortInfo(pod *corev1.Pod) *lpInfo {
	var portInfo *lpInfo
	key := util.GetLogicalPortName(pod.Namespace, pod.Name)
	if util.PodWantsHostNetwork(pod) {
		// create dummy logicalPortInfo for host-networked pods
		mac, _ := net.ParseMAC("00:00:00:00:00:00")
		portInfo = &lpInfo{
			logicalSwitch: "host-networked",
			name:          key,
			uuid:          "host-networked",
			ips:           []*net.IPNet{},
			mac:           mac,
		}
	} else {
		portInfo, _ = oc.logicalPortCache.get(pod, ovntypes.DefaultNetworkName)
	}
	return portInfo
}

func (oc *DefaultNetworkController) recordPodEvent(reason string, addErr error, pod *corev1.Pod) {
	podRef, err := ref.GetReference(scheme.Scheme, pod)
	if err != nil {
		klog.Errorf("Couldn't get a reference to pod %s/%s to post an event: '%v'",
			pod.Namespace, pod.Name, err)
	} else {
		klog.V(5).Infof("Posting a %s event for Pod %s/%s", corev1.EventTypeWarning, pod.Namespace, pod.Name)
		oc.recorder.Eventf(podRef, corev1.EventTypeWarning, reason, addErr.Error())
	}
}

func (oc *DefaultNetworkController) recordNodeEvent(reason string, addErr error, node *corev1.Node) {
	nodeRef, err := ref.GetReference(scheme.Scheme, node)
	if err != nil {
		klog.Errorf("Couldn't get a reference to node %s to post an event: '%v'", node.Name, err)
	} else {
		klog.V(5).Infof("Posting a %s event for node %s", corev1.EventTypeWarning, node.Name)
		oc.recorder.Eventf(nodeRef, corev1.EventTypeWarning, reason, addErr.Error())
	}
}

func exGatewayAnnotationsChanged(oldPod, newPod *corev1.Pod) bool {
	return oldPod.Annotations[util.RoutingNamespaceAnnotation] != newPod.Annotations[util.RoutingNamespaceAnnotation] ||
		oldPod.Annotations[util.RoutingNetworkAnnotation] != newPod.Annotations[util.RoutingNetworkAnnotation] ||
		oldPod.Annotations[util.BfdAnnotation] != newPod.Annotations[util.BfdAnnotation]
}

func networkStatusAnnotationsChanged(oldPod, newPod *corev1.Pod) bool {
	return oldPod.Annotations[nettypes.NetworkStatusAnnot] != newPod.Annotations[nettypes.NetworkStatusAnnot]
}

func podBecameReady(oldPod, newPod *corev1.Pod) bool {
	return !v1pod.IsPodReadyConditionTrue(oldPod.Status) && v1pod.IsPodReadyConditionTrue(newPod.Status)
}

// ensurePod tries to set up a pod. It returns nil on success and error on failure; failure
// indicates the pod set up should be retried later.
func (oc *DefaultNetworkController) ensurePod(oldPod, pod *corev1.Pod, addPort bool) error {
	// Try unscheduled pods later
	if !util.PodScheduled(pod) {
		return nil
	}

	// Add podIPs on no host subnet Nodes to the namespace address_set
	switchName := pod.Spec.NodeName
	if oc.lsManager.IsNonHostSubnetSwitch(switchName) {
		return oc.ensureRemotePodIP(oldPod, pod, addPort)
	}

	// If an external gateway pod is in terminating or not ready state then remove the
	// routes for the external gateway pod
	if util.PodTerminating(pod) || !v1pod.IsPodReadyConditionTrue(pod.Status) {
		if err := oc.deletePodExternalGW(pod); err != nil {
			return fmt.Errorf("ensurePod failed %s/%s: %w", pod.Namespace, pod.Name, err)
		}
	}

	if oc.isPodScheduledinLocalZone(pod) {
		klog.V(5).Infof("Ensuring zone local for Pod %s/%s in node %s", pod.Namespace, pod.Name, pod.Spec.NodeName)
		return oc.ensureLocalZonePod(oldPod, pod, addPort)
	}

	klog.V(5).Infof("Ensuring zone remote for Pod %s/%s in node %s", pod.Namespace, pod.Name, pod.Spec.NodeName)
	return oc.ensureRemoteZonePod(oldPod, pod, addPort)
}

// ensureLocalZonePod tries to set up a local zone pod. It returns nil on success and error on failure; failure
// indicates the pod set up should be retried later.
func (oc *DefaultNetworkController) ensureLocalZonePod(oldPod, pod *corev1.Pod, addPort bool) error {
	if config.Metrics.EnableScaleMetrics {
		start := time.Now()
		defer func() {
			duration := time.Since(start)
			eventName := "add"
			if !addPort {
				eventName = "update"
			}
			metrics.RecordPodEvent(eventName, duration)
		}()
	}

	if oldPod != nil && (exGatewayAnnotationsChanged(oldPod, pod) || networkStatusAnnotationsChanged(oldPod, pod)) {
		// No matter if a pod is ovn networked, or host networked, we still need to check for exgw
		// annotations. If the pod is ovn networked and is in update reschedule, addLogicalPort will take
		// care of updating the exgw updates
		if err := oc.deletePodExternalGW(oldPod); err != nil {
			return fmt.Errorf("ensurePod failed %s/%s: %w", pod.Namespace, pod.Name, err)
		}
	}

	if !util.PodWantsHostNetwork(pod) && addPort {
		if err := oc.addLogicalPort(pod); err != nil {
			return fmt.Errorf("addLogicalPort failed for %s/%s: %w", pod.Namespace, pod.Name, err)
		}
	} else {
		// either pod is host-networked or its an update for a normal pod (addPort=false case)
		if oldPod == nil || exGatewayAnnotationsChanged(oldPod, pod) || networkStatusAnnotationsChanged(oldPod, pod) || podBecameReady(oldPod, pod) {
			if err := oc.addPodExternalGW(pod); err != nil {
				return fmt.Errorf("addPodExternalGW failed for %s/%s: %w", pod.Namespace, pod.Name, err)
			}
		}
	}

	// update open ports for UDN pods on pod update.
	if util.IsNetworkSegmentationSupportEnabled() && !util.PodWantsHostNetwork(pod) && !addPort &&
		pod != nil && oldPod != nil &&
		pod.Annotations[util.UDNOpenPortsAnnotationName] != oldPod.Annotations[util.UDNOpenPortsAnnotationName] {
		networkRole, err := oc.GetNetworkRole(pod)
		if err != nil {
			return err
		}
		if networkRole == ovntypes.NetworkRoleInfrastructure {
			// only update for non-default network pods
			portName := oc.GetLogicalPortName(pod, oc.GetNetworkName())
			err := oc.setUDNPodOpenPorts(pod.Namespace+"/"+pod.Name, pod.Annotations, portName)
			if err != nil {
				return fmt.Errorf("failed to update UDN pod  %s/%s open ports: %w", pod.Namespace, pod.Name, err)
			}
		}
	}

	if kubevirt.IsPodLiveMigratable(pod) {
		v4Subnets, v6Subnets := util.GetClusterSubnetsWithHostPrefix()
		return kubevirt.EnsureLocalZonePodAddressesToNodeRoute(oc.watchFactory, oc.nbClient, oc.lsManager, pod, ovntypes.DefaultNetworkName, append(v4Subnets, v6Subnets...))
	}

	return nil
}

func (oc *DefaultNetworkController) ensureRemotePodIP(oldPod, pod *corev1.Pod, addPort bool) error {
	if (addPort || (oldPod != nil && len(pod.Status.PodIPs) != len(oldPod.Status.PodIPs))) && !util.PodWantsHostNetwork(pod) {
		podIfAddrs, err := util.GetPodCIDRsWithFullMask(pod, oc.GetNetInfo())
		if err != nil {
			// not finding pod IPs on a remote pod is common until the other node wires the pod, suppress it
			return fmt.Errorf("failed to obtain IPs to add remote pod %s/%s: %w",
				pod.Namespace, pod.Name, ovntypes.NewSuppressedError(err))
		}
		if err := oc.addRemotePodToNamespace(pod.Namespace, podIfAddrs); err != nil {
			return fmt.Errorf("failed to add remote pod %s/%s to namespace: %w", pod.Namespace, pod.Name, err)
		}
	}
	return nil
}

// ensureRemoteZonePod tries to set up remote zone pod bits required to interconnect it.
//   - Adds the remote pod ips to the pod namespace address set for network policy and egress gw
//
// It returns nil on success and error on failure; failure indicates the pod set up should be retried later.
func (oc *DefaultNetworkController) ensureRemoteZonePod(oldPod, pod *corev1.Pod, addPort bool) error {
	if err := oc.ensureRemotePodIP(oldPod, pod, addPort); err != nil {
		return err
	}

	//FIXME: Update comments & reduce code duplication.
	// check if this remote pod is serving as an external GW.
	if oldPod != nil && (exGatewayAnnotationsChanged(oldPod, pod) || networkStatusAnnotationsChanged(oldPod, pod)) {
		// Delete the routes in the namespace associated with this remote oldPod if its acting as an external GW
		if err := oc.deletePodExternalGW(oldPod); err != nil {
			return fmt.Errorf("deletePodExternalGW failed for remote pod %s/%s: %w", oldPod.Namespace, oldPod.Name, err)
		}
	}

	// either pod is host-networked or its an update for a normal pod (addPort=false case)
	if oldPod == nil || exGatewayAnnotationsChanged(oldPod, pod) || networkStatusAnnotationsChanged(oldPod, pod) || podBecameReady(oldPod, pod) {
		// check if this remote pod is serving as an external GW. If so add the routes in the namespace
		// associated with this remote pod
		if err := oc.addPodExternalGW(pod); err != nil {
			return fmt.Errorf("addPodExternalGW failed for remote pod %s/%s: %v", pod.Namespace, pod.Name, err)
		}
	}
	if kubevirt.IsPodLiveMigratable(pod) {
		return kubevirt.EnsureRemoteZonePodAddressesToNodeRoute(oc.watchFactory, oc.nbClient, pod, ovntypes.DefaultNetworkName)
	}
	return nil
}

// removePod tried to tear down a pod. It returns nil on success and error on failure;
// failure indicates the pod tear down should be retried later.
func (oc *DefaultNetworkController) removePod(pod *corev1.Pod, portInfo *lpInfo) error {
	if oc.isPodScheduledinLocalZone(pod) {
		if err := oc.removeLocalZonePod(pod, portInfo); err != nil {
			return err
		}
	} else {
		if err := oc.removeRemoteZonePod(pod); err != nil {
			return err
		}
	}

	err := kubevirt.CleanUpLiveMigratablePod(oc.nbClient, oc.watchFactory, pod)
	if err != nil {
		return err
	}

	oc.forgetPodReleasedBeforeStartup(string(pod.UID), ovntypes.DefaultNetworkName)
	return nil
}

// removeLocalZonePod tries to tear down a local zone pod. It returns nil on success and error on failure;
// failure indicates the pod tear down should be retried later.
func (oc *DefaultNetworkController) removeLocalZonePod(pod *corev1.Pod, portInfo *lpInfo) error {
	oc.logicalPortCache.remove(pod, ovntypes.DefaultNetworkName)

	if config.Metrics.EnableScaleMetrics {
		start := time.Now()
		defer func() {
			duration := time.Since(start)
			metrics.RecordPodEvent("delete", duration)
		}()
	}
	if util.PodWantsHostNetwork(pod) {
		if err := oc.deletePodExternalGW(pod); err != nil {
			return fmt.Errorf("unable to delete external gateway routes for pod %s: %w",
				getPodNamespacedName(pod), err)
		}
		return nil
	}
	if err := oc.deleteLogicalPort(pod, portInfo); err != nil {
		return fmt.Errorf("deleteLogicalPort failed for pod %s: %w",
			getPodNamespacedName(pod), err)
	}

	return nil
}

// removeRemoteZonePod tries to tear down a remote zone pod bits. It returns nil on success and error on failure;
// failure indicates the pod tear down should be retried later.
// It removes the remote pod ips from the namespace address set and if its an external gw pod, removes
// its routes.
func (oc *DefaultNetworkController) removeRemoteZonePod(pod *corev1.Pod) error {
	// Delete the routes in the namespace associated with this remote pod if it was acting as an external GW
	if err := oc.deletePodExternalGW(pod); err != nil {
		return fmt.Errorf("unable to delete external gateway routes for remote pod %s: %w",
			getPodNamespacedName(pod), err)
	}

	// while this check is only intended for local pods, we also need it for
	// remote live migrated pods that might have been allocated from this zone
	if oc.wasPodReleasedBeforeStartup(string(pod.UID), ovntypes.DefaultNetworkName) {
		klog.Infof("Completed pod %s/%s was already released before startup",
			pod.Namespace,
			pod.Name,
		)
		return nil
	}

	if err := oc.removeRemoteZonePodFromNamespaceAddressSet(pod); err != nil {
		return fmt.Errorf("failed to remove the remote zone pod: %w", err)
	}

	if kubevirt.IsPodLiveMigratable(pod) {
		ips, err := util.GetPodCIDRsWithFullMask(pod, oc.GetNetInfo())
		if err != nil && !errors.Is(err, util.ErrNoPodIPFound) {
			return fmt.Errorf("failed to get pod ips for the pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		switchName, zoneContainsPodSubnet := kubevirt.ZoneContainsPodSubnet(oc.lsManager, ips)
		if zoneContainsPodSubnet {
			if err := oc.lsManager.ReleaseIPs(switchName, ips); err != nil {
				return err
			}
		}
	}

	return nil
}

// WatchEgressFirewall starts the watching of egressfirewall resource and calls
// back the appropriate handler logic
func (oc *DefaultNetworkController) WatchEgressFirewall() error {
	_, err := oc.retryEgressFirewalls.WatchResource()
	return err
}

// WatchEgressNodes starts the watching of egress assignable nodes and calls
// back the appropriate handler logic.
func (oc *DefaultNetworkController) WatchEgressNodes() error {
	_, err := oc.retryEgressNodes.WatchResource()
	return err
}

// WatchEgressIP starts the watching of egressip resource and calls back the
// appropriate handler logic. It also initiates the other dedicated resource
// handlers for egress IP setup: namespaces, pods.
func (oc *DefaultNetworkController) WatchEgressIP() error {
	_, err := oc.retryEgressIPs.WatchResource()
	return err
}

func (oc *DefaultNetworkController) WatchEgressIPNamespaces() error {
	_, err := oc.retryEgressIPNamespaces.WatchResource()
	return err
}

func (oc *DefaultNetworkController) WatchEgressIPPods() error {
	_, err := oc.retryEgressIPPods.WatchResource()
	return err
}

// syncNodeGateway ensures a node's gateway router is configured
func (oc *DefaultNetworkController) syncNodeGateway(node *corev1.Node) error {
	gwConfig, err := oc.nodeGatewayConfig(node)
	if err != nil {
		return fmt.Errorf("error getting gateway config for node %s: %v", node.Name, err)
	}

	if err := oc.newGatewayManager(node.Name).SyncGateway(
		node,
		gwConfig,
	); err != nil {
		return fmt.Errorf("error creating gateway for node %s: %v", node.Name, err)
	}

	if util.IsPodNetworkAdvertisedAtNode(oc, node.Name) {
		return oc.addAdvertisedNetworkIsolation(node.Name)
	}
	return oc.deleteAdvertisedNetworkIsolation(node.Name)
}

// gatewayChanged() compares old annotations to new and returns true if something has changed.
func gatewayChanged(oldNode, newNode *corev1.Node) bool {
	return oldNode.Annotations[util.OvnNodeL3GatewayConfig] != newNode.Annotations[util.OvnNodeL3GatewayConfig] ||
		oldNode.Annotations[util.OvnNodeChassisID] != newNode.Annotations[util.OvnNodeChassisID]
}

// hostCIDRsChanged compares old annotations to new and returns true if the something has changed.
func hostCIDRsChanged(oldNode, newNode *corev1.Node) bool {
	return util.NodeHostCIDRsAnnotationChanged(oldNode, newNode)
}

func nodeSubnetChanged(oldNode, node *corev1.Node, netName string) bool {
	if !util.NodeSubnetAnnotationChanged(oldNode, node) {
		return false
	}

	return util.NodeSubnetAnnotationChangedForNetwork(oldNode, node, netName)
}

func joinCIDRChanged(oldNode, node *corev1.Node, netName string) bool {
	var oldCIDRs, newCIDRs map[string]json.RawMessage

	if oldNode.Annotations[util.OVNNodeGRLRPAddrs] == node.Annotations[util.OVNNodeGRLRPAddrs] {
		return false
	}

	if err := json.Unmarshal([]byte(oldNode.Annotations[util.OVNNodeGRLRPAddrs]), &oldCIDRs); err != nil {
		klog.Errorf("Failed to unmarshal old node %s annotation: %v", oldNode.Name, err)
		return false
	}
	if err := json.Unmarshal([]byte(node.Annotations[util.OVNNodeGRLRPAddrs]), &newCIDRs); err != nil {
		klog.Errorf("Failed to unmarshal new node %s annotation: %v", node.Name, err)
		return false
	}
	return !bytes.Equal(oldCIDRs[netName], newCIDRs[netName])
}

func primaryAddrChanged(oldNode, newNode *corev1.Node) bool {
	oldIP, _ := util.GetNodePrimaryIP(oldNode)
	newIP, _ := util.GetNodePrimaryIP(newNode)
	return oldIP != newIP
}

func nodeChassisChanged(oldNode, node *corev1.Node) bool {
	return util.NodeChassisIDAnnotationChanged(oldNode, node)
}

// nodeGatewayMTUSupportChanged returns true if annotation "k8s.ovn.org/gateway-mtu-support" on the node was updated.
func nodeGatewayMTUSupportChanged(oldNode, node *corev1.Node) bool {
	return oldNode.Annotations[util.OvnNodeGatewayMtuSupport] != node.Annotations[util.OvnNodeGatewayMtuSupport]
}

// shouldUpdateNode() determines if the ovn-kubernetes plugin should update the state of the node.
// ovn-kube should not perform an update if it does not assign a hostsubnet, or if you want to change
// whether or not ovn-kubernetes assigns a hostsubnet
func shouldUpdateNode(node, oldNode *corev1.Node) bool {
	newNoHostSubnet := util.NoHostSubnet(node)
	oldNoHostSubnet := util.NoHostSubnet(oldNode)

	if oldNoHostSubnet && newNoHostSubnet {
		return false
	}

	return true
}

func (oc *DefaultNetworkController) StartServiceController(wg *sync.WaitGroup, runRepair bool) error {
	useLBGroups := oc.clusterLoadBalancerGroupUUID != ""
	// use 5 workers like most of the kubernetes controllers in the
	// kubernetes controller-manager
	err := oc.svcController.Run(5, oc.stopChan, wg, runRepair, useLBGroups, oc.svcTemplateSupport)
	if err != nil {

		return fmt.Errorf("error running OVN Kubernetes Services controller: %v", err)
	}
	return nil
}

func (oc *DefaultNetworkController) InitEgressServiceZoneController() (*egresssvc_zone.Controller, error) {
	// If the EgressIP controller is enabled it will take care of creating the
	// "no reroute" policies - we can pass "noop" functions to the egress service controller.
	initClusterEgressPolicies := func(_ libovsdbclient.Client, _ addressset.AddressSetFactory, _ util.NetInfo, _ []*net.IPNet, _, _ string) error {
		return nil
	}
	ensureNodeNoReroutePolicies := func(_ libovsdbclient.Client, _ addressset.AddressSetFactory, _, _, _ string, _ listers.NodeLister, _, _ bool) error {
		return nil
	}
	// used only when IC=true
	createDefaultNodeRouteToExternal := func(_ libovsdbclient.Client, _, _ string, _ []config.CIDRNetworkEntry, _ []*net.IPNet) error {
		return nil
	}

	if !config.OVNKubernetesFeature.EnableEgressIP {
		initClusterEgressPolicies = InitClusterEgressPolicies
		ensureNodeNoReroutePolicies = ensureDefaultNoRerouteNodePolicies
		createDefaultNodeRouteToExternal = libovsdbutil.CreateDefaultRouteToExternal
	}

	return egresssvc_zone.NewController(oc.GetNetInfo(), DefaultNetworkControllerName, oc.client, oc.nbClient, oc.addressSetFactory,
		initClusterEgressPolicies, ensureNodeNoReroutePolicies,
		createDefaultNodeRouteToExternal,
		oc.stopChan, oc.watchFactory.EgressServiceInformer(), oc.watchFactory.ServiceCoreInformer(),
		oc.watchFactory.EndpointSliceCoreInformer(),
		oc.watchFactory.NodeCoreInformer(), oc.zone)
}

func (oc *DefaultNetworkController) newANPController() error {
	var err error
	oc.anpController, err = anpcontroller.NewController(
		DefaultNetworkControllerName,
		oc.nbClient,
		oc.kube.ANPClient,
		oc.watchFactory.ANPInformer(),
		oc.watchFactory.BANPInformer(),
		oc.watchFactory.NamespaceCoreInformer(),
		oc.watchFactory.PodCoreInformer(),
		oc.watchFactory.NodeCoreInformer(),
		oc.addressSetFactory,
		oc.isPodScheduledinLocalZone,
		oc.zone,
		oc.recorder,
		oc.observManager,
	)
	return err
}
