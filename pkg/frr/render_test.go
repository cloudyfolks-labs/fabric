package frr

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeovnv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/kubeovn/v1"
)

func TestRenderEmpty(t *testing.T) {
	config := Render(RenderInput{NodeName: "node1"})
	if !strings.Contains(config, "hostname node1") {
		t.Errorf("expected hostname line, got:\n%s", config)
	}
	if strings.Contains(config, "router bgp") {
		t.Errorf("expected no bgp instance, got:\n%s", config)
	}
	if !strings.Contains(config, "ip protocol bgp route-map KUBE-OVN-NO-FIB") {
		t.Errorf("expected no-fib guard, got:\n%s", config)
	}
}

func TestRenderFull(t *testing.T) {
	input := RenderInput{
		NodeName: "node1",
		RouterID: "172.19.0.2",
		LocalASN: 65002,
		Neighbors: []Neighbor{
			{Address: "172.19.0.4", ASN: 65001, BFD: true},
			{Address: "172.19.0.5", ASN: 65003, Password: "secret"},
		},
		AdvertiseFilter: []string{"91.246.31.0/24 ge 32 le 32"},
		HoldTime:        30,
		KeepaliveTime:   10,
		Vpcs: []VpcAdvertisement{
			{VpcName: "vpc-b", TableID: 1002, LrpIP: "172.19.0.24"},
			{VpcName: "vpc-a", TableID: 1001, LrpIP: "172.19.0.21"},
		},
	}
	want := `frr defaults traditional
hostname node1
!
router bgp 65002
 bgp router-id 172.19.0.2
 no bgp ebgp-requires-policy
 neighbor 172.19.0.4 remote-as 65001
 neighbor 172.19.0.4 timers 10 30
 neighbor 172.19.0.4 bfd
 neighbor 172.19.0.5 remote-as 65003
 neighbor 172.19.0.5 password secret
 neighbor 172.19.0.5 timers 10 30
 address-family ipv4 unicast
  neighbor 172.19.0.4 activate
  neighbor 172.19.0.4 route-map KUBE-OVN-OUT out
  neighbor 172.19.0.5 activate
  neighbor 172.19.0.5 route-map KUBE-OVN-OUT out
  redistribute table-direct 1001 route-map KUBE-OVN-NH-vpc-a
  redistribute table-direct 1002 route-map KUBE-OVN-NH-vpc-b
 exit-address-family
!
route-map KUBE-OVN-NH-vpc-a permit 10
 set ip next-hop 172.19.0.21
exit
!
route-map KUBE-OVN-NH-vpc-b permit 10
 set ip next-hop 172.19.0.24
exit
!
ip prefix-list KUBE-OVN-ADVERTISE seq 5 permit 91.246.31.0/24 ge 32 le 32
route-map KUBE-OVN-OUT permit 10
 match ip address prefix-list KUBE-OVN-ADVERTISE
exit
!
route-map KUBE-OVN-NO-FIB deny 10
exit
!
ip protocol bgp route-map KUBE-OVN-NO-FIB
`
	if got := Render(input); got != want {
		t.Errorf("rendered config differs from the expected shape\nwant:\n%s\ngot:\n%s", want, got)
	}
}

func TestRenderSingleBgpInstance(t *testing.T) {
	config := Render(RenderInput{
		NodeName:  "node1",
		RouterID:  "10.0.0.1",
		LocalASN:  65002,
		Neighbors: []Neighbor{{Address: "10.0.0.9", ASN: 65001}},
		Vpcs: []VpcAdvertisement{
			{VpcName: "vpc-a", TableID: 1001, LrpIP: "10.0.0.21"},
			{VpcName: "vpc-b", TableID: 1002, LrpIP: "10.0.0.22"},
		},
	})

	if got := strings.Count(config, "router bgp "); got != 1 {
		t.Errorf("expected exactly one bgp instance, got %d:\n%s", got, config)
	}
	for _, forbidden := range []string{" vrf ", "import vrf", "redistribute kernel"} {
		if strings.Contains(config, forbidden) {
			t.Errorf("expected no per-vrf instance or vrf import, found %q:\n%s", forbidden, config)
		}
	}
}

func TestRenderDeterministic(t *testing.T) {
	input := RenderInput{
		NodeName: "node1",
		RouterID: "10.0.0.1",
		LocalASN: 65002,
		Vpcs: []VpcAdvertisement{
			{VpcName: "z", TableID: 3, LrpIP: "10.0.0.3"},
			{VpcName: "a", TableID: 1, LrpIP: "10.0.0.1"},
			{VpcName: "m", TableID: 2, LrpIP: "10.0.0.2"},
		},
	}
	first := Render(input)
	for range 10 {
		if got := Render(input); got != first {
			t.Fatal("render output is not deterministic")
		}
	}
}

func TestRenderAcceptsRoutesFromPeer(t *testing.T) {
	config := Render(RenderInput{
		NodeName:  "node1",
		RouterID:  "10.0.0.1",
		LocalASN:  65002,
		Neighbors: []Neighbor{{Address: "10.0.0.9", ASN: 65001}},
		Vpcs:      []VpcAdvertisement{{VpcName: "vpc-a", TableID: 1001, LrpIP: "10.0.0.21"}},
	})

	if !strings.Contains(config, " no bgp ebgp-requires-policy\n") {
		t.Errorf("expected inbound prefixes to be permitted, got:\n%s", config)
	}
	if strings.Contains(config, "neighbor 10.0.0.9 route-map ") && strings.Contains(config, " in\n") {
		t.Errorf("expected no inbound route-map, got:\n%s", config)
	}
}

func TestRenderAdvertisesWithLrpNextHop(t *testing.T) {
	config := Render(RenderInput{
		NodeName:  "node1",
		RouterID:  "10.0.0.1",
		LocalASN:  65002,
		Neighbors: []Neighbor{{Address: "10.0.0.9", ASN: 65001}},
		Vpcs:      []VpcAdvertisement{{VpcName: "vpc-a", TableID: 1001, LrpIP: "10.0.0.21"}},
	})

	if strings.Contains(config, "network ") {
		t.Errorf("expected no origin networks, every advertised prefix comes from the vrf table:\n%s", config)
	}
	if !strings.Contains(config, "  redistribute table-direct 1001 route-map KUBE-OVN-NH-vpc-a\n") {
		t.Errorf("expected the vrf table routes to carry the next-hop route-map:\n%s", config)
	}
	if !strings.Contains(config, "route-map KUBE-OVN-NH-vpc-a permit 10\n set ip next-hop 10.0.0.21\n") {
		t.Errorf("expected the lrp address as next hop:\n%s", config)
	}
}

func TestRenderNoFilterPermitsAll(t *testing.T) {
	config := Render(RenderInput{
		NodeName:  "node1",
		RouterID:  "10.0.0.1",
		LocalASN:  65002,
		Neighbors: []Neighbor{{Address: "10.0.0.9", ASN: 65001}},
	})
	if strings.Contains(config, "ip prefix-list") {
		t.Errorf("expected no prefix-list, got:\n%s", config)
	}
	if !strings.Contains(config, "route-map KUBE-OVN-OUT permit 10") {
		t.Errorf("expected permit-all out route-map, got:\n%s", config)
	}
	if strings.Contains(config, "match ip address prefix-list") {
		t.Errorf("expected no match clause, got:\n%s", config)
	}
}

func TestBuildRenderInput(t *testing.T) {
	conf := &kubeovnv1.BgpConf{
		Spec: kubeovnv1.BgpConfSpec{
			LocalASN:   65002,
			PeerASN:    65001,
			Neighbours: []string{"10.0.0.9"},
			Peers: []kubeovnv1.BgpPeer{
				{Address: "10.0.0.10", BFD: true},
				{Address: "10.0.0.11", ASN: 65500},
			},
			HoldTime:        metav1.Duration{Duration: 30e9},
			AdvertiseFilter: []string{"192.0.2.0/24 le 32"},
		},
	}
	input := BuildRenderInput(conf, "node1", "10.0.0.1", nil)

	if len(input.Neighbors) != 3 {
		t.Fatalf("expected 3 neighbors, got %d", len(input.Neighbors))
	}
	if input.Neighbors[0].Address != "10.0.0.9" || input.Neighbors[0].ASN != 65001 {
		t.Errorf("legacy neighbour not mapped: %+v", input.Neighbors[0])
	}
	if input.Neighbors[1].Address != "10.0.0.10" || input.Neighbors[1].ASN != 65001 || !input.Neighbors[1].BFD {
		t.Errorf("structured neighbor not mapped: %+v", input.Neighbors[1])
	}
	if input.Neighbors[2].ASN != 65500 {
		t.Errorf("per-neighbor asn not honored: %+v", input.Neighbors[2])
	}
	if input.HoldTime != 30 {
		t.Errorf("hold time not mapped: %d", input.HoldTime)
	}
	if input.KeepaliveTime != 10 {
		t.Errorf("keepalive time not derived from hold time: %d", input.KeepaliveTime)
	}
}

func TestValidateRenderInput(t *testing.T) {
	valid := RenderInput{
		RouterID:        "10.0.0.1",
		LocalASN:        65002,
		Neighbors:       []Neighbor{{Address: "10.0.0.9", ASN: 65001, Password: "secret"}},
		AdvertiseFilter: []string{"192.0.2.0/24 ge 32 le 32"},
		Vpcs:            []VpcAdvertisement{{VpcName: "vpc-a", TableID: 65535, LrpIP: "10.0.0.21"}},
	}
	if err := ValidateRenderInput(valid); err != nil {
		t.Errorf("expected valid input to pass, got %v", err)
	}

	cases := map[string]func(*RenderInput){
		"router id not ipv4":    func(in *RenderInput) { in.RouterID = "fd00::1" },
		"router id garbage":     func(in *RenderInput) { in.RouterID = "not-an-ip" },
		"neighbor not an ip":    func(in *RenderInput) { in.Neighbors[0].Address = "10.0.0.9\nrouter bgp 65000" },
		"neighbor asn zero":     func(in *RenderInput) { in.Neighbors[0].ASN = 0 },
		"password newline":      func(in *RenderInput) { in.Neighbors[0].Password = "secret\nrouter bgp 65000 vrf x" },
		"filter incomplete":     func(in *RenderInput) { in.AdvertiseFilter[0] = "192.0.2.0/24 ge" },
		"filter not a prefix":   func(in *RenderInput) { in.AdvertiseFilter[0] = "bogus ge 32" },
		"filter bad keyword":    func(in *RenderInput) { in.AdvertiseFilter[0] = "192.0.2.0/24 gt 32" },
		"filter injected line":  func(in *RenderInput) { in.AdvertiseFilter[0] = "192.0.2.0/24\nge 32" },
		"filter bad length":     func(in *RenderInput) { in.AdvertiseFilter[0] = "192.0.2.0/24 ge 300" },
		"lrp address missing":   func(in *RenderInput) { in.Vpcs[0].LrpIP = "" },
		"table id zero":         func(in *RenderInput) { in.Vpcs[0].TableID = 0 },
		"table id above 65535":  func(in *RenderInput) { in.Vpcs[0].TableID = 65536 },
		"lrp address not an ip": func(in *RenderInput) { in.Vpcs[0].LrpIP = "bogus" },
		"neighbor is ipv6":      func(in *RenderInput) { in.Neighbors[0].Address = "fd00::9" },
		"lrp address is ipv6":   func(in *RenderInput) { in.Vpcs[0].LrpIP = "fd00::21" },
		"filter is ipv6":        func(in *RenderInput) { in.AdvertiseFilter[0] = "fd00::/64 le 128" },
	}
	for name, mutate := range cases {
		in := valid
		in.Neighbors = []Neighbor{valid.Neighbors[0]}
		in.AdvertiseFilter = []string{valid.AdvertiseFilter[0]}
		in.Vpcs = []VpcAdvertisement{valid.Vpcs[0]}
		mutate(&in)
		if err := ValidateRenderInput(in); err == nil {
			t.Errorf("case %q: expected a validation error", name)
		}
	}
}

func TestNodeApplyState(t *testing.T) {
	state, message := nodeApplyState("abc", ApplyStatus{AppliedSerial: "abc"})
	if state != kubeovnv1.BgpNodeStateApplied || message != "" {
		t.Errorf("expected an applied state, got %q %q", state, message)
	}

	state, message = nodeApplyState("abc", ApplyStatus{ResultSerial: "abc", ResultState: "error", Detail: "error abc reload"})
	if state != kubeovnv1.BgpNodeStateFailed || message != "error abc reload" {
		t.Errorf("expected a failed state, got %q %q", state, message)
	}

	if state, _ = nodeApplyState("abc", ApplyStatus{AppliedSerial: "old"}); state != kubeovnv1.BgpNodeStatePending {
		t.Errorf("expected a pending state, got %q", state)
	}
}
