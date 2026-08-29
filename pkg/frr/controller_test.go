package frr

import (
	"slices"
	"strings"
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

	noDevice := func(string) bool { return false }
	got := vpcAdvertisements(vpcs, eips, noDevice)

	want := []VpcAdvertisement{{VpcName: "vpc-a", TableID: 1001, LrpIP: "10.0.0.21"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("expected only the vpc with a dynamic routing spec, a vrf id and a ready lrp, got %+v", got)
	}
}

func TestVpcAdvertisementsSkipTablesOwnedByAVrfDevice(t *testing.T) {
	named := dynamicRoutingVpc("vpc-named", 1001)
	named.Spec.DynamicRouting.VrfName = "tenant-a"
	vpcs := []*kubeovnv1.Vpc{named, dynamicRoutingVpc("vpc-default-name", 1002), dynamicRoutingVpc("vpc-plain", 1003)}
	eips := []*kubeovnv1.OvnEip{
		lrpEip("vpc-named", "external", "10.0.0.21"),
		lrpEip("vpc-default-name", "external", "10.0.0.22"),
		lrpEip("vpc-plain", "external", "10.0.0.23"),
	}
	devices := map[string]bool{"tenant-a": true, "ovnvrf1002": true}

	got := vpcAdvertisements(vpcs, eips, func(name string) bool { return devices[name] })

	want := []VpcAdvertisement{{VpcName: "vpc-plain", TableID: 1003, LrpIP: "10.0.0.23"}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("expected only the vpc whose table no vrf device owns, got %+v", got)
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

func TestLrpAddressFollowsTheSingleExternalSubnet(t *testing.T) {
	eips := []*kubeovnv1.OvnEip{
		lrpEip("vpc-a", "external", "10.0.0.21"),
		lrpEip("vpc-a", "transit", "10.1.0.21"),
		lrpEip("vpc-b", "external", "10.0.0.22"),
	}

	got, err := lrpAddress(eips, dynamicRoutingVpc("vpc-a", 1001, "transit"))
	if err != nil || got != "10.1.0.21" {
		t.Errorf("expected the lrp of the extra external subnet, got %q %v", got, err)
	}

	got, err = lrpAddress(eips, dynamicRoutingVpc("vpc-b", 1002))
	if err != nil || got != "10.0.0.22" {
		t.Errorf("expected the only lrp of the vpc, got %q %v", got, err)
	}

	if _, err = lrpAddress(eips, dynamicRoutingVpc("vpc-a", 1001)); err == nil {
		t.Error("expected two gateway lrps without an explicit external subnet to be rejected")
	}

	if _, err = lrpAddress(eips, dynamicRoutingVpc("vpc-c", 1003)); err == nil {
		t.Error("expected a vpc without a ready lrp to be rejected")
	}
}

func TestLrpAddressFollowsTheNamedExternalSubnet(t *testing.T) {
	eips := []*kubeovnv1.OvnEip{
		lrpEip("vpc-a", "transit", "10.1.0.21"),
		lrpEip("vpc-a", "services", "10.2.0.21"),
		lrpEip("vpc-b", "external", "10.0.0.22"),
	}
	named := func(name string, subnet string, extra ...string) *kubeovnv1.Vpc {
		vpc := dynamicRoutingVpc(name, 1001, extra...)
		vpc.Spec.DynamicRouting.ExternalSubnet = subnet
		return vpc
	}

	got, err := lrpAddress(eips, named("vpc-a", "transit", "services", "transit"))
	if err != nil || got != "10.1.0.21" {
		t.Errorf("expected the lrp of the named external subnet, got %q %v", got, err)
	}

	got, err = lrpAddress(eips, named("vpc-a", "services", "services", "transit"))
	if err != nil || got != "10.2.0.21" {
		t.Errorf("expected the lrp of the other named external subnet, got %q %v", got, err)
	}

	_, err = lrpAddress(eips, named("vpc-a", "missing", "services", "transit"))
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("expected the missing named subnet to be reported, got %v", err)
	}

	_, err = lrpAddress(eips, named("vpc-b", "transit"))
	if err == nil || !strings.Contains(err.Error(), "transit") {
		t.Errorf("expected a named subnet without an lrp of the vpc to be reported, got %v", err)
	}

	if _, err = lrpAddress(eips, dynamicRoutingVpc("vpc-a", 1001, "services", "transit")); err == nil {
		t.Error("expected two gateway lrps without a named external subnet to be rejected")
	}
}
