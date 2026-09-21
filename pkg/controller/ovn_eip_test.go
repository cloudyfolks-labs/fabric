package controller

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func TestNatGatewayPort(t *testing.T) {
	routedVpc := func(name string, routing *fabricv1.VpcDynamicRouting) *fabricv1.Vpc {
		return &fabricv1.Vpc{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: fabricv1.VpcSpec{
				ExtraExternalSubnets: []string{"transit", "services"},
				DynamicRouting:       routing,
			},
		}
	}

	t.Run("routed vpc names the lrp of the external subnet", func(t *testing.T) {
		vpc := routedVpc("vpc-a", &fabricv1.VpcDynamicRouting{Enabled: true, ExternalSubnet: "transit"})
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{Vpcs: []*fabricv1.Vpc{vpc}})
		require.NoError(t, err)
		fc.mockOvnClient.EXPECT().GetLogicalRouterPort("vpc-a-transit", false).Return(&ovnnb.LogicalRouterPort{UUID: "lrp-uuid"}, nil)

		gatewayPort, err := fc.fakeController.natGatewayPort("vpc-a", "transit")
		require.NoError(t, err)
		require.Equal(t, "lrp-uuid", gatewayPort)
	})

	t.Run("nat on an external subnet with its own lrp names that lrp", func(t *testing.T) {
		vpc := routedVpc("vpc-f", &fabricv1.VpcDynamicRouting{Enabled: true, ExternalSubnet: "transit"})
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{Vpcs: []*fabricv1.Vpc{vpc}})
		require.NoError(t, err)
		fc.mockOvnClient.EXPECT().GetLogicalRouterPort("vpc-f-services", true).Return(&ovnnb.LogicalRouterPort{UUID: "services-lrp-uuid"}, nil)

		gatewayPort, err := fc.fakeController.natGatewayPort("vpc-f", "services")
		require.NoError(t, err)
		require.Equal(t, "services-lrp-uuid", gatewayPort)
	})

	t.Run("nat on a pool subnet without an lrp falls back to the bgp next hop", func(t *testing.T) {
		vpc := routedVpc("vpc-g", &fabricv1.VpcDynamicRouting{Enabled: true, ExternalSubnet: "transit"})
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{Vpcs: []*fabricv1.Vpc{vpc}})
		require.NoError(t, err)
		fc.mockOvnClient.EXPECT().GetLogicalRouterPort("vpc-g-public-pool", true).Return(nil, nil)
		fc.mockOvnClient.EXPECT().GetLogicalRouterPort("vpc-g-transit", false).Return(&ovnnb.LogicalRouterPort{UUID: "lrp-uuid"}, nil)

		gatewayPort, err := fc.fakeController.natGatewayPort("vpc-g", "public-pool")
		require.NoError(t, err)
		require.Equal(t, "lrp-uuid", gatewayPort)
	})

	t.Run("routed vpc without the lrp is retried", func(t *testing.T) {
		vpc := routedVpc("vpc-b", &fabricv1.VpcDynamicRouting{Enabled: true, ExternalSubnet: "transit"})
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{Vpcs: []*fabricv1.Vpc{vpc}})
		require.NoError(t, err)
		fc.mockOvnClient.EXPECT().GetLogicalRouterPort("vpc-b-transit", false).Return(nil, errors.New("not found"))

		_, err = fc.fakeController.natGatewayPort("vpc-b", "")
		require.Error(t, err)
	})

	t.Run("routed vpc without a named external subnet leaves the choice to northd", func(t *testing.T) {
		vpc := routedVpc("vpc-c", &fabricv1.VpcDynamicRouting{Enabled: true})
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{Vpcs: []*fabricv1.Vpc{vpc}})
		require.NoError(t, err)

		gatewayPort, err := fc.fakeController.natGatewayPort("vpc-c", "services")
		require.NoError(t, err)
		require.Empty(t, gatewayPort)
	})

	t.Run("vpc without dynamic routing leaves the choice to northd", func(t *testing.T) {
		vpc := routedVpc("vpc-d", nil)
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{Vpcs: []*fabricv1.Vpc{vpc}})
		require.NoError(t, err)

		gatewayPort, err := fc.fakeController.natGatewayPort("vpc-d", "services")
		require.NoError(t, err)
		require.Empty(t, gatewayPort)
	})

	t.Run("missing vpc is an error", func(t *testing.T) {
		fc := newFakeController(t)
		_, err := fc.fakeController.natGatewayPort("vpc-e", "")
		require.Error(t, err)
	})
}

func Test_getOvnEipNat(t *testing.T) {
	ipv6Dnat := &fabricv1.OvnDnatRule{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dnat-v6",
			Labels: map[string]string{
				util.EipV4IpLabel: "",
				util.EipV6IpLabel: util.IPv6ToLabelValue("fc00:1::a"),
			},
		},
	}
	ipv6Snat := &fabricv1.OvnSnatRule{
		ObjectMeta: metav1.ObjectMeta{
			Name: "snat-v6",
			Labels: map[string]string{
				util.EipV4IpLabel: "",
				util.EipV6IpLabel: util.IPv6ToLabelValue("fc00:1::a"),
			},
		},
	}
	ipv4Dnat := &fabricv1.OvnDnatRule{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dnat-v4",
			Labels: map[string]string{
				util.EipV4IpLabel: "192.168.0.5",
				util.EipV6IpLabel: "",
			},
		},
	}

	t.Run("pure IPv6 EIP does not match unrelated IPv6 NAT via empty v4 label", func(t *testing.T) {
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			OvnDnatRules: []*fabricv1.OvnDnatRule{ipv6Dnat},
			OvnSnatRules: []*fabricv1.OvnSnatRule{ipv6Snat},
		})
		require.NoError(t, err)
		nat, err := fc.fakeController.getOvnEipNat("", "fc00:1::7")
		require.NoError(t, err)
		require.Empty(t, nat)
	})

	t.Run("pure IPv6 EIP matches NAT rules that actually use its v6 ip", func(t *testing.T) {
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			OvnDnatRules: []*fabricv1.OvnDnatRule{ipv6Dnat},
			OvnSnatRules: []*fabricv1.OvnSnatRule{ipv6Snat},
		})
		require.NoError(t, err)
		nat, err := fc.fakeController.getOvnEipNat("", "fc00:1::a")
		require.NoError(t, err)
		require.Equal(t, util.DnatUsingEip+","+util.SnatUsingEip, nat)
	})

	t.Run("IPv4 EIP matches a NAT rule that uses its v4 ip", func(t *testing.T) {
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			OvnDnatRules: []*fabricv1.OvnDnatRule{ipv4Dnat},
		})
		require.NoError(t, err)
		nat, err := fc.fakeController.getOvnEipNat("192.168.0.5", "")
		require.NoError(t, err)
		require.Equal(t, util.DnatUsingEip, nat)
	})

	t.Run("IPv4 EIP does not match a different v4 NAT rule", func(t *testing.T) {
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			OvnDnatRules: []*fabricv1.OvnDnatRule{ipv4Dnat},
		})
		require.NoError(t, err)
		nat, err := fc.fakeController.getOvnEipNat("192.168.0.99", "")
		require.NoError(t, err)
		require.Empty(t, nat)
	})

	t.Run("EIP with no ip queries nothing", func(t *testing.T) {
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			OvnDnatRules: []*fabricv1.OvnDnatRule{ipv6Dnat},
		})
		require.NoError(t, err)
		nat, err := fc.fakeController.getOvnEipNat("", "")
		require.NoError(t, err)
		require.Empty(t, nat)
	})
}

func assertEnqueueAddRoutingWithFinalizer(
	t *testing.T,
	addQueue, updateQueue workqueue.TypedRateLimitingInterface[string],
	enqueue func(any),
	live, terminatingWithFinalizer, terminatingNoFinalizer any,
) {
	t.Helper()
	enqueue(live)
	require.Equal(t, 1, addQueue.Len(), "live object should go to the add queue")
	require.Equal(t, 0, updateQueue.Len(), "live object must not go to the update queue")

	enqueue(terminatingWithFinalizer)
	require.Equal(t, 1, addQueue.Len(), "terminating object must not go to the add queue")
	require.Equal(t, 1, updateQueue.Len(), "terminating object with finalizer should go to the update queue")

	enqueue(terminatingNoFinalizer)
	require.Equal(t, 1, addQueue.Len(), "terminating object without finalizer must not go to the add queue")
	require.Equal(t, 1, updateQueue.Len(), "terminating object without finalizer must not be re-enqueued")
}

func TestEnqueueAddOvnEip(t *testing.T) {
	t.Parallel()
	c := &Controller{
		config:            &Configuration{},
		addOvnEipQueue:    newTypedRateLimitingQueue[string]("AddOvnEip", nil),
		updateOvnEipQueue: newTypedRateLimitingQueue[string]("UpdateOvnEip", nil),
	}
	t.Cleanup(c.addOvnEipQueue.ShutDown)
	t.Cleanup(c.updateOvnEipQueue.ShutDown)
	now := metav1.Now()
	fin := []string{util.FabricControllerFinalizer}
	assertEnqueueAddRoutingWithFinalizer(t, c.addOvnEipQueue, c.updateOvnEipQueue, c.enqueueAddOvnEip,
		&fabricv1.OvnEip{ObjectMeta: metav1.ObjectMeta{Name: "live-eip"}},
		&fabricv1.OvnEip{ObjectMeta: metav1.ObjectMeta{Name: "terminating-eip", DeletionTimestamp: &now, Finalizers: fin}},
		&fabricv1.OvnEip{ObjectMeta: metav1.ObjectMeta{Name: "terminating-eip-no-finalizer", DeletionTimestamp: &now}},
	)
}

func TestEnqueueAddOvnFip(t *testing.T) {
	t.Parallel()
	c := &Controller{
		addOvnFipQueue:    newTypedRateLimitingQueue[string]("AddOvnFip", nil),
		updateOvnFipQueue: newTypedRateLimitingQueue[string]("UpdateOvnFip", nil),
	}
	t.Cleanup(c.addOvnFipQueue.ShutDown)
	t.Cleanup(c.updateOvnFipQueue.ShutDown)
	now := metav1.Now()
	fin := []string{util.FabricControllerFinalizer}
	assertEnqueueAddRoutingWithFinalizer(t, c.addOvnFipQueue, c.updateOvnFipQueue, c.enqueueAddOvnFip,
		&fabricv1.OvnFip{ObjectMeta: metav1.ObjectMeta{Name: "live-fip"}},
		&fabricv1.OvnFip{ObjectMeta: metav1.ObjectMeta{Name: "terminating-fip", DeletionTimestamp: &now, Finalizers: fin}},
		&fabricv1.OvnFip{ObjectMeta: metav1.ObjectMeta{Name: "terminating-fip-no-finalizer", DeletionTimestamp: &now}},
	)
}

func TestEnqueueAddOvnDnatRule(t *testing.T) {
	t.Parallel()
	c := &Controller{
		addOvnDnatRuleQueue:    newTypedRateLimitingQueue[string]("AddOvnDnat", nil),
		updateOvnDnatRuleQueue: newTypedRateLimitingQueue[string]("UpdateOvnDnat", nil),
	}
	t.Cleanup(c.addOvnDnatRuleQueue.ShutDown)
	t.Cleanup(c.updateOvnDnatRuleQueue.ShutDown)
	now := metav1.Now()
	fin := []string{util.FabricControllerFinalizer}
	assertEnqueueAddRoutingWithFinalizer(t, c.addOvnDnatRuleQueue, c.updateOvnDnatRuleQueue, c.enqueueAddOvnDnatRule,
		&fabricv1.OvnDnatRule{ObjectMeta: metav1.ObjectMeta{Name: "live-dnat"}},
		&fabricv1.OvnDnatRule{ObjectMeta: metav1.ObjectMeta{Name: "terminating-dnat", DeletionTimestamp: &now, Finalizers: fin}},
		&fabricv1.OvnDnatRule{ObjectMeta: metav1.ObjectMeta{Name: "terminating-dnat-no-finalizer", DeletionTimestamp: &now}},
	)
}

func TestEnqueueAddOvnSnatRule(t *testing.T) {
	t.Parallel()
	c := &Controller{
		addOvnSnatRuleQueue:    newTypedRateLimitingQueue[string]("AddOvnSnat", nil),
		updateOvnSnatRuleQueue: newTypedRateLimitingQueue[string]("UpdateOvnSnat", nil),
	}
	t.Cleanup(c.addOvnSnatRuleQueue.ShutDown)
	t.Cleanup(c.updateOvnSnatRuleQueue.ShutDown)
	now := metav1.Now()
	fin := []string{util.FabricControllerFinalizer}
	assertEnqueueAddRoutingWithFinalizer(t, c.addOvnSnatRuleQueue, c.updateOvnSnatRuleQueue, c.enqueueAddOvnSnatRule,
		&fabricv1.OvnSnatRule{ObjectMeta: metav1.ObjectMeta{Name: "live-snat"}},
		&fabricv1.OvnSnatRule{ObjectMeta: metav1.ObjectMeta{Name: "terminating-snat", DeletionTimestamp: &now, Finalizers: fin}},
		&fabricv1.OvnSnatRule{ObjectMeta: metav1.ObjectMeta{Name: "terminating-snat-no-finalizer", DeletionTimestamp: &now}},
	)
}

func TestEnqueueAddOvnEipRequeuesRouterLBRules(t *testing.T) {
	t.Parallel()
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		RouterLBRules: []*fabricv1.RouterLBRule{{
			ObjectMeta: metav1.ObjectMeta{Name: "rlr1"},
			Spec:       fabricv1.RouterLBRuleSpec{OvnEip: "eip1"},
		}},
	})
	require.NoError(t, err)
	c := fc.fakeController
	c.config.EnableLb = true
	c.addRouterLBRuleQueue = newTypedRateLimitingQueue[string]("AddRouterLBRule", nil)
	c.addOvnEipQueue = newTypedRateLimitingQueue[string]("AddOvnEip", nil)
	c.updateOvnEipQueue = newTypedRateLimitingQueue[string]("UpdateOvnEip", nil)
	t.Cleanup(c.addRouterLBRuleQueue.ShutDown)
	t.Cleanup(c.addOvnEipQueue.ShutDown)
	t.Cleanup(c.updateOvnEipQueue.ShutDown)

	now := metav1.Now()
	c.enqueueAddOvnEip(&fabricv1.OvnEip{
		ObjectMeta: metav1.ObjectMeta{Name: "eip1", DeletionTimestamp: &now, Finalizers: []string{util.FabricControllerFinalizer}},
	})

	require.Equal(t, 1, c.updateOvnEipQueue.Len(), "terminating eip with finalizer goes to the update queue")
	require.Equal(t, 0, c.addOvnEipQueue.Len(), "terminating eip must not go to the add queue")
	require.Equal(t, 1, c.addRouterLBRuleQueue.Len(), "router lb rules must be re-queued even for a terminating eip")
}
