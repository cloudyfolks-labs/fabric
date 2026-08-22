package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
	"github.com/kubeovn/kube-ovn/pkg/internal"
	"github.com/kubeovn/kube-ovn/pkg/util"
)

func makeLbSvc(namespace, name string, class *string, svcType corev1.ServiceType) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, UID: types.UID("uid-" + name)},
		Spec: corev1.ServiceSpec{
			Type:              svcType,
			LoadBalancerClass: class,
		},
	}
}

func Test_isOvnLbSvc(t *testing.T) {
	fabricClass := util.LoadBalancerClass
	otherClass := "example.com/other"

	tests := []struct {
		name         string
		svc          *corev1.Service
		defaultClass bool
		want         bool
	}{
		{
			name: "nil service",
			svc:  nil,
			want: false,
		},
		{
			name: "cluster ip service",
			svc:  makeLbSvc("ns", "svc", nil, corev1.ServiceTypeClusterIP),
			want: false,
		},
		{
			name: "fabric class",
			svc:  makeLbSvc("ns", "svc", &fabricClass, corev1.ServiceTypeLoadBalancer),
			want: true,
		},
		{
			name: "other class",
			svc:  makeLbSvc("ns", "svc", &otherClass, corev1.ServiceTypeLoadBalancer),
			want: false,
		},
		{
			name:         "other class with default claim",
			svc:          makeLbSvc("ns", "svc", &otherClass, corev1.ServiceTypeLoadBalancer),
			defaultClass: true,
			want:         false,
		},
		{
			name: "no class without default claim",
			svc:  makeLbSvc("ns", "svc", nil, corev1.ServiceTypeLoadBalancer),
			want: false,
		},
		{
			name:         "no class with default claim",
			svc:          makeLbSvc("ns", "svc", nil, corev1.ServiceTypeLoadBalancer),
			defaultClass: true,
			want:         true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isOvnLbSvc(tt.svc, tt.defaultClass))
		})
	}
}

func Test_ovnLbSvcNames(t *testing.T) {
	assert.Equal(t, "lb-abc-123", ovnLbSvcEipName(types.UID("abc-123")))
	assert.Equal(t, "ns1.svc1", ovnLbSvcLabelValue("ns1", "svc1"))

	namespace, name, ok := parseOvnLbSvcLabelValue("ns1.svc1")
	assert.True(t, ok)
	assert.Equal(t, "ns1", namespace)
	assert.Equal(t, "svc1", name)

	_, _, ok = parseOvnLbSvcLabelValue("no-separator")
	assert.False(t, ok)
}

func Test_poolAnnounceMode(t *testing.T) {
	pool := &kubeovnv1.LoadBalancerPool{}
	assert.Equal(t, kubeovnv1.LoadBalancerPoolAnnounceL2, poolAnnounceMode(pool))

	pool.Spec.Announce = kubeovnv1.LoadBalancerPoolAnnounceBGP
	assert.Equal(t, kubeovnv1.LoadBalancerPoolAnnounceBGP, poolAnnounceMode(pool))
}

func Test_servicePortsOverlap(t *testing.T) {
	tcp80 := corev1.ServicePort{Port: 80, Protocol: corev1.ProtocolTCP}
	tcp443 := corev1.ServicePort{Port: 443, Protocol: corev1.ProtocolTCP}
	udp80 := corev1.ServicePort{Port: 80, Protocol: corev1.ProtocolUDP}
	defaulted80 := corev1.ServicePort{Port: 80}

	tests := []struct {
		name string
		a    []corev1.ServicePort
		b    []corev1.ServicePort
		want bool
	}{
		{"disjoint ports", []corev1.ServicePort{tcp80}, []corev1.ServicePort{tcp443}, false},
		{"same port same protocol", []corev1.ServicePort{tcp80}, []corev1.ServicePort{tcp80}, true},
		{"same port different protocol", []corev1.ServicePort{tcp80}, []corev1.ServicePort{udp80}, false},
		{"empty protocol defaults to tcp", []corev1.ServicePort{defaulted80}, []corev1.ServicePort{tcp80}, true},
		{"empty sets", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, servicePortsOverlap(tt.a, tt.b))
		})
	}
}

func Test_splitRequestedLbIPs(t *testing.T) {
	tests := []struct {
		name       string
		annotation string
		cidrBlock  string
		wantV4     string
		wantV6     string
		wantErr    bool
	}{
		{"empty", "", "10.10.0.0/24", "", "", false},
		{"single v4", "10.10.0.5", "10.10.0.0/24", "10.10.0.5", "", false},
		{"dual stack", "10.10.0.5,fd00::5", "10.10.0.0/24,fd00::/64", "10.10.0.5", "fd00::5", false},
		{"outside cidr", "192.168.0.5", "10.10.0.0/24", "", "", true},
		{"invalid ip", "not-an-ip", "10.10.0.0/24", "", "", true},
		{"duplicate v4", "10.10.0.5,10.10.0.6", "10.10.0.0/24", "", "", true},
		{"duplicate v6", "fd00::5,fd00::6", "fd00::/64", "", "", true},
		{"whitespace tolerated", " 10.10.0.5 , ", "10.10.0.0/24", "10.10.0.5", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v4, v6, err := splitRequestedLbIPs(tt.annotation, tt.cidrBlock)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantV4, v4)
			assert.Equal(t, tt.wantV6, v6)
		})
	}
}

func Test_selectDefaultPool(t *testing.T) {
	makePool := func(name string, isDefault bool, selector *metav1.LabelSelector) *kubeovnv1.LoadBalancerPool {
		return &kubeovnv1.LoadBalancerPool{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: kubeovnv1.LoadBalancerPoolSpec{
				Subnet:          "ext",
				Default:         isDefault,
				ServiceSelector: selector,
			},
		}
	}
	teamSelector := &metav1.LabelSelector{MatchLabels: map[string]string{"team": "a"}}

	tests := []struct {
		name      string
		pools     []*kubeovnv1.LoadBalancerPool
		svcLabels map[string]string
		want      string
	}{
		{
			name:  "no pools",
			pools: nil,
			want:  "",
		},
		{
			name:  "non default ignored",
			pools: []*kubeovnv1.LoadBalancerPool{makePool("p1", false, nil)},
			want:  "",
		},
		{
			name:  "default without selector matches",
			pools: []*kubeovnv1.LoadBalancerPool{makePool("p1", true, nil)},
			want:  "p1",
		},
		{
			name:      "selector match",
			pools:     []*kubeovnv1.LoadBalancerPool{makePool("p1", true, teamSelector)},
			svcLabels: map[string]string{"team": "a"},
			want:      "p1",
		},
		{
			name:      "selector mismatch",
			pools:     []*kubeovnv1.LoadBalancerPool{makePool("p1", true, teamSelector)},
			svcLabels: map[string]string{"team": "b"},
			want:      "",
		},
		{
			name: "lowest name wins",
			pools: []*kubeovnv1.LoadBalancerPool{
				makePool("pb", true, nil),
				makePool("pa", true, nil),
			},
			want: "pa",
		},
		{
			name: "matching selector preferred over mismatch",
			pools: []*kubeovnv1.LoadBalancerPool{
				makePool("pa", true, teamSelector),
				makePool("pb", true, nil),
			},
			svcLabels: map[string]string{"team": "b"},
			want:      "pb",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := selectDefaultPool(tt.pools, tt.svcLabels)
			if tt.want == "" {
				assert.Nil(t, pool)
				return
			}
			require.NotNil(t, pool)
			assert.Equal(t, tt.want, pool.Name)
		})
	}
}

func Test_splitAnnotationIPs(t *testing.T) {
	assert.Nil(t, splitAnnotationIPs(""))
	assert.Equal(t, []string{"10.0.0.1"}, splitAnnotationIPs("10.0.0.1"))
	assert.Equal(t, []string{"10.0.0.1", "fd00::1"}, splitAnnotationIPs(" 10.0.0.1 ,fd00::1, "))
}

func Test_ovnEipIPs(t *testing.T) {
	eip := &kubeovnv1.OvnEip{}
	assert.Nil(t, ovnEipIPs(eip))

	eip.Status.V4Ip = "10.0.0.1"
	assert.Equal(t, []string{"10.0.0.1"}, ovnEipIPs(eip))

	eip.Status.V6Ip = "fd00::1"
	assert.Equal(t, []string{"10.0.0.1", "fd00::1"}, ovnEipIPs(eip))
}

func Test_bigIntToInt64(t *testing.T) {
	assert.Equal(t, int64(42), bigIntToInt64(internal.NewBigInt(42)))

	huge := internal.NewBigInt(1)
	for range 10 {
		huge = huge.Add(huge)
	}
	assert.Equal(t, int64(1024), bigIntToInt64(huge))

	overflow := internal.NewBigInt(1)
	for range 70 {
		overflow = overflow.Add(overflow)
	}
	assert.Equal(t, int64(9223372036854775807), bigIntToInt64(overflow))
}

func Test_newOvnLbSvcRelease(t *testing.T) {
	svc := makeLbSvc("ns1", "svc1", nil, corev1.ServiceTypeLoadBalancer)
	svc.Spec.Ports = []corev1.ServicePort{{Port: 80, Protocol: corev1.ProtocolTCP}}

	rel := newOvnLbSvcRelease(svc, "ovn-cluster")
	assert.Equal(t, "ns1/svc1", rel.key)
	assert.Equal(t, svc.UID, rel.uid)
	assert.Equal(t, "ovn-cluster", rel.vpc)
	assert.Nil(t, rel.vips)
	assert.Equal(t, svc.Spec.Ports, rel.ports)

	svc.Annotations = map[string]string{
		util.VpcAnnotation:              "vpc1",
		util.RouterLBRuleVipsAnnotation: "10.0.0.1,fd00::1",
	}
	rel = newOvnLbSvcRelease(svc, "ovn-cluster")
	assert.Equal(t, "vpc1", rel.vpc)
	assert.Equal(t, []string{"10.0.0.1", "fd00::1"}, rel.vips)

	svc.Annotations = map[string]string{util.LogicalRouterAnnotation: "vpc2"}
	rel = newOvnLbSvcRelease(svc, "ovn-cluster")
	assert.Equal(t, "vpc2", rel.vpc)
}

func Test_poolSubnetExhausted(t *testing.T) {
	subnet := &kubeovnv1.Subnet{
		Spec: kubeovnv1.SubnetSpec{CIDRBlock: "10.10.0.0/24"},
	}
	subnet.Status.V4AvailableIPs = internal.NewBigInt(0)
	assert.True(t, poolSubnetExhausted(subnet))

	subnet.Status.V4AvailableIPs = internal.NewBigInt(3)
	assert.False(t, poolSubnetExhausted(subnet))

	v6Subnet := &kubeovnv1.Subnet{
		Spec: kubeovnv1.SubnetSpec{CIDRBlock: "fd00::/64"},
	}
	v6Subnet.Status.V6AvailableIPs = internal.NewBigInt(0)
	assert.True(t, poolSubnetExhausted(v6Subnet))

	v6Subnet.Status.V6AvailableIPs = internal.NewBigInt(1)
	assert.False(t, poolSubnetExhausted(v6Subnet))
}

func TestVpcAdvertisesLoadBalancerVips(t *testing.T) {
	t.Parallel()

	assert.False(t, vpcAdvertisesLoadBalancerVips(&kubeovnv1.Vpc{}))
	assert.False(t, vpcAdvertisesLoadBalancerVips(&kubeovnv1.Vpc{
		Spec: kubeovnv1.VpcSpec{DynamicRouting: &kubeovnv1.VpcDynamicRouting{
			Enabled:      true,
			Redistribute: []kubeovnv1.RedistributeType{kubeovnv1.RedistributeNAT},
		}},
	}))
	assert.False(t, vpcAdvertisesLoadBalancerVips(&kubeovnv1.Vpc{
		Spec: kubeovnv1.VpcSpec{DynamicRouting: &kubeovnv1.VpcDynamicRouting{
			Redistribute: []kubeovnv1.RedistributeType{kubeovnv1.RedistributeLB},
		}},
	}))
	assert.True(t, vpcAdvertisesLoadBalancerVips(&kubeovnv1.Vpc{
		Spec: kubeovnv1.VpcSpec{DynamicRouting: &kubeovnv1.VpcDynamicRouting{
			Enabled:      true,
			Redistribute: []kubeovnv1.RedistributeType{kubeovnv1.RedistributeNAT, kubeovnv1.RedistributeLB},
		}},
	}))
}

func TestCheckPoolAnnouncePath(t *testing.T) {
	bgpVpc := &kubeovnv1.Vpc{
		ObjectMeta: metav1.ObjectMeta{Name: "vpc-bgp"},
		Spec: kubeovnv1.VpcSpec{
			EnableExternal: true,
			DynamicRouting: &kubeovnv1.VpcDynamicRouting{
				Enabled:      true,
				VrfID:        1001,
				Redistribute: []kubeovnv1.RedistributeType{kubeovnv1.RedistributeLB},
			},
		},
	}
	l2Vpc := &kubeovnv1.Vpc{ObjectMeta: metav1.ObjectMeta{Name: "vpc-l2"}}
	lrpEip := &kubeovnv1.OvnEip{
		ObjectMeta: metav1.ObjectMeta{Name: "vpc-l2-external"},
		Spec:       kubeovnv1.OvnEipSpec{Type: util.OvnEipTypeLRP, ExternalSubnet: "external"},
		Status:     kubeovnv1.OvnEipStatus{Ready: true},
	}

	fakeCtrl, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		Vpcs:    []*kubeovnv1.Vpc{bgpVpc, l2Vpc},
		OvnEips: []*kubeovnv1.OvnEip{lrpEip},
	})
	require.NoError(t, err)
	ctrl := fakeCtrl.fakeController

	bgpPool := &kubeovnv1.LoadBalancerPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-bgp"},
		Spec:       kubeovnv1.LoadBalancerPoolSpec{Subnet: "ipam-only", Announce: kubeovnv1.LoadBalancerPoolAnnounceBGP},
	}
	assert.Empty(t, ctrl.checkPoolAnnouncePath(bgpPool, "vpc-bgp"))
	assert.Contains(t, ctrl.checkPoolAnnouncePath(bgpPool, "vpc-l2"), "does not advertise loadbalancer vips")

	l2Pool := &kubeovnv1.LoadBalancerPool{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-l2"},
		Spec:       kubeovnv1.LoadBalancerPoolSpec{Subnet: "external", Announce: kubeovnv1.LoadBalancerPoolAnnounceL2},
	}
	assert.Empty(t, ctrl.checkPoolAnnouncePath(l2Pool, "vpc-l2"))
	assert.Contains(t, ctrl.checkPoolAnnouncePath(l2Pool, "vpc-bgp"), "has no ready LRP")
}
