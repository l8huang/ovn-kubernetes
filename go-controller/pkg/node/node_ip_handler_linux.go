//go:build linux
// +build linux

package node

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/vishvananda/netlink"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/node/bridgeconfig"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/node/managementport"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

type addressManager struct {
	nodeName      string
	watchFactory  factory.NodeWatchFactory
	cidrs         sets.Set[string]
	nodeAnnotator kube.Annotator
	mgmtPort      managementport.Interface
	// useNetlink indicates the addressManager should use machine
	// information from netlink. Set to false for testcases.
	useNetlink bool
	syncPeriod time.Duration
	// compare node primary IP change
	nodePrimaryAddr net.IP
	gatewayBridge   *bridgeconfig.BridgeConfiguration

	OnChanged func()
	sync.Mutex
}

// initializes a new address manager which will hold all the IPs on a node
func newAddressManager(nodeName string, k kube.Interface, mgmtPort managementport.Interface, watchFactory factory.NodeWatchFactory, gwBridge *bridgeconfig.BridgeConfiguration) *addressManager {
	return newAddressManagerInternal(nodeName, k, mgmtPort, watchFactory, gwBridge, true)
}

// newAddressManagerInternal creates a new address manager; this function is
// only expose for testcases to disable netlink subscription to ensure
// reproducibility of unit tests.
func newAddressManagerInternal(nodeName string, k kube.Interface, mgmtPort managementport.Interface, watchFactory factory.NodeWatchFactory, gwBridge *bridgeconfig.BridgeConfiguration, useNetlink bool) *addressManager {
	mgr := &addressManager{
		nodeName:      nodeName,
		watchFactory:  watchFactory,
		cidrs:         sets.New[string](),
		mgmtPort:      mgmtPort,
		gatewayBridge: gwBridge,
		OnChanged:     func() {},
		useNetlink:    useNetlink,
		syncPeriod:    30 * time.Second,
	}
	mgr.nodeAnnotator = kube.NewNodeAnnotator(k, nodeName)
	if config.OvnKubeNode.Mode == types.NodeModeDPU {
		if err := mgr.updateHostCIDRs(); err != nil {
			klog.Errorf("Failed to update host-cidrs annotations on node %s: %v", nodeName, err)
			return nil
		}
		if err := mgr.nodeAnnotator.Run(); err != nil {
			klog.Errorf("Failed to set host-cidrs annotations on node %s: %v", nodeName, err)
			return nil
		}
	} else {
		mgr.sync()
	}

	return mgr
}

// updates the address manager with a new IP
// returns true if there was an update
func (c *addressManager) addAddr(ipnet net.IPNet, linkIndex int) bool {
	c.Lock()
	defer c.Unlock()
	if !c.cidrs.Has(ipnet.String()) && c.isValidNodeIP(ipnet.IP, linkIndex) {
		klog.Infof("Adding IP: %s, to node IP manager", ipnet)
		c.cidrs.Insert(ipnet.String())
		return true
	}

	return false
}

// removes IP from address manager
// returns true if there was an update
func (c *addressManager) delAddr(ipnet net.IPNet, linkIndex int) bool {
	c.Lock()
	defer c.Unlock()
	if c.cidrs.Has(ipnet.String()) && c.isValidNodeIP(ipnet.IP, linkIndex) {
		klog.Infof("Removing IP: %s, from node IP manager", ipnet)
		c.cidrs.Delete(ipnet.String())
		return true
	}

	return false
}

// ListAddresses returns all the addresses we know about
func (c *addressManager) ListAddresses() ([]net.IP, []*net.IPNet) {
	c.Lock()
	defer c.Unlock()
	addrs := sets.List(c.cidrs)
	addresses := make([]net.IP, 0, len(addrs))
	networkAddresses := make([]*net.IPNet, 0, len(addrs))
	for _, addr := range addrs {
		ip, networkAddress, err := net.ParseCIDR(addr)
		if err != nil {
			klog.Errorf("Failed to parse %s: %v", addr, err)
			continue
		}
		addresses = append(addresses, ip)
		networkAddresses = append(networkAddresses, networkAddress)
	}
	return addresses, networkAddresses
}

type subscribeFn func() (bool, chan netlink.AddrUpdate, error)

func (c *addressManager) Run(stopChan <-chan struct{}, doneWg *sync.WaitGroup) {
	if config.OvnKubeNode.Mode == types.NodeModeDPU {
		return
	}

	c.addHandlerForPrimaryAddrChange()
	doneWg.Add(1)
	go func() {
		c.runInternal(stopChan, c.getNetlinkAddrSubFunc(stopChan))
		doneWg.Done()
	}()
}

// runInternal gathers node IP information and publishes it on the k8 node annotations.
// The annotations it updates are k8s.ovn.org/host-cidrs, k8s.ovn.org/node-primary-ifaddr and k8s.ovn.org/l3-gateway-config.
// It waits on 3 events and only the "stop" event may end execution.
// Event 1: Address change events using a subscription func. In normal execution, this is a netlink addr subscription func that returns a channel that
// conveys address updates that can be processed upon immediately.
// Event 2: Ticker events which is used to trigger a sync func. This is required in-case address change events are missed.
// Event 3: Stop events which stops event watching and returns.
func (c *addressManager) runInternal(stopChan <-chan struct{}, subscribe subscribeFn) {
	addressSyncTimer := time.NewTicker(c.syncPeriod)
	defer addressSyncTimer.Stop()

	subscribed, addrChan, err := subscribe()
	if err != nil {
		klog.Errorf("Error during netlink subscribe for IP Manager: %v", err)
	}
	klog.Info("Node IP manager is running")
	for {
		select {
		case a, ok := <-addrChan:
			addressSyncTimer.Reset(c.syncPeriod)
			if !ok {
				if subscribed, addrChan, err = subscribe(); err != nil {
					klog.Errorf("Error during netlink re-subscribe due to channel closing for IP Manager: %v", err)
				}
				continue
			}
			addrChanged := false
			if a.NewAddr {
				addrChanged = c.addAddr(a.LinkAddress, a.LinkIndex)
			} else {
				addrChanged = c.delAddr(a.LinkAddress, a.LinkIndex)
			}

			c.handleNodePrimaryAddrChange()
			if addrChanged || !c.doNodeHostCIDRsMatch() {
				klog.Infof("Host CIDRs changed to %v. Updating node address annotations.", c.cidrs)
				err := c.updateNodeAddressAnnotations()
				if err != nil {
					klog.Errorf("Address Manager failed to update node address annotations: %v", err)
				}
				c.OnChanged()
			}
		case <-addressSyncTimer.C:
			if subscribed {
				klog.V(5).Info("Node IP manager calling sync() explicitly")
				c.sync()
			} else {
				if subscribed, addrChan, err = subscribe(); err != nil {
					klog.Errorf("Error during netlink re-subscribe for IP Manager: %v", err)
				}
			}
		case <-stopChan:
			klog.Info("Node IP manager is finished")
			return
		}
	}
}

func (c *addressManager) getNetlinkAddrSubFunc(stopChan <-chan struct{}) func() (bool, chan netlink.AddrUpdate, error) {
	addrSubscribeOptions := netlink.AddrSubscribeOptions{
		ErrorCallback: func(err error) {
			klog.Errorf("Failed during AddrSubscribe callback: %v", err)
			// Note: Not calling sync() from here: it is redudant and unsafe when stopChan is closed.
		},
	}
	return func() (bool, chan netlink.AddrUpdate, error) {
		addrChan := make(chan netlink.AddrUpdate)
		if err := netlink.AddrSubscribeWithOptions(addrChan, stopChan, addrSubscribeOptions); err != nil {
			return false, nil, err
		}
		// sync the manager with current addresses on the node
		c.sync()
		return true, addrChan, nil
	}
}

// addHandlerForPrimaryAddrChange handles reconfiguration of a node primary IP address change
func (c *addressManager) addHandlerForPrimaryAddrChange() {
	// Add an event handler to the node informer. This is needed for cases where users first update the node's IP
	// address but only later update kubelet configuration and restart kubelet (which in turn will update the reported
	// IP address inside the node's status field).
	nodeInformer := c.watchFactory.NodeInformer()
	_, err := nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, _ interface{}) {
			c.handleNodePrimaryAddrChange()
		},
	})
	if err != nil {
		klog.Fatalf("Could not add node event handler while starting address manager %v", err)
	}
}

// updates OVN's EncapIP if the node IP changed
func (c *addressManager) handleNodePrimaryAddrChange() {
	c.Lock()
	defer c.Unlock()
	nodePrimaryAddrChanged, err := c.nodePrimaryAddrChanged()
	if err != nil {
		klog.Errorf("Address Manager failed to check node primary address change: %v", err)
		return
	}
	if nodePrimaryAddrChanged && config.Default.EncapIP == "" {
		klog.Infof("Node primary address changed to %v. Updating OVN encap IP.", c.nodePrimaryAddr)
		c.updateOVNEncapIPAndReconnect(c.nodePrimaryAddr)
	}
}

// updateNodeAddressAnnotations updates all relevant annotations for the node including
// k8s.ovn.org/host-cidrs, k8s.ovn.org/node-primary-ifaddr, k8s.ovn.org/l3-gateway-config.
func (c *addressManager) updateNodeAddressAnnotations() error {
	var err error
	var ifAddrs []*net.IPNet

	// Get node information
	node, err := c.watchFactory.GetNode(c.nodeName)
	if err != nil {
		return err
	}

	if c.useNetlink {
		// get updated interface IP addresses for the gateway bridge
		ifAddrs, err = c.gatewayBridge.UpdateInterfaceIPAddresses(node)
		if err != nil {
			return err
		}
	}

	// update k8s.ovn.org/host-cidrs
	if err = c.updateHostCIDRs(); err != nil {
		return err
	}

	// sets both IPv4 and IPv6 primary IP addr in annotation k8s.ovn.org/node-primary-ifaddr
	// Note: this is not the API node's internal interface, but the primary IP on the gateway
	// bridge (cf. gateway_init.go)
	if err = util.SetNodePrimaryIfAddrs(c.nodeAnnotator, ifAddrs); err != nil {
		return err
	}

	// update k8s.ovn.org/l3-gateway-config
	gatewayCfg, err := util.ParseNodeL3GatewayAnnotation(node)
	if err != nil {
		return err
	}
	gatewayCfg.IPAddresses = ifAddrs
	err = util.SetL3GatewayConfig(c.nodeAnnotator, gatewayCfg)
	if err != nil {
		return err
	}

	// push all updates to the node
	err = c.nodeAnnotator.Run()
	if err != nil {
		return err
	}
	return nil
}

func (c *addressManager) updateHostCIDRs() error {
	if config.OvnKubeNode.Mode == types.NodeModeDPU {
		// For DPU mode, we don't need to update the host-cidrs annotation.
		return nil
	}

	return util.SetNodeHostCIDRs(c.nodeAnnotator, c.cidrs)
}

func (c *addressManager) assignCIDRs(nodeHostCIDRs sets.Set[string]) bool {
	c.Lock()
	defer c.Unlock()

	if nodeHostCIDRs.Equal(c.cidrs) {
		return false
	}
	c.cidrs = nodeHostCIDRs
	return true
}

func (c *addressManager) doNodeHostCIDRsMatch() bool {
	c.Lock()
	defer c.Unlock()

	node, err := c.watchFactory.GetNode(c.nodeName)
	if err != nil {
		klog.Errorf("Unable to get node from informer")
		return false
	}
	// check to see if ips on the node differ from what we stored
	// in host-cidrs annotation
	nodeHostAddresses, err := util.ParseNodeHostCIDRs(node)
	if err != nil {
		klog.Errorf("Unable to parse addresses from node host %s: %s", node.Name, err.Error())
		return false
	}

	return nodeHostAddresses.Equal(c.cidrs)
}

// nodePrimaryAddrChanged returns false if there is an error or if the IP does
// match, otherwise it returns true and updates the current primary IP address.
func (c *addressManager) nodePrimaryAddrChanged() (bool, error) {
	node, err := c.watchFactory.GetNode(c.nodeName)
	if err != nil {
		return false, err
	}
	// check to see if ips on the node differ from what we stored
	// in addressManager and it's an address that is known locally
	nodePrimaryAddrStr, err := util.GetNodePrimaryIP(node)
	if err != nil {
		return false, err
	}
	nodePrimaryAddr := net.ParseIP(nodePrimaryAddrStr)

	if nodePrimaryAddr == nil {
		return false, fmt.Errorf("failed to parse the primary IP address string from kubernetes node status")
	}

	var exists bool
	for _, hostCIDR := range c.cidrs.UnsortedList() {
		ip, _, err := net.ParseCIDR(hostCIDR)
		if err != nil {
			klog.Errorf("Node IP: failed to parse node address %q. Unable to detect if node primary address changed: %v",
				hostCIDR, err)
			continue
		}
		if ip.Equal(nodePrimaryAddr) {
			exists = true
			break
		}
	}

	if !exists || c.nodePrimaryAddr.Equal(nodePrimaryAddr) {
		return false, nil
	}
	c.nodePrimaryAddr = nodePrimaryAddr

	return true, nil
}

// detects if the IP is valid for a node
// excludes things like local IPs, mgmt port ip, special masquerade IP and Egress IPs for non-ovs type interfaces
func (c *addressManager) isValidNodeIP(addr net.IP, linkIndex int) bool {
	if addr == nil {
		return false
	}
	if addr.IsLinkLocalUnicast() {
		return false
	}
	if addr.IsLoopback() {
		return false
	}
	// check CDN management port
	mgmtPortAddress, _ := util.MatchFirstIPNetFamily(utilnet.IsIPv6(addr), c.mgmtPort.GetAddresses())
	if mgmtPortAddress != nil && addr.Equal(mgmtPortAddress.IP) {
		return false
	}

	if util.IsNetworkSegmentationSupportEnabled() {
		// check CDN + UDN management ports
		if mpLink, err := util.GetNetLinkOps().LinkByIndex(linkIndex); err != nil {
			klog.Errorf("Unable to determine if link is an OVN management port for address %s and link index %d: %v", addr.String(), linkIndex, err)
		} else {
			if strings.HasPrefix(mpLink.Attrs().Name, types.K8sMgmtIntfNamePrefix) {
				return false
			}
		}
	}

	if util.IsAddressReservedForInternalUse(addr) {
		return false
	}
	if config.OVNKubernetesFeature.EnableEgressIP {
		// EIP assigned to the primary interface which selects pods with a role primary user defined network must be excluded.
		if util.IsNetworkSegmentationSupportEnabled() && config.OVNKubernetesFeature.EnableInterconnect && config.Gateway.Mode != config.GatewayModeDisabled {
			// Two methods to lookup EIPs assigned to the gateway bridge. Fast path from a shared cache or slow path from node annotations.
			// At startup, gateway bridge cache gets sync
			eipMarkIPs := c.gatewayBridge.GetEIPMarkIPs()
			if eipMarkIPs != nil && eipMarkIPs.HasSyncdOnce() && eipMarkIPs.IsIPPresent(addr) {
				return false
			} else {
				if eipAddresses, err := c.getPrimaryHostEgressIPs(); err != nil {
					klog.Errorf("Failed to get primary host assigned Egress IPs and ensure they are excluded: %v", err)
				} else {
					if eipAddresses.Has(addr.String()) {
						return false
					}
				}
			}
		}
		if !util.PlatformTypeIsEgressIPCloudProvider() {
			// IPs assigned to host interfaces to support the egress IP multi NIC feature must be excluded.
			if eipAddresses, err := c.getSecondaryHostEgressIPs(); err != nil {
				klog.Errorf("Failed to get secondary host assigned Egress IPs and ensure they are excluded: %v", err)
			} else {
				if eipAddresses.Has(addr.String()) {
					return false
				}
			}
		}
	}

	return true
}

func (c *addressManager) sync() {
	if config.OvnKubeNode.Mode == types.NodeModeDPU {
		return
	}

	var addrs []netlink.Addr

	if c.useNetlink {
		links, err := netlink.LinkList()
		if err != nil {
			klog.Errorf("Failed sync due to being unable to list links: %v", err)
			return
		}
		for _, link := range links {
			foundAddrs, err := netlink.AddrList(link, getSupportedIPFamily())
			if err != nil {
				klog.Errorf("Failed sync due to being unable to list addresses for %q: %v", link.Attrs().Name, err)
				return
			}
			addrs = append(addrs, foundAddrs...)
		}
	}

	currAddresses := sets.New[string]()
	for _, addr := range addrs {
		if !c.isValidNodeIP(addr.IP, addr.LinkIndex) {
			klog.V(5).Infof("Skipping non-useable IP address for host: %s", addr.String())
			continue
		}
		netAddr := net.IPNet{IP: addr.IP, Mask: addr.Mask}
		currAddresses.Insert(netAddr.String())
	}

	addrChanged := c.assignCIDRs(currAddresses)
	c.handleNodePrimaryAddrChange()
	if addrChanged || !c.doNodeHostCIDRsMatch() {
		klog.Infof("Node address changed to %v. Updating annotations.", currAddresses)
		err := c.updateNodeAddressAnnotations()
		if err != nil {
			klog.Errorf("Address Manager failed to update node address annotations: %v", err)
		}
		c.OnChanged()
	}
}

// getSecondaryHostEgressIPs returns the set of egress IPs that are assigned to standard linux interfaces (non ovs type). The
// addresses are used to support Egress IP multi NIC feature. The addresses must not be included in address manager
// because the addresses are only to support Egress IP multi NIC feature and must not be exposed via host-cidrs annot.
func (c *addressManager) getSecondaryHostEgressIPs() (sets.Set[string], error) {
	node, err := c.watchFactory.GetNode(c.nodeName)
	if err != nil {
		return nil, fmt.Errorf("unable to get Node from informer: %v", err)
	}
	eipAddrs, err := util.ParseNodeSecondaryHostEgressIPsAnnotation(node)
	if err != nil {
		if util.IsAnnotationNotSetError(err) {
			return sets.New[string](), nil
		}
		return nil, err
	}
	return eipAddrs, nil
}

func (c *addressManager) getPrimaryHostEgressIPs() (sets.Set[string], error) {
	node, err := c.watchFactory.GetNode(c.nodeName)
	if err != nil {
		return nil, fmt.Errorf("unable to get Node from informer: %v", err)
	}
	eipAddrs, err := util.ParseNodeBridgeEgressIPsAnnotation(node)
	if err != nil {
		if util.IsAnnotationNotSetError(err) {
			eipAddrs = make([]string, 0)
		} else {
			return nil, err
		}
	}
	return sets.New[string](eipAddrs...), nil
}

// updateOVNEncapIPAndReconnect updates encap IP to OVS when the node primary IP changed.
func (c *addressManager) updateOVNEncapIPAndReconnect(newIP net.IP) {
	checkCmd := []string{
		"get",
		"Open_vSwitch",
		".",
		"external_ids:ovn-encap-ip",
	}
	encapIP, stderr, err := util.RunOVSVsctl(checkCmd...)
	if err != nil {
		klog.Warningf("Unable to retrieve configured ovn-encap-ip from OVS: %v, %q", err, stderr)
	} else {
		encapIP = strings.TrimSuffix(encapIP, "\n")
		if len(encapIP) > 0 && newIP.String() == encapIP {
			klog.V(4).Infof("Will not update encap IP %s - it is already configured", newIP.String())
			return
		}
	}

	config.Default.EffectiveEncapIP = newIP.String()
	confCmd := []string{
		"set",
		"Open_vSwitch",
		".",
		fmt.Sprintf("external_ids:ovn-encap-ip=%s", newIP),
	}

	_, stderr, err = util.RunOVSVsctl(confCmd...)
	if err != nil {
		klog.Errorf("Error setting OVS encap IP %s: %v %q", newIP.String(), err, stderr)
		return
	}

	// force ovn-controller to reconnect SB with new encap IP immediately.
	// otherwise there will be a max delay of 200s due to the 100s
	// ovn-controller inactivity probe.
	_, stderr, err = util.RunOVNAppctlWithTimeout(5, "-t", "ovn-controller", "exit", "--restart")
	if err != nil {
		klog.Errorf("Failed to exit ovn-controller %v %q", err, stderr)
		return
	}

	// Update node-encap-ips annotation
	encapIPList := sets.New[string](config.Default.EffectiveEncapIP)
	if err := util.SetNodeEncapIPs(c.nodeAnnotator, encapIPList); err != nil {
		klog.Errorf("Failed to set node-encap-ips annotation for node %s: %v", c.nodeName, err)
		return
	}

	if err := c.nodeAnnotator.Run(); err != nil {
		klog.Errorf("Failed to set node %s annotations: %v", c.nodeName, err)
		return
	}
}

func getSupportedIPFamily() int {
	var ipFamily int // value of 0 means include both IP v4 and v6 addresses
	if config.IPv4Mode && !config.IPv6Mode {
		ipFamily = netlink.FAMILY_V4
	} else if !config.IPv4Mode && config.IPv6Mode {
		ipFamily = netlink.FAMILY_V6
	}
	return ipFamily
}
