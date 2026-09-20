package controller

// Unified Fake Controller for Testing
//
// This file provides a unified approach to creating fake controllers for testing.
// The main function is newFakeControllerWithOptions() which accepts optional parameters
// for subnets, NADs (Network Attachment Definitions), pods, and namespaces.
//
// The fake controller properly initializes:
// - Kubernetes fake client with pods and namespaces
// - NAD fake client with network attachment definitions (populated via API)
// - Fabric fake client with subnets (populated via API)
// - All necessary informers with proper synchronization
// - Mock OVN client for OVN operations

import (
	"context"
	"fmt"
	"os"
	"testing"

	nadv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	nadfake "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/clientset/versioned/fake"
	nadinformers "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/client/informers/externalversions"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/keymutex"

	mockovs "github.com/cloudyfolks-labs/fabric/mocks/pkg/ovs"
	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	fabricfake "github.com/cloudyfolks-labs/fabric/pkg/client/clientset/versioned/fake"
	fabricinformerfactory "github.com/cloudyfolks-labs/fabric/pkg/client/informers/externalversions"
	fabricinformer "github.com/cloudyfolks-labs/fabric/pkg/client/informers/externalversions/fabric/v1"
	ovnipam "github.com/cloudyfolks-labs/fabric/pkg/ipam"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func TestMain(m *testing.M) {
	// Disable WatchListClient feature gate because the NAD fake client doesn't
	// implement IsWatchListSemanticsUnSupported(), causing informer reflectors
	// to hang with WatchList (enabled by default since k8s 1.35).
	if err := os.Setenv("KUBE_FEATURE_WatchListClient", "false"); err != nil {
		fmt.Fprintf(os.Stderr, "failed to set KUBE_FEATURE_WatchListClient: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

type fakeControllerInformers struct {
	vpcInformer       fabricinformer.VpcInformer
	subnetInformer    fabricinformer.SubnetInformer
	ipInformer        fabricinformer.IPInformer
	vlanInformer      fabricinformer.VlanInformer
	serviceInformer   coreinformers.ServiceInformer
	namespaceInformer coreinformers.NamespaceInformer
	nodeInformer      coreinformers.NodeInformer
	podInformer       coreinformers.PodInformer
}

type fakeController struct {
	fakeController  *Controller
	fakeInformers   *fakeControllerInformers
	mockOvnClient   *mockovs.MockNbClient
	mockOvnSbClient *mockovs.MockSbClient
}

func alwaysReady() bool { return true }

// FakeControllerOptions holds optional parameters for creating a fake controller
type FakeControllerOptions struct {
	Subnets            []*fabricv1.Subnet
	IPPools            []*fabricv1.IPPool
	IPs                []*fabricv1.IP
	Vlans              []*fabricv1.Vlan
	ProviderNetworks   []*fabricv1.ProviderNetwork
	NetworkAttachments []*nadv1.NetworkAttachmentDefinition
	Pods               []*corev1.Pod
	Nodes              []*corev1.Node
	Namespaces         []*corev1.Namespace
	Services           []*corev1.Service
	Vpcs               []*fabricv1.Vpc
	RouterLBRules      []*fabricv1.RouterLBRule
	LoadBalancers      []*fabricv1.LoadBalancer
	LoadBalancerPools  []*fabricv1.LoadBalancerPool
	OvnEips            []*fabricv1.OvnEip
	OvnDnatRules       []*fabricv1.OvnDnatRule
	OvnFipRules        []*fabricv1.OvnFip
	OvnSnatRules       []*fabricv1.OvnSnatRule
	DNSZones           []*fabricv1.DNSZone
}

// newFakeControllerWithOptions creates a fake controller with optional pre-populated objects
func newFakeControllerWithOptions(t *testing.T, opts *FakeControllerOptions) (*fakeController, error) {
	if opts == nil {
		opts = &FakeControllerOptions{}
	}

	namespaces := opts.Namespaces
	if len(namespaces) == 0 {
		// Create default namespace if none provided
		namespaces = []*corev1.Namespace{{
			ObjectMeta: metav1.ObjectMeta{
				Name: metav1.NamespaceDefault,
				Annotations: map[string]string{
					util.LogicalSwitchAnnotation: util.DefaultSubnet,
				},
			},
		}}
	}

	// Create fake Kubernetes client with namespaces, pods, nodes, and services
	kubeObjects := make([]runtime.Object, 0, len(namespaces)+len(opts.Pods)+len(opts.Nodes)+len(opts.Services))
	for _, ns := range namespaces {
		kubeObjects = append(kubeObjects, ns)
	}
	for _, pod := range opts.Pods {
		kubeObjects = append(kubeObjects, pod)
	}
	for _, node := range opts.Nodes {
		kubeObjects = append(kubeObjects, node)
	}
	for _, svc := range opts.Services {
		kubeObjects = append(kubeObjects, svc)
	}
	kubeClient := fake.NewSimpleClientset(kubeObjects...)

	// Create fake NAD client
	nadClient := nadfake.NewSimpleClientset()
	for _, nad := range opts.NetworkAttachments {
		_, err := nadClient.K8sCniCncfIoV1().NetworkAttachmentDefinitions(nad.Namespace).Create(
			context.Background(), nad, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}

	// Create fake Fabric client
	fabricClient := fabricfake.NewSimpleClientset()
	for _, subnet := range opts.Subnets {
		_, err := fabricClient.FabricV1().Subnets().Create(
			context.Background(), subnet, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, ippool := range opts.IPPools {
		_, err := fabricClient.FabricV1().IPPools().Create(
			context.Background(), ippool, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, ip := range opts.IPs {
		_, err := fabricClient.FabricV1().IPs().Create(
			context.Background(), ip, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, vlan := range opts.Vlans {
		_, err := fabricClient.FabricV1().Vlans().Create(
			context.Background(), vlan, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, pn := range opts.ProviderNetworks {
		_, err := fabricClient.FabricV1().ProviderNetworks().Create(
			context.Background(), pn, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, vpc := range opts.Vpcs {
		_, err := fabricClient.FabricV1().Vpcs().Create(
			context.Background(), vpc, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, rlr := range opts.RouterLBRules {
		_, err := fabricClient.FabricV1().RouterLBRules().Create(
			context.Background(), rlr, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, lb := range opts.LoadBalancers {
		_, err := fabricClient.FabricV1().LoadBalancers().Create(
			context.Background(), lb, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, zone := range opts.DNSZones {
		_, err := fabricClient.FabricV1().DNSZones().Create(
			context.Background(), zone, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, pool := range opts.LoadBalancerPools {
		_, err := fabricClient.FabricV1().LoadBalancerPools().Create(
			context.Background(), pool, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, eip := range opts.OvnEips {
		_, err := fabricClient.FabricV1().OvnEips().Create(
			context.Background(), eip, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, dnat := range opts.OvnDnatRules {
		_, err := fabricClient.FabricV1().OvnDnatRules().Create(
			context.Background(), dnat, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, fip := range opts.OvnFipRules {
		_, err := fabricClient.FabricV1().OvnFips().Create(
			context.Background(), fip, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	for _, snat := range opts.OvnSnatRules {
		_, err := fabricClient.FabricV1().OvnSnatRules().Create(
			context.Background(), snat, metav1.CreateOptions{},
		)
		if err != nil {
			return nil, err
		}
	}

	// Create informer factories
	kubeInformerFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, 0,
		informers.WithTransform(util.TrimManagedFields),
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.Watch = true
			options.AllowWatchBookmarks = true
		}),
	)
	serviceInformer := kubeInformerFactory.Core().V1().Services()
	namespaceInformer := kubeInformerFactory.Core().V1().Namespaces()
	nodeInformer := kubeInformerFactory.Core().V1().Nodes()
	podInformer := kubeInformerFactory.Core().V1().Pods()
	endpointSliceInformer := kubeInformerFactory.Discovery().V1().EndpointSlices()

	nadInformerFactory := nadinformers.NewSharedInformerFactoryWithOptions(nadClient, 0,
		nadinformers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.Watch = true
			options.AllowWatchBookmarks = true
		}),
	)
	nadInformer := nadInformerFactory.K8sCniCncfIo().V1().NetworkAttachmentDefinitions()

	fabricInformerFactory := fabricinformerfactory.NewSharedInformerFactoryWithOptions(fabricClient, 0,
		fabricinformerfactory.WithTransform(util.TrimManagedFields),
		fabricinformerfactory.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.Watch = true
			options.AllowWatchBookmarks = true
		}),
	)
	vpcInformer := fabricInformerFactory.Fabric().V1().Vpcs()
	subnetInformer := fabricInformerFactory.Fabric().V1().Subnets()
	ipInformer := fabricInformerFactory.Fabric().V1().IPs()
	vlanInformer := fabricInformerFactory.Fabric().V1().Vlans()
	providerNetworkInformer := fabricInformerFactory.Fabric().V1().ProviderNetworks()
	ippoolInformer := fabricInformerFactory.Fabric().V1().IPPools()
	routerLBRuleInformer := fabricInformerFactory.Fabric().V1().RouterLBRules()
	loadBalancerInformer := fabricInformerFactory.Fabric().V1().LoadBalancers()
	switchLBRuleInformer := fabricInformerFactory.Fabric().V1().SwitchLBRules()
	loadBalancerPoolInformer := fabricInformerFactory.Fabric().V1().LoadBalancerPools()
	dnsZoneInformer := fabricInformerFactory.Fabric().V1().DNSZones()
	ovnEipInformer := fabricInformerFactory.Fabric().V1().OvnEips()
	ovnDnatRuleInformer := fabricInformerFactory.Fabric().V1().OvnDnatRules()
	ovnFipInformer := fabricInformerFactory.Fabric().V1().OvnFips()
	ovnSnatRuleInformer := fabricInformerFactory.Fabric().V1().OvnSnatRules()

	fakeInformers := &fakeControllerInformers{
		vpcInformer:       vpcInformer,
		subnetInformer:    subnetInformer,
		ipInformer:        ipInformer,
		vlanInformer:      vlanInformer,
		serviceInformer:   serviceInformer,
		namespaceInformer: namespaceInformer,
		nodeInformer:      nodeInformer,
		podInformer:       podInformer,
	}

	// Create mock OVN clients
	mockCtrl := gomock.NewController(t)
	mockOvnClient := mockovs.NewMockNbClient(mockCtrl)
	mockOvnSbClient := mockovs.NewMockSbClient(mockCtrl)

	// Create controller with all informers
	ctrl := &Controller{
		servicesLister:          serviceInformer.Lister(),
		namespacesLister:        namespaceInformer.Lister(),
		nodesLister:             nodeInformer.Lister(),
		podsLister:              podInformer.Lister(),
		endpointSlicesLister:    endpointSliceInformer.Lister(),
		vpcsLister:              vpcInformer.Lister(),
		vpcSynced:               alwaysReady,
		subnetsLister:           subnetInformer.Lister(),
		subnetSynced:            alwaysReady,
		ippoolLister:            ippoolInformer.Lister(),
		ippoolSynced:            alwaysReady,
		ipsLister:               ipInformer.Lister(),
		ipSynced:                alwaysReady,
		vlansLister:             vlanInformer.Lister(),
		providerNetworksLister:  providerNetworkInformer.Lister(),
		routerLBRuleLister:      routerLBRuleInformer.Lister(),
		routerLBRuleSynced:      alwaysReady,
		loadBalancerLister:      loadBalancerInformer.Lister(),
		loadBalancerSynced:      alwaysReady,
		switchLBRuleLister:      switchLBRuleInformer.Lister(),
		switchLBRuleSynced:      alwaysReady,
		loadBalancerPoolLister:  loadBalancerPoolInformer.Lister(),
		loadBalancerPoolSynced:  alwaysReady,
		dnsZoneLister:           dnsZoneInformer.Lister(),
		dnsZoneSynced:           alwaysReady,
		dnsZoneKeyMutex:         keymutex.NewHashed(0),
		addOrUpdateDNSZoneQueue: newTypedRateLimitingQueue[string]("AddOrUpdateDNSZone", nil),
		delDNSZoneQueue:         newTypedRateLimitingQueue[string]("DeleteDNSZone", nil),
		ovnEipsLister:           ovnEipInformer.Lister(),
		ovnEipSynced:            alwaysReady,
		ovnDnatRulesLister:      ovnDnatRuleInformer.Lister(),
		ovnDnatRuleSynced:       alwaysReady,
		ovnFipsLister:           ovnFipInformer.Lister(),
		ovnFipSynced:            alwaysReady,
		ovnSnatRulesLister:      ovnSnatRuleInformer.Lister(),
		ovnSnatRuleSynced:       alwaysReady,
		netAttachLister:         nadInformer.Lister(),
		netAttachSynced:         alwaysReady,
		OVNNbClient:             mockOvnClient,
		OVNSbClient:             mockOvnSbClient,
		ipam:                    ovnipam.NewIPAM(),
		recorder:                record.NewFakeRecorder(100),
		podKeyMutex:             keymutex.NewHashed(0),
		subnetKeyMutex:          keymutex.NewHashed(0),
		nsKeyMutex:              keymutex.NewHashed(0),
		addOrUpdateSubnetQueue:  newTypedRateLimitingQueue[string]("AddOrUpdateSubnet", nil),
		syncVirtualPortsQueue:   newTypedRateLimitingQueue[string]("SyncVirtualPort", nil),
		updateSubnetStatusQueue: newTypedRateLimitingQueue[string]("UpdateSubnetStatus", nil),
	}

	ctrl.config = &Configuration{
		ClusterRouter:        util.DefaultVpc,
		DefaultLogicalSwitch: util.DefaultSubnet,
		NodeSwitch:           "join",
		FabricClient:         fabricClient,
		KubeClient:           kubeClient,
		PodNamespace:         metav1.NamespaceSystem,
		AttachNetClient:      nadClient,
	}

	if err := ctrl.setupIndexers(vpcInformer.Informer(), podInformer.Informer(), endpointSliceInformer.Informer(), ipInformer.Informer()); err != nil {
		return nil, err
	}

	// Start informers and wait for sync
	stopCh := make(chan struct{})
	t.Cleanup(func() { close(stopCh) })

	kubeInformerFactory.Start(stopCh)
	nadInformerFactory.Start(stopCh)
	fabricInformerFactory.Start(stopCh)

	kubeInformerFactory.WaitForCacheSync(stopCh)
	nadInformerFactory.WaitForCacheSync(stopCh)
	fabricInformerFactory.WaitForCacheSync(stopCh)

	return &fakeController{
		fakeController:  ctrl,
		fakeInformers:   fakeInformers,
		mockOvnClient:   mockOvnClient,
		mockOvnSbClient: mockOvnSbClient,
	}, nil
}

// newFakeController creates a basic fake controller
func newFakeController(t *testing.T) *fakeController {
	controller, err := newFakeControllerWithOptions(t, nil)
	require.NoError(t, err)
	return controller
}

func Test_allSubnetReady(t *testing.T) {
	fakeController, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		Subnets: []*fabricv1.Subnet{{
			ObjectMeta: metav1.ObjectMeta{Name: util.DefaultSubnet},
		}, {
			ObjectMeta: metav1.ObjectMeta{Name: "join"},
		}},
	})
	require.NoError(t, err)
	ctrl := fakeController.fakeController
	mockOvnClient := fakeController.mockOvnClient

	subnets := []string{util.DefaultSubnet, "join"}

	t.Run("all subnet ready", func(t *testing.T) {
		mockOvnClient.EXPECT().LogicalSwitchExists(gomock.Any()).Return(true, nil).Times(2)

		ready, err := ctrl.allSubnetReady(subnets...)
		require.NoError(t, err)
		require.True(t, ready)
	})

	t.Run("some subnets are not ready", func(t *testing.T) {
		mockOvnClient.EXPECT().LogicalSwitchExists(subnets[0]).Return(true, nil)
		mockOvnClient.EXPECT().LogicalSwitchExists(subnets[1]).Return(false, nil)

		ready, err := ctrl.allSubnetReady(subnets...)
		require.NoError(t, err)
		require.False(t, ready)
	})
}

// TestFakeControllerWithOptions demonstrates usage of the unified fake controller
func TestFakeControllerWithOptions(t *testing.T) {
	// Example: creating a fake controller with NADs, subnets, and pods
	opts := &FakeControllerOptions{
		Subnets: []*fabricv1.Subnet{{
			ObjectMeta: metav1.ObjectMeta{Name: "net1-subnet"},
			Spec:       fabricv1.SubnetSpec{CIDRBlock: "192.168.1.0/24"},
		}},
		NetworkAttachments: []*nadv1.NetworkAttachmentDefinition{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "net1",
				Namespace: metav1.NamespaceDefault,
			},
			Spec: nadv1.NetworkAttachmentDefinitionSpec{
				Config: `{"cniVersion": "0.3.1", "name": "net1", "type": "fabric"}`,
			},
		}},
		Pods: []*corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-pod",
				Namespace: metav1.NamespaceDefault,
				Annotations: map[string]string{
					nadv1.NetworkAttachmentAnnot: `[{"name": "net1"}]`,
				},
			},
		}},
	}

	fakeCtrl, err := newFakeControllerWithOptions(t, opts)
	require.NoError(t, err)
	ctrl := fakeCtrl.fakeController

	// Verify that the fake controller was created successfully
	require.NotNil(t, ctrl)
	require.NotNil(t, ctrl.config)
	require.NotNil(t, ctrl.config.AttachNetClient)
	require.NotNil(t, ctrl.config.FabricClient)

	// Verify that NADs can be retrieved
	nadClient := ctrl.config.AttachNetClient.K8sCniCncfIoV1().NetworkAttachmentDefinitions(metav1.NamespaceDefault)
	retrievedNAD, err := nadClient.Get(context.Background(), "net1", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "net1", retrievedNAD.Name)

	// Verify that subnets can be retrieved
	subnetClient := ctrl.config.FabricClient.FabricV1().Subnets()
	retrievedSubnet, err := subnetClient.Get(context.Background(), "net1-subnet", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "net1-subnet", retrievedSubnet.Name)
}
