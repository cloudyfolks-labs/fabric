package frr

import (
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeovnv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/kubeovn/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func dynamicRoutingVpc(name string, vrfID uint32, extraExternalSubnets ...string) *kubeovnv1.Vpc {
	return &kubeovnv1.Vpc{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kubeovnv1.VpcSpec{
			EnableExternal:       true,
			ExtraExternalSubnets: extraExternalSubnets,
			DynamicRouting: &kubeovnv1.VpcDynamicRouting{
				Enabled:      true,
				VrfID:        vrfID,
				Redistribute: []kubeovnv1.RedistributeType{kubeovnv1.RedistributeNAT},
			},
		},
	}
}

func lrpEip(vpc, subnet, v4ip string) *kubeovnv1.OvnEip {
	return &kubeovnv1.OvnEip{
		ObjectMeta: metav1.ObjectMeta{Name: vpc + "-" + subnet},
		Spec:       kubeovnv1.OvnEipSpec{Type: util.OvnEipTypeLRP, ExternalSubnet: subnet},
		Status:     kubeovnv1.OvnEipStatus{V4Ip: v4ip, Ready: true},
	}
}

func TestVpcAdvertisementsNeedNoVrfDevice(t *testing.T) {
	vpcs := []*kubeovnv1.Vpc{
		dynamicRoutingVpc("vpc-a", 1001),
		dynamicRoutingVpc("vpc-no-id", 0),
		dynamicRoutingVpc("vpc-no-lrp", 1003),
		{ObjectMeta: metav1.ObjectMeta{Name: "vpc-static"}, Spec: kubeovnv1.VpcSpec{EnableExternal: true}},
	}
	eips := []*kubeovnv1.OvnEip{
		lrpEip("vpc-a", "external", "10.0.0.21"),
		lrpEip("vpc-no-id", "external", "10.0.0.22"),
		lrpEip("vpc-static", "external", "10.0.0.24"),
	}

	got := vpcAdvertisements(vpcs, eips)

	want := []VpcAdvertisement{{VpcName: "vpc-a", TableID: 1001, LrpIP: "10.0.0.21"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("expected only the vpc with a dynamic routing spec, a vrf id and a ready lrp, got %+v", got)
	}
}

func TestAdvertisedSubnetNamesIncludeNatEipSubnets(t *testing.T) {
	pools := []*kubeovnv1.LoadBalancerPool{
		{ObjectMeta: metav1.ObjectMeta{Name: "pool-bgp"}, Spec: kubeovnv1.LoadBalancerPoolSpec{Subnet: "lb-pool", Announce: kubeovnv1.LoadBalancerPoolAnnounceBGP}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pool-l2"}, Spec: kubeovnv1.LoadBalancerPoolSpec{Subnet: "external", Announce: kubeovnv1.LoadBalancerPoolAnnounceL2}},
	}
	eips := []*kubeovnv1.OvnEip{
		{ObjectMeta: metav1.ObjectMeta{Name: "eip-a"}, Spec: kubeovnv1.OvnEipSpec{Type: util.OvnEipTypeNAT, ExternalSubnet: "eip-pool"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "eip-b"}, Spec: kubeovnv1.OvnEipSpec{Type: util.OvnEipTypeNAT, ExternalSubnet: "eip-pool"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "eip-c"}, Spec: kubeovnv1.OvnEipSpec{Type: util.OvnEipTypeNAT, ExternalSubnet: "lb-pool"}},
		lrpEip("vpc-a", "external", "10.0.0.21"),
	}

	got := advertisedSubnetNames(pools, eips)

	want := []string{"eip-pool", "lb-pool"}
	if !slices.Equal(got, want) {
		t.Errorf("expected the bgp pool subnet and the nat eip subnets once each, got %v", got)
	}
}

func TestHostRouteEntriesAreIPv4HostPrefixes(t *testing.T) {
	subnets := []*kubeovnv1.Subnet{
		{ObjectMeta: metav1.ObjectMeta{Name: "eip-pool"}, Spec: kubeovnv1.SubnetSpec{CIDRBlock: "203.0.113.0/24,fd00::/64"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "lb-pool"}, Spec: kubeovnv1.SubnetSpec{CIDRBlock: "198.51.100.0/24"}},
	}

	got := hostRouteEntries(subnets)

	want := []string{"198.51.100.0/24 ge 32 le 32", "203.0.113.0/24 ge 32 le 32"}
	if !slices.Equal(got, want) {
		t.Errorf("expected sorted ipv4 host route entries, got %v", got)
	}
}
