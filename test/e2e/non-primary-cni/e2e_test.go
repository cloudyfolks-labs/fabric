package non_primary_cni

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	klog "k8s.io/klog/v2"
	k8sframework "k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/config"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"

	ginkgo "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
	"github.com/kubeovn/kube-ovn/pkg/util"
	"github.com/kubeovn/kube-ovn/test/e2e/framework"
	"github.com/kubeovn/kube-ovn/test/e2e/framework/docker"
	"github.com/kubeovn/kube-ovn/test/e2e/framework/kind"
)

func init() {
	klog.SetOutput(ginkgo.GinkgoWriter)

	// Register flags.
	config.CopyFlags(config.Flags, flag.CommandLine)
	k8sframework.RegisterCommonFlags(flag.CommandLine)
	k8sframework.RegisterClusterFlags(flag.CommandLine)
}

func TestE2E(t *testing.T) {
	k8sframework.AfterReadingAllFlags(&k8sframework.TestContext)
	// Note: environment validation will happen during test execution

	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "kube-ovn non-primary cni e2e suite")
}

// Constants for test configuration
const (
	EnvTestConfigPath    = "TEST_CONFIG_PATH"
	EnvKubeOVNPrimaryCNI = "KUBE_OVN_PRIMARY_CNI"

	DefaultNetworkInterface = "net1"
	DefaultConfigPath       = "/opt/testconfigs"
	DefaultCommandTimeout   = 30 * time.Second
)

// Helper functions
func getTestConfigFile(relativePath string) string {
	testConfigPath := os.Getenv(EnvTestConfigPath)
	if testConfigPath == "" {
		testConfigPath = DefaultConfigPath
	}
	return filepath.Join(testConfigPath, relativePath)
}

func runBashCommand(command string) (string, error) {
	cmd := exec.Command("bash", "-c", command)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func isKubeOVNPrimaryCNI() bool {
	return os.Getenv(EnvKubeOVNPrimaryCNI) == "true"
}

// removeFinalizers removes finalizers from Kube-OVN resources to ensure cleanup
func removeFinalizers(configStage string) {
	ginkgo.GinkgoHelper()

	ginkgo.By(fmt.Sprintf("Removing finalizers from config-stage=%s resources", configStage))

	// Get all resources with the specific config-stage label
	cmd := fmt.Sprintf("kubectl get all,vpc,subnet,networkattachmentdefinitions,providernet,vlan -l config-stage=%s -o custom-columns=KIND:.kind,NAMESPACE:.metadata.namespace,NAME:.metadata.name --no-headers 2>/dev/null || true", configStage)
	output, _ := runBashCommand(cmd)

	if strings.TrimSpace(output) == "" {
		return
	}

	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) >= 3 {
			kind := fields[0]
			namespace := fields[1]
			name := fields[2]

			var patchCmd string
			if namespace == "<none>" || namespace == metav1.NamespaceNone {
				// Cluster-scoped resource
				patchCmd = fmt.Sprintf(`kubectl patch %s %s --type=merge -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true`, strings.ToLower(kind), name)
			} else {
				// Namespaced resource
				patchCmd = fmt.Sprintf(`kubectl patch %s %s -n %s --type=merge -p '{"metadata":{"finalizers":[]}}' 2>/dev/null || true`, strings.ToLower(kind), name, namespace)
			}
			_, _ = runBashCommand(patchCmd)
		}
	}
}

// KindBridgeNetwork represents KIND bridge network configuration
type KindBridgeNetwork struct {
	CIDR    string
	Gateway string
}

// detectKindBridgeNetwork dynamically detects KIND bridge network configuration
func detectKindBridgeNetwork() *KindBridgeNetwork {
	ginkgo.GinkgoHelper()

	ginkgo.By("Detecting KIND bridge network configuration")

	network, err := docker.NetworkInspect(kind.NetworkName)
	framework.ExpectNoError(err, "Failed to inspect KIND network %s", kind.NetworkName)

	for _, config := range network.IPAM.Config {
		if config.Subnet.IsValid() && util.CheckProtocol(config.Subnet.String()) == kubeovnv1.ProtocolIPv4 {
			ginkgo.By(fmt.Sprintf("Detected KIND bridge network: CIDR=%s, Gateway=%s", config.Subnet, config.Gateway))
			return &KindBridgeNetwork{CIDR: config.Subnet.String(), Gateway: config.Gateway.String()}
		}
	}
	framework.Failf("No IPv4 subnet found in KIND network %s", kind.NetworkName)
	return nil
}

// generateExcludeIPs creates a YAML list of IPs to exclude based on the gateway
func generateExcludeIPs(_, gateway string) string {
	lastDot := strings.LastIndex(gateway, ".")
	if lastDot == -1 {
		return "- " + gateway
	}
	baseIP := gateway[:lastDot]
	var ips []string
	for i := 1; i <= 5; i++ {
		ips = append(ips, fmt.Sprintf("- %s.%d", baseIP, i))
	}
	return strings.Join(ips, "\n    ")
}

// processConfigWithKindBridge dynamically updates YAML configuration with KIND bridge network
func processConfigWithKindBridge(yamlPath string, kindNetwork *KindBridgeNetwork) string {
	ginkgo.GinkgoHelper()

	ginkgo.By(fmt.Sprintf("Processing config file %s with KIND bridge network", yamlPath))

	content, err := os.ReadFile(yamlPath)
	framework.ExpectNoError(err, "Failed to read config file %s", yamlPath)

	// Replace common bridge network CIDRs with actual KIND bridge CIDR
	bridgeCIDRs := []string{"172.17.0.0/16", "172.18.0.0/16", "172.19.0.0/16", "172.20.0.0/16"}
	bridgeGateways := []string{"172.17.0.1", "172.18.0.1", "172.19.0.1", "172.20.0.1"}

	yamlContent := string(content)
	for _, cidr := range bridgeCIDRs {
		yamlContent = strings.ReplaceAll(yamlContent, cidr, kindNetwork.CIDR)
	}
	for _, gw := range bridgeGateways {
		yamlContent = strings.ReplaceAll(yamlContent, gw, kindNetwork.Gateway)
	}

	templateReplacements := map[string]string{
		"<cidrBlock01>":                       kindNetwork.CIDR,
		"<gateway01>":                         kindNetwork.Gateway,
		"<00-lnet-simple-subnet1-cidr>":       kindNetwork.CIDR,
		"<00-lnet-simple-subnet1-gateway>":    kindNetwork.Gateway,
		"<00-lnet-simple-subnet1-exclude-ip>": generateExcludeIPs(kindNetwork.CIDR, kindNetwork.Gateway),
	}

	for placeholder, value := range templateReplacements {
		yamlContent = strings.ReplaceAll(yamlContent, placeholder, value)
	}

	// Create temporary file with updated configuration
	tmpFile, err := os.CreateTemp(os.TempDir(), "kind-config-*.yaml")
	framework.ExpectNoError(err, "Failed to create temporary config file")
	defer tmpFile.Close()

	framework.Logf("Writing config to temporary file %s:\n%s", tmpFile.Name(), yamlContent)
	_, err = tmpFile.WriteString(yamlContent)
	framework.ExpectNoError(err, "Failed to write updated config")

	ginkgo.By(fmt.Sprintf("Created dynamic config file: %s", tmpFile.Name()))
	return tmpFile.Name()
}

// Helper function to get pod IPs (primary or non-primary)
func getPodIPs(pod *corev1.Pod) []string {
	ginkgo.GinkgoHelper()

	if isKubeOVNPrimaryCNI() {
		return util.PodIPs(*pod)
	}
	return getPodNonPrimaryIP(pod)
}

// VPC Simple Test
var _ = framework.SerialDescribe("[group:non-primary-cni]", func() {
	f := framework.NewDefaultFramework("non-primary-cni-vpc-simple")

	ginkgo.Context("VPC Simple", ginkgo.Label("Feature:VPC-Simple"), func() {
		namespaceName := "vpc-simple-ns"
		podNames := []string{"vpc-simple-pod1", "vpc-simple-pod2", "vpc-multi-nic-pod"}
		yamlFile := getTestConfigFile("VPC/00-vpc-simple.yaml")

		var nodeNames []string
		var cs clientset.Interface
		var podClient *framework.PodClient

		ginkgo.BeforeEach(func() {
			ginkgo.By("Initialize clients")
			cs = f.ClientSet
			podClient = f.PodClientNS(namespaceName)

			ginkgo.By("Get cluster nodes")
			nodeObjs, err := e2enode.GetReadySchedulableNodes(context.Background(), cs)
			framework.ExpectNoError(err)
			for _, node := range nodeObjs.Items {
				nodeNames = append(nodeNames, node.Name)
			}

			for stage := 0; stage <= 1; stage++ {
				ginkgo.By(fmt.Sprintf("Apply YAML with config-stage=%d", stage))
				cmd := fmt.Sprintf("kubectl apply -f %s --prune -l config-stage=%d", yamlFile, stage)
				output, err := runBashCommand(cmd)
				framework.ExpectNoError(err, "Failed to apply stage %d config: %s", stage, output)
				if stage == 0 {
					time.Sleep(5 * time.Second)
				}
			}
		})

		ginkgo.AfterEach(func() {
			ginkgo.By("Cleanup resources")
			for stage := 1; stage >= 0; stage-- {
				removeFinalizers(strconv.Itoa(stage))
				cmd := fmt.Sprintf("kubectl delete -f %s --ignore-not-found=true -l config-stage=%d --timeout=30s", yamlFile, stage)
				_, _ = runBashCommand(cmd)
			}
		})

		ginkgo.It("Should create pods and test connectivity in VPC", func() {
			ginkgo.By("Wait for pods to be ready")
			for _, podName := range podNames {
				pod := podClient.GetPod(podName)
				podClient.WaitForRunning(pod.Name)
			}

			ginkgo.By("Test connectivity between pods")
			pod1 := podClient.GetPod(podNames[0])
			pod2 := podClient.GetPod(podNames[1])

			// Get pod IPs
			pod1IPs := getPodIPs(pod1)
			pod2IPs := getPodIPs(pod2)

			framework.ExpectNotEmpty(pod1IPs, "Pod1 should have at least one IP address")
			framework.ExpectNotEmpty(pod2IPs, "Pod2 should have at least one IP address")

			pod1IP := strings.Join(pod1IPs, ",")
			for _, pod2IP := range pod2IPs {
				description := fmt.Sprintf("from %s (%s) to %s (%s)", pod1.Name, pod1IP, pod2.Name, pod2IP)
				err := testPodConnectivity(pod1, pod2IP, description)
				framework.ExpectNoError(err, "Ping should succeed between pods in VPC")
			}
		})
	})
})

// Logical Network Simple Test
var _ = framework.SerialDescribe("[group:non-primary-cni]", func() {
	f := framework.NewDefaultFramework("non-primary-cni-lnet-simple")

	ginkgo.Context("Logical Network Simple", ginkgo.Label("Feature:LogicalNetwork-Simple"), func() {
		namespaceName := "lnet-simple-ns"
		podNames := []string{"lnet-simple-pod1", "lnet-simple-pod2"}
		originalYamlFile := getTestConfigFile("LogicalNetwork/00-lnet-simple.yaml")
		var yamlFile string // Will be set dynamically

		var nodeNames []string
		var cs clientset.Interface
		var podClient *framework.PodClient

		ginkgo.BeforeEach(func() {
			ginkgo.By("Initialize clients")
			cs = f.ClientSet
			podClient = f.PodClientNS(namespaceName)

			ginkgo.By("Get cluster nodes")
			nodeObjs, err := e2enode.GetReadySchedulableNodes(context.Background(), cs)
			framework.ExpectNoError(err)
			for _, node := range nodeObjs.Items {
				nodeNames = append(nodeNames, node.Name)
			}

			ginkgo.By("Detect KIND bridge network and generate dynamic config")
			kindNetwork := detectKindBridgeNetwork()
			yamlFile = processConfigWithKindBridge(originalYamlFile, kindNetwork)
			ginkgo.DeferCleanup(func() {
				if yamlFile != originalYamlFile {
					os.Remove(yamlFile)
				}
			})

			for stage := 0; stage <= 1; stage++ {
				ginkgo.By(fmt.Sprintf("Apply YAML with config-stage=%d", stage))
				cmd := fmt.Sprintf("kubectl apply -f %s --prune -l config-stage=%d", yamlFile, stage)
				output, err := runBashCommand(cmd)
				framework.ExpectNoError(err, "Failed to apply stage %d config: %s", stage, output)
				if stage == 0 {
					time.Sleep(1 * time.Second)
				} else {
					time.Sleep(10 * time.Second)
				}
			}
		})

		ginkgo.AfterEach(func() {
			ginkgo.By("Cleanup resources")
			for stage := 1; stage >= 0; stage-- {
				removeFinalizers(strconv.Itoa(stage))
				cmd := fmt.Sprintf("kubectl delete -f %s --ignore-not-found=true -l config-stage=%d --timeout=30s", yamlFile, stage)
				_, _ = runBashCommand(cmd)
			}
		})

		ginkgo.It("Should create pods and test connectivity in logical network", func() {
			ginkgo.By("Wait for pods to be ready")
			for _, podName := range podNames {
				pod := podClient.GetPod(podName)
				podClient.WaitForRunning(pod.Name)
			}

			ginkgo.By("Test connectivity between pods")
			pod1 := podClient.GetPod(podNames[0])
			pod2 := podClient.GetPod(podNames[1])

			// Get pod IPs
			pod1IPs := getPodIPs(pod1)
			pod2IPs := getPodIPs(pod2)
			framework.ExpectNotEmpty(pod1IPs, "Pod1 should have at least one IP address")
			framework.ExpectNotEmpty(pod2IPs, "Pod2 should have at least one IP address")

			pod1IP := strings.Join(pod1IPs, ",")
			for _, pod2IP := range pod2IPs {
				description := fmt.Sprintf("from %s (%s) to %s (%s)", pod1.Name, pod1IP, pod2.Name, pod2IP)
				err := testPodConnectivity(pod1, pod2IP, description)
				framework.ExpectNoError(err, "Ping should succeed between pods in logical network")
			}
		})
	})
})

// Helper function to get non-primary IP from pod annotation
func getPodNonPrimaryIP(pod *corev1.Pod) []string {
	ginkgo.GinkgoHelper()

	// For non-primary CNI, look for k8s.v1.cni.cncf.io/networks annotation
	network := pod.Annotations[nadv1.NetworkAttachmentAnnot]
	if network == "" {
		return nil
	}

	ips, err := util.PodAttachmentIPs(pod, network)
	framework.ExpectNoError(err, "Failed to get pod attachment IPs for pod %s", pod.Name)
	if len(ips) != 0 {
		return ips
	}

	// For Kube-OVN non-primary CNI, the IP is stored in a specific annotation format:
	// {network-attachment-name}.{namespace}.fabric.cloudyfolks.io/ip_address
	// Example: vpc-simple-nad.vpc-simple-ns.fabric.cloudyfolks.io/ip_address: 10.100.0.2
	// Extract the network attachment definition name from the networks annotation
	// Format: namespace/nad-name (e.g., "vpc-simple-ns/vpc-simple-nad")
	// Convert namespace/nad-name to nad-name.namespace.fabric.cloudyfolks.io/ip_address format
	parts := strings.Split(network, "/")
	if len(parts) != 2 {
		return nil
	}
	namespace := parts[0]
	name := parts[1]

	// Construct the Kube-OVN IP annotation key
	ipAnnotationKey := fmt.Sprintf(util.IPAddressAnnotationTemplate, fmt.Sprintf("%s.%s", name, namespace))
	// Get the IP from the annotation
	ip := pod.Annotations[ipAnnotationKey]
	if ip != "" {
		return strings.Split(ip, ",")
	}

	return nil
}

// Helper function to test network connectivity with proper interface handling
func testPodConnectivity(sourcePod *corev1.Pod, targetIP, description string) error {
	return testPodConnectivityWithInterface(sourcePod, targetIP, description, DefaultNetworkInterface)
}

// Helper function to test network connectivity with specified interface
func testPodConnectivityWithInterface(sourcePod *corev1.Pod, targetIP, description, interfaceName string) error {
	ginkgo.By(fmt.Sprintf("Testing connectivity: %s", description))

	var cmd string
	if isKubeOVNPrimaryCNI() {
		cmd = fmt.Sprintf("ping -c 3 %s", targetIP)
		_, _, err := framework.KubectlExec(sourcePod.Namespace, sourcePod.Name, cmd)
		return err
	}
	// For non-primary CNI, use specific interface
	if interfaceName == "" {
		interfaceName = DefaultNetworkInterface
	}
	cmd = fmt.Sprintf("ping -I %s -c 3 %s", interfaceName, targetIP)
	_, _, err := framework.KubectlExec(sourcePod.Namespace, sourcePod.Name, cmd)
	return err
}

// Iptables Cleanup Verification Test
var _ = framework.SerialDescribe("[group:non-primary-cni]", func() {
	f := framework.NewDefaultFramework("non-primary-cni-iptables")

	ginkgo.Context("Iptables Cleanup", ginkgo.Label("Feature:Iptables-Cleanup"), func() {
		var cs clientset.Interface

		ginkgo.BeforeEach(func() {
			cs = f.ClientSet
			f.SkipVersionPriorTo(1, 17, "iptables cleanup in non-primary CNI mode requires v1.17+")
		})

		ginkgo.It("Should not have kube-ovn iptables chains or rules in non-primary CNI mode", func() {
			ginkgo.By("Get cluster nodes")
			nodeObjs, err := e2enode.GetReadySchedulableNodes(context.Background(), cs)
			framework.ExpectNoError(err)
			gomega.Expect(nodeObjs.Items).NotTo(gomega.BeEmpty(), "cluster should have at least one schedulable node")

			ginkgo.By("Get ovs-ovn pod on first node")
			node := nodeObjs.Items[0]
			daemonSetClient := f.DaemonSetClientNS(framework.KubeOvnNamespace)
			ds := daemonSetClient.Get("ovs-ovn")
			ovsPod, err := daemonSetClient.GetPodOnNode(ds, node.Name)
			framework.ExpectNoError(err)

			// Kube-OVN custom chains that should not exist in non-primary CNI mode
			kubeOvnChains := []struct {
				table string
				chain string
			}{
				{"nat", "OVN-PREROUTING"},
				{"nat", "OVN-POSTROUTING"},
				{"nat", "OVN-MASQUERADE"},
				{"nat", "OVN-NAT-POLICY"},
				{"mangle", "OVN-PREROUTING"},
				{"mangle", "OVN-POSTROUTING"},
				{"mangle", "OVN-OUTPUT"},
			}

			for _, tc := range kubeOvnChains {
				ginkgo.By(fmt.Sprintf("Verify chain %s/%s does not exist", tc.table, tc.chain))
				// Use iptables -S to list all rules in the table and verify the chain
				// name is absent. This is more robust across iptables variants
				// (iptables-legacy vs iptables-nft) than relying on a specific error
				// message when querying a non-existent chain.
				cmd := fmt.Sprintf("iptables -t %s -S 2>&1", tc.table)
				stdout, _, err := framework.KubectlExec(ovsPod.Namespace, ovsPod.Name, cmd)
				framework.ExpectNoError(err)
				gomega.Expect(string(stdout)).NotTo(
					gomega.ContainSubstring(tc.chain),
					fmt.Sprintf("chain %s should not exist in table %s", tc.chain, tc.table),
				)
			}

			ginkgo.By("Verify no OVN-NAT-PSUBNET- chains in nat table")
			cmd := "iptables -t nat -S 2>&1"
			stdout, _, err := framework.KubectlExec(ovsPod.Namespace, ovsPod.Name, cmd)
			framework.ExpectNoError(err)
			gomega.Expect(string(stdout)).NotTo(
				gomega.ContainSubstring("OVN-NAT-PSUBNET-"),
				"nat table should not contain OVN-NAT-PSUBNET- chains",
			)

			ginkgo.By("Verify no kube-ovn jump rules in nat/mangle PREROUTING/POSTROUTING")
			for _, table := range []string{"nat", "mangle"} {
				for _, chain := range []string{"PREROUTING", "POSTROUTING"} {
					cmd := fmt.Sprintf("iptables -t %s -S %s", table, chain)
					stdout, _, err := framework.KubectlExec(ovsPod.Namespace, ovsPod.Name, cmd)
					framework.ExpectNoError(err)
					output := string(stdout)
					gomega.Expect(output).NotTo(
						gomega.ContainSubstring("kube-ovn"),
						fmt.Sprintf("%s %s should not contain kube-ovn jump rules", table, chain),
					)
					gomega.Expect(output).NotTo(
						gomega.ContainSubstring("OVN-"),
						fmt.Sprintf("%s %s should not contain OVN- chain references", table, chain),
					)
				}
			}

			ginkgo.By("Verify no kube-ovn jump rules in mangle OUTPUT")
			cmd = "iptables -t mangle -S OUTPUT"
			stdout, _, err = framework.KubectlExec(ovsPod.Namespace, ovsPod.Name, cmd)
			framework.ExpectNoError(err)
			gomega.Expect(string(stdout)).NotTo(
				gomega.ContainSubstring("OVN-"),
				"mangle OUTPUT should not contain OVN- chain references",
			)

			ginkgo.By("Verify no kube-ovn rules in filter INPUT/OUTPUT/FORWARD")
			for _, chain := range []string{"INPUT", "OUTPUT", "FORWARD"} {
				cmd := fmt.Sprintf("iptables -t filter -S %s", chain)
				stdout, _, err := framework.KubectlExec(ovsPod.Namespace, ovsPod.Name, cmd)
				framework.ExpectNoError(err)
				output := string(stdout)
				gomega.Expect(output).NotTo(
					gomega.ContainSubstring("ovn40subnets"),
					fmt.Sprintf("filter %s should not contain ovn40subnets rules", chain),
				)
				gomega.Expect(output).NotTo(
					gomega.ContainSubstring("ovn40services"),
					fmt.Sprintf("filter %s should not contain ovn40services rules", chain),
				)
				gomega.Expect(output).NotTo(
					gomega.ContainSubstring("ovn-subnet-gateway"),
					fmt.Sprintf("filter %s should not contain ovn-subnet-gateway rules", chain),
				)
			}

			ginkgo.By("Verify no kube-ovn NodePort MARK rules in nat PREROUTING")
			cmd = "iptables -t nat -S PREROUTING"
			stdout, _, err = framework.KubectlExec(ovsPod.Namespace, ovsPod.Name, cmd)
			framework.ExpectNoError(err)
			output := string(stdout)
			gomega.Expect(output).NotTo(
				gomega.ContainSubstring("0x80000/0x80000"),
				"nat PREROUTING should not contain NodePort MARK rules",
			)
			gomega.Expect(output).NotTo(
				gomega.ContainSubstring("0x4000/0x4000"),
				"nat PREROUTING should not contain service MARK rules",
			)
		})
	})
})
