package frr

import (
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
