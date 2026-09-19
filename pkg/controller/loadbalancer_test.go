package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func makeUnifiedLBEip(name, v4ip, specType, externalSubnet string) *fabricv1.OvnEip {
	return &fabricv1.OvnEip{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       fabricv1.OvnEipSpec{Type: specType, ExternalSubnet: externalSubnet},
		Status:     fabricv1.OvnEipStatus{V4Ip: v4ip, Ready: true},
	}
}

func makeUnifiedLB(name string, frontend fabricv1.LoadBalancerFrontend) *fabricv1.LoadBalancer {
	return &fabricv1.LoadBalancer{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: fabricv1.LoadBalancerSpec{
			Vpc:       "vpc1",
			Frontend:  frontend,
			Namespace: "t-abc",
			Selector:  []string{"app.kubernetes.io/instance:x"},
			Ports:     []fabricv1.LoadBalancerPort{{Name: "http", Port: 80, TargetPort: 8080, Protocol: "TCP"}},
		},
	}
}

func TestLoadBalancer_VipFrontendTranslatesToSwitchRule(t *testing.T) {
	lb := makeUnifiedLB("lb1", fabricv1.LoadBalancerFrontend{Vip: "100.69.0.10"})
	subnet := &fabricv1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "vpc1-lb"},
		Spec:       fabricv1.SubnetSpec{Vpc: "vpc1", CIDRBlock: "100.69.0.0/24"},
	}
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		LoadBalancers: []*fabricv1.LoadBalancer{lb},
		Subnets:       []*fabricv1.Subnet{subnet},
	})
	require.NoError(t, err)

	require.NoError(t, fc.fakeController.handleAddOrUpdateLoadBalancer("lb1"))

	child, err := fc.fakeController.config.FabricClient.FabricV1().SwitchLBRules().Get(context.Background(), "flb-lb1", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "100.69.0.10", child.Spec.Vip)
	assert.Equal(t, "t-abc", child.Spec.Namespace)
	assert.Empty(t, child.Annotations, "placement comes from endpoint discovery, not annotations")
	assert.Equal(t, "lb1", child.Labels[util.LoadBalancerOwnerLabel])
	require.Len(t, child.Spec.Ports, 1)
	assert.Equal(t, int32(8080), child.Spec.Ports[0].TargetPort)
}

func TestLoadBalancer_EipFrontendTranslatesToRouterRule(t *testing.T) {
	lb := makeUnifiedLB("lb2", fabricv1.LoadBalancerFrontend{OvnEip: "eip1"})
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		LoadBalancers: []*fabricv1.LoadBalancer{lb},
		OvnEips:       []*fabricv1.OvnEip{makeUnifiedLBEip("eip1", "91.246.31.5", util.OvnEipTypeNAT, "pubnet")},
	})
	require.NoError(t, err)

	require.NoError(t, fc.fakeController.handleAddOrUpdateLoadBalancer("lb2"))

	child, err := fc.fakeController.config.FabricClient.FabricV1().RouterLBRules().Get(context.Background(), "flb-lb2", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "eip1", child.Spec.OvnEip)
	assert.Equal(t, "vpc1", child.Spec.Vpc)
	assert.Equal(t, "lb2", child.Labels[util.LoadBalancerOwnerLabel])
}

func TestLoadBalancer_FrontendSwitchReplacesTheChild(t *testing.T) {
	lb := makeUnifiedLB("lb3", fabricv1.LoadBalancerFrontend{OvnEip: "eip1"})
	subnet := &fabricv1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "vpc1-lb"},
		Spec:       fabricv1.SubnetSpec{Vpc: "vpc1", CIDRBlock: "100.69.0.0/24"},
	}
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		LoadBalancers: []*fabricv1.LoadBalancer{lb},
		Subnets:       []*fabricv1.Subnet{subnet},
		OvnEips:       []*fabricv1.OvnEip{makeUnifiedLBEip("eip1", "91.246.31.5", util.OvnEipTypeNAT, "pubnet")},
	})
	require.NoError(t, err)
	require.NoError(t, fc.fakeController.handleAddOrUpdateLoadBalancer("lb3"))

	updated := lb.DeepCopy()
	updated.Spec.Frontend = fabricv1.LoadBalancerFrontend{Vip: "100.69.0.11"}
	_, err = fc.fakeController.config.FabricClient.FabricV1().LoadBalancers().Update(context.Background(), updated, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, gerr := fc.fakeController.loadBalancerLister.Get("lb3")
		return gerr == nil && got.Spec.Frontend.Vip == "100.69.0.11"
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, fc.fakeController.handleAddOrUpdateLoadBalancer("lb3"))

	_, err = fc.fakeController.config.FabricClient.FabricV1().RouterLBRules().Get(context.Background(), "flb-lb3", metav1.GetOptions{})
	assert.Error(t, err)
	child, err := fc.fakeController.config.FabricClient.FabricV1().SwitchLBRules().Get(context.Background(), "flb-lb3", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "100.69.0.11", child.Spec.Vip)
}

func TestLoadBalancer_BothOrNeitherFrontendIsRejectedWithoutChildren(t *testing.T) {
	for name, frontend := range map[string]fabricv1.LoadBalancerFrontend{
		"neither": {},
		"both":    {Vip: "100.69.0.10", OvnEip: "eip1"},
	} {
		lb := makeUnifiedLB("lb-"+name, frontend)
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			LoadBalancers: []*fabricv1.LoadBalancer{lb},
		})
		require.NoError(t, err)
		require.NoError(t, fc.fakeController.handleAddOrUpdateLoadBalancer(lb.Name))
		_, serr := fc.fakeController.config.FabricClient.FabricV1().SwitchLBRules().Get(context.Background(), lbChildName(lb.Name), metav1.GetOptions{})
		_, rerr := fc.fakeController.config.FabricClient.FabricV1().RouterLBRules().Get(context.Background(), lbChildName(lb.Name), metav1.GetOptions{})
		assert.Error(t, serr, name)
		assert.Error(t, rerr, name)
	}
}

func TestLoadBalancer_DeleteRemovesTheChildren(t *testing.T) {
	lb := makeUnifiedLB("lb5", fabricv1.LoadBalancerFrontend{OvnEip: "eip1"})
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		LoadBalancers: []*fabricv1.LoadBalancer{lb},
		OvnEips:       []*fabricv1.OvnEip{makeUnifiedLBEip("eip1", "91.246.31.5", util.OvnEipTypeNAT, "pubnet")},
	})
	require.NoError(t, err)
	require.NoError(t, fc.fakeController.handleAddOrUpdateLoadBalancer("lb5"))
	require.NoError(t, fc.fakeController.handleDelLoadBalancer("lb5"))
	_, err = fc.fakeController.config.FabricClient.FabricV1().RouterLBRules().Get(context.Background(), "flb-lb5", metav1.GetOptions{})
	assert.Error(t, err)
}
