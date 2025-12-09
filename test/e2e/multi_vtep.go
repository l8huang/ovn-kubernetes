package e2e

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/ovn-org/ovn-kubernetes/test/e2e/deploymentconfig"
	"github.com/ovn-org/ovn-kubernetes/test/e2e/feature"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"

	nadapi "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	nadclient "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/clientset/versioned/typed/k8s.cni.cncf.io/v1"

	"github.com/ovn-org/ovn-kubernetes/test/e2e/infraprovider"
	infraapi "github.com/ovn-org/ovn-kubernetes/test/e2e/infraprovider/api"
)

const (
	networkEncapIPMappingAnnotation = "test.k8s.ovn.org/pod-network-encap-ip-mapping"
)

type MultiVtepNode struct {
	Node     *v1.Node
	EncapIPs []string
}

var _ = Describe("Multi VTEP", feature.MultiVTEP, func() {
	const (
		clientPodName  = "client-pod"
		serverPod1Name = "server-pod-1"
		serverPod2Name = "server-pod-2"
		port           = 9000

		secondaryNetworkCIDR       = "172.31.0.0/16" // last subnet in private range 172.16.0.0/12 (rfc1918)
		secondaryNetworkName       = "tenant-blue"
		secondaryFlatL2IgnoreCIDR  = "172.31.0.0/29"
		secondaryFlatL2NetworkCIDR = "172.31.0.0/24"
		netPrefixLengthPerNode     = 24
		secondaryIPv6CIDR          = "2010:100:200::0/60"
		netPrefixLengthIPv6PerNode = 64
	)
	f := wrappedTestFramework("multi-vtep")

	var (
		cs          clientset.Interface
		nadClient   nadclient.K8sCniCncfIoV1Interface
		providerCtx infraapi.Context

		multiVtepNodes []MultiVtepNode
		vtepInterfaces = []string{"eth0", "vtep1"}
	)

	BeforeEach(func() {
		cs = f.ClientSet

		var err error
		nadClient, err = nadclient.NewForConfig(f.ClientConfig())
		Expect(err).NotTo(HaveOccurred())

		providerCtx = infraprovider.Get().NewTestContext()
		_ = nadClient
		_ = providerCtx

		By("Getting multi-vtep nodes")
		nodeList, err := e2enode.GetReadySchedulableNodes(context.TODO(), cs)
		Expect(err).NotTo(HaveOccurred())
		for _, node := range nodeList.Items {
			encapIPs, _ := util.ParseNodeEncapIPsAnnotation(&node)
			if len(encapIPs) > 1 {
				GinkgoT().Logf("Found multi-vtep node: %s, encap IPs: %v", node.Name, encapIPs)
				multiVtepNodes = append(multiVtepNodes, MultiVtepNode{Node: &node, EncapIPs: encapIPs})
			}
		}
		Expect(len(nodeList.Items)).To(BeNumerically(">", 1), "cluster should have at least 2  multi-vtep nodes for testing")

	})

	Context("Pods with an OVN-K secondary network", func() {

		DescribeTable("can communicate over 2nd VTEP", func(netConfigParams networkAttachmentConfigParams, podConfigs []podConfiguration, clientPodName string, serverPodName string) {
			netConfig := newNetworkAttachmentConfig(netConfigParams)
			netConfig.namespace = f.Namespace.Name
			By("creating the attachment configuration")
			_, err := nadClient.NetworkAttachmentDefinitions(netConfig.namespace).Create(
				context.Background(),
				generateNAD(netConfig, f.ClientSet),
				metav1.CreateOptions{},
			)
			Expect(err).NotTo(HaveOccurred())

			By("updating client and server Pod config configurations")
			podConfigByName := map[string]podConfiguration{}
			for _, podConfig := range podConfigs {
				podConfig.namespace = f.Namespace.Name
				encapIp := multiVtepNodes[podConfig.nodeIndex].EncapIPs[podConfig.vtepIndex]
				podConfig.annotations = map[string]string{
					networkEncapIPMappingAnnotation: fmt.Sprintf(`{"%s":"%s"}`, netConfig.name, encapIp),
				}
				podConfig.nodeSelector = map[string]string{nodeHostnameKey: multiVtepNodes[podConfig.nodeIndex].Node.Name}
				podConfigByName[podConfig.name] = podConfig
			}

			By("instantiating the pods")
			podByName := map[string]*v1.Pod{}
			for _, pc := range podConfigByName {
				pod, err := cs.CoreV1().Pods(pc.namespace).Create(
					context.Background(),
					generatePodSpec(pc),
					metav1.CreateOptions{},
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(pod).NotTo(BeNil())
				podByName[pod.Name] = pod
			}

			By("asserting the pods gets to the `Ready` phase")
			for _, pod := range podByName {
				Eventually(func() v1.PodPhase {
					updatedPod, err := cs.CoreV1().Pods(pod.Namespace).Get(context.Background(), pod.GetName(), metav1.GetOptions{})
					if err != nil {
						return v1.PodFailed
					}
					return updatedPod.Status.Phase
				}, 2*time.Minute, 6*time.Second).Should(Equal(v1.PodRunning))
			}

			By("getting the ovnkube-node pods")
			ovnKubeNodePodByNodeName := map[string]string{}
			ovnKubernetesNamespace := deploymentconfig.Get().OVNKubernetesNamespace()
			ovnkubeNodes, err := cs.CoreV1().Pods(ovnKubernetesNamespace).List(context.Background(), metav1.ListOptions{LabelSelector: "app=ovnkube-node"})
			Expect(err).NotTo(HaveOccurred())
			for _, ovnkubeNode := range ovnkubeNodes.Items {
				GinkgoT().Logf("ovnkube-node: %s on node %s", ovnkubeNode.Name, ovnkubeNode.Spec.NodeName)
				ovnKubeNodePodByNodeName[ovnkubeNode.Spec.NodeName] = ovnkubeNode.Name
			}

			clientPodConfig := podConfigByName[clientPodName]
			serverPodConfig := podConfigByName[serverPodName]
			serverPod := podByName[serverPodName]
			serverNodeName := multiVtepNodes[serverPodConfig.nodeIndex].Node.Name
			expectedVtepInterface := vtepInterfaces[serverPodConfig.vtepIndex]

			By("getting the server IP")
			serverIP, err := podIPForAttachment(cs, f.Namespace.Name, serverPod.GetName(), netConfig.name, 0)
			Expect(err).NotTo(HaveOccurred())
			GinkgoT().Logf("serverIP: %s", serverIP)

			By("starting tcpdump on all VTEP interfaces on the server's ovnkube-node pod")
			ovnkubeNodePod := ovnKubeNodePodByNodeName[serverNodeName]
			Expect(ovnkubeNodePod).NotTo(BeEmpty(), "ovnkube-node pod not found for node %s", serverNodeName)

			ovnKubeContainerName := "ovnkube-node"
			if isInterconnectEnabled() {
				ovnKubeContainerName = "ovnkube-controller"
			}
			ovnkubeNodeExecutor := ForPod(ovnKubernetesNamespace, ovnkubeNodePod, ovnKubeContainerName)

			// Start tcpdump on each VTEP interface in background, writing text output to a file
			tcpdumpOutputFiles := map[string]string{}
			for _, iface := range vtepInterfaces {
				outputFile := fmt.Sprintf("/tmp/%s-%s.txt", f.Namespace.Name, iface)
				tcpdumpOutputFiles[iface] = outputFile
				// Start tcpdump in background with nohup to survive shell exit, timeout to auto-terminate
				tcpdumpCmd := fmt.Sprintf("nohup timeout 300 tcpdump -l -i %s -nne 'geneve and host %s' > %s 2>&1 &", iface, serverIP, outputFile)
				_, err := ovnkubeNodeExecutor.Exec("sh", "-c", tcpdumpCmd)
				if err != nil {
					GinkgoT().Logf("Warning: failed to start tcpdump on %s: %v", iface, err)
				}
			}

			// Wait for tcpdump to start
			time.Sleep(300 * time.Second)

			By("asserting the client pod can contact the server pod")
			Eventually(func() error {
				return reachServerPodFromClient(cs, serverPodConfig, clientPodConfig, serverIP, port)
			}, 2*time.Minute, 6*time.Second).Should(Succeed())

			By("stopping tcpdump and verifying packets on expected VTEP interface")
			// Wait for packets to be captured and flushed to file
			time.Sleep(3 * time.Second)
			// Kill tcpdump processes
			_, _ = ovnkubeNodeExecutor.Exec("sh", "-c", "pkill -f tcpdump || true")
			time.Sleep(1 * time.Second)

			// Check packet counts on each interface by counting lines in output files
			for iface, outputFile := range tcpdumpOutputFiles {
				countCmd := fmt.Sprintf("wc -l < %s 2>/dev/null || echo 0", outputFile)
				output, err := ovnkubeNodeExecutor.Exec("sh", "-c", countCmd)
				if err != nil {
					GinkgoT().Logf("Warning: failed to read output for %s: %v", iface, err)
					continue
				}
				count, _ := strconv.Atoi(strings.TrimSpace(output))
				GinkgoT().Logf("VTEP interface %s: %d packets captured", iface, count)

				if iface == expectedVtepInterface {
					Expect(count).To(BeNumerically(">", 0), "expected packets on VTEP interface %s but got none", iface)
				} else {
					Expect(count).To(Equal(0), "unexpected packets on VTEP interface %s", iface)
				}

				// Cleanup output file
				_, _ = ovnkubeNodeExecutor.Exec("rm", "-f", outputFile)
			}
		},
			Entry("when attaching to L3 network over 1nd VTEP",
				networkAttachmentConfigParams{
					name:     secondaryNetworkName,
					topology: "layer3",
					cidr:     netCIDR(secondaryNetworkCIDR, netPrefixLengthPerNode),
				},
				[]podConfiguration{
					{
						name:        clientPodName,
						attachments: []nadapi.NetworkSelectionElement{{Name: secondaryNetworkName}},
						vtepIndex:   0,
						nodeIndex:   0,
					},
					{
						name:         serverPod1Name,
						attachments:  []nadapi.NetworkSelectionElement{{Name: secondaryNetworkName}},
						containerCmd: httpServerContainerCmd(port),
						vtepIndex:    0,
						nodeIndex:    1,
					},
				},
				clientPodName,
				serverPod1Name),
		)
	})

})
