package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
)

func TestHandleAddOvnSnatRule_ConvergesGatewayPortWhenReady(t *testing.T) {
	vpc := &fabricv1.Vpc{
		ObjectMeta: metav1.ObjectMeta{Name: "vpc-h"},
		Spec: fabricv1.VpcSpec{
			ExtraExternalSubnets: []string{"transit", "services"},
			DynamicRouting:       &fabricv1.VpcDynamicRouting{Enabled: true, ExternalSubnet: "transit"},
		},
	}
	eip := &fabricv1.OvnEip{
		ObjectMeta: metav1.ObjectMeta{Name: "eip-services"},
		Spec:       fabricv1.OvnEipSpec{ExternalSubnet: "services"},
		Status:     fabricv1.OvnEipStatus{V4Ip: "100.67.128.4"},
	}
	snat := &fabricv1.OvnSnatRule{
		ObjectMeta: metav1.ObjectMeta{Name: "snat-ready"},
		Spec:       fabricv1.OvnSnatRuleSpec{OvnEip: "eip-services"},
		Status: fabricv1.OvnSnatRuleStatus{
			Ready: true, Vpc: "vpc-h", V4Eip: "100.67.128.4", V4IpCidr: "10.0.0.0/8",
		},
	}
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		Vpcs: []*fabricv1.Vpc{vpc}, OvnEips: []*fabricv1.OvnEip{eip}, OvnSnatRules: []*fabricv1.OvnSnatRule{snat},
	})
	require.NoError(t, err)
	fc.mockOvnClient.EXPECT().GetLogicalRouterPort("vpc-h-services", true).Return(&ovnnb.LogicalRouterPort{UUID: "services-lrp-uuid"}, nil)
	fc.mockOvnClient.EXPECT().EnsureNatGatewayPort("vpc-h", ovnnb.NATTypeSNAT, "100.67.128.4", "10.0.0.0/8", "services-lrp-uuid").Return(nil)

	require.NoError(t, fc.fakeController.handleAddOvnSnatRule("snat-ready"))
}

func TestHandleAddOvnFip_ConvergesGatewayPortWhenReady(t *testing.T) {
	vpc := &fabricv1.Vpc{
		ObjectMeta: metav1.ObjectMeta{Name: "vpc-h"},
		Spec: fabricv1.VpcSpec{
			ExtraExternalSubnets: []string{"transit", "services"},
			DynamicRouting:       &fabricv1.VpcDynamicRouting{Enabled: true, ExternalSubnet: "transit"},
		},
	}
	eip := &fabricv1.OvnEip{
		ObjectMeta: metav1.ObjectMeta{Name: "eip-pool"},
		Spec:       fabricv1.OvnEipSpec{ExternalSubnet: "public-pool"},
		Status:     fabricv1.OvnEipStatus{V4Ip: "91.246.31.42"},
	}
	fip := &fabricv1.OvnFip{
		ObjectMeta: metav1.ObjectMeta{Name: "fip-ready"},
		Spec:       fabricv1.OvnFipSpec{OvnEip: "eip-pool"},
		Status: fabricv1.OvnFipStatus{
			Ready: true, Vpc: "vpc-h", V4Eip: "91.246.31.42", V4Ip: "10.0.0.11",
		},
	}
	fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		Vpcs: []*fabricv1.Vpc{vpc}, OvnEips: []*fabricv1.OvnEip{eip}, OvnFipRules: []*fabricv1.OvnFip{fip},
	})
	require.NoError(t, err)
	fc.mockOvnClient.EXPECT().GetLogicalRouterPort("vpc-h-public-pool", true).Return(nil, nil)
	fc.mockOvnClient.EXPECT().GetLogicalRouterPort("vpc-h-transit", false).Return(&ovnnb.LogicalRouterPort{UUID: "transit-lrp-uuid"}, nil)
	fc.mockOvnClient.EXPECT().EnsureNatGatewayPort("vpc-h", ovnnb.NATTypeDNATAndSNAT, "91.246.31.42", "10.0.0.11", "transit-lrp-uuid").Return(nil)

	require.NoError(t, fc.fakeController.handleAddOvnFip("fip-ready"))
}
