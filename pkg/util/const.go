package util

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
)

const (
	ReservedRoutingTableIDStart = 253
	ReservedRoutingTableIDEnd   = 255

	CniTypeName = "kube-ovn"

	DeprecatedFinalizerName    = "kube-ovn-controller"
	LegacyControllerFinalizer  = "kubeovn.io/kube-ovn-controller"
	KubeOVNControllerFinalizer = "fabric.cloudyfolks.io/controller"

	AllocatedAnnotation          = "fabric.cloudyfolks.io/allocated"
	ServiceCIDRHashAnnotation    = "fabric.cloudyfolks.io/service-cidr-hash"
	RoutedAnnotation             = "fabric.cloudyfolks.io/routed"
	RoutesAnnotation             = "fabric.cloudyfolks.io/routes"
	MacAddressAnnotation         = "fabric.cloudyfolks.io/mac_address"
	IPAddressAnnotation          = "fabric.cloudyfolks.io/ip_address"
	IPFamilyAnnotation           = "fabric.cloudyfolks.io/ip_family"
	CidrAnnotation               = "fabric.cloudyfolks.io/cidr"
	GatewayAnnotation            = "fabric.cloudyfolks.io/gateway"
	IPPoolAnnotation             = "fabric.cloudyfolks.io/ip_pool"
	BgpAnnotation                = "fabric.cloudyfolks.io/bgp"
	FipFinalizer                 = "fabric.cloudyfolks.io/fip"
	VipAnnotation                = "fabric.cloudyfolks.io/vip"
	AAPsAnnotation               = "fabric.cloudyfolks.io/aaps"
	ChassisAnnotation            = "fabric.cloudyfolks.io/chassis"
	VMAnnotation                 = "fabric.cloudyfolks.io/virtualmachine"
	ActivationStrategyAnnotation = "fabric.cloudyfolks.io/activation_strategy"

	VpcEipAnnotation  = "fabric.cloudyfolks.io/vpc_eip"
	VpcDnatEPortLabel = "fabric.cloudyfolks.io/vpc_dnat_eport"
	VpcNatAnnotation  = "fabric.cloudyfolks.io/vpc_nat"
	OvnEipTypeLabel   = "fabric.cloudyfolks.io/ovn_eip_type"
	EipV4IpLabel      = "fabric.cloudyfolks.io/eip_v4_ip"
	EipV6IpLabel      = "fabric.cloudyfolks.io/eip_v6_ip"

	RouterLBRuleVipsAnnotation = "fabric.cloudyfolks.io/router_lb_vip"
	SwitchLBRuleVipsAnnotation = "fabric.cloudyfolks.io/switch_lb_vip"
	SwitchLBRuleVip            = "switch_lb_vip"
	KubeHostVMVip              = "kube_host_vm_vip"
	SwitchLBRuleSubnet         = "switch_lb_subnet"

	LoadBalancerClass              = "fabric.cloudyfolks.io/loadbalancer"
	LoadBalancerPoolAnnotation     = "lb.fabric.cloudyfolks.io/address-pool"
	LoadBalancerIPsAnnotation      = "lb.fabric.cloudyfolks.io/ips"
	LoadBalancerSharedIPAnnotation = "lb.fabric.cloudyfolks.io/allow-shared-ip"
	LoadBalancerAnnounceLabel      = "fabric.cloudyfolks.io/announce"
	LoadBalancerServiceLabel       = "fabric.cloudyfolks.io/service"

	LogicalRouterAnnotation = "fabric.cloudyfolks.io/logical_router"
	VpcAnnotation           = "fabric.cloudyfolks.io/vpc"

	Layer2ForwardAnnotationTemplate = "%s.cloudyfolks.io/layer2_forward"
	PortSecurityAnnotationTemplate  = "%s.cloudyfolks.io/port_security"
	PortVipAnnotationTemplate       = "%s.cloudyfolks.io/port_vips"
	PortSecurityAnnotation          = "fabric.cloudyfolks.io/port_security"
	NorthGatewayAnnotation          = "fabric.cloudyfolks.io/north_gateway"

	AllocatedAnnotationSuffix       = ".cloudyfolks.io/allocated"
	AllocatedAnnotationTemplate     = "%s.cloudyfolks.io/allocated"
	RoutedAnnotationTemplate        = "%s.cloudyfolks.io/routed"
	RoutesAnnotationTemplate        = "%s.cloudyfolks.io/routes"
	MacAddressAnnotationTemplate    = "%s.cloudyfolks.io/mac_address"
	IPAddressAnnotationTemplate     = "%s.cloudyfolks.io/ip_address"
	IPFamilyAnnotationTemplate      = "%s.cloudyfolks.io/ip_family"
	CidrAnnotationTemplate          = "%s.cloudyfolks.io/cidr"
	GatewayAnnotationTemplate       = "%s.cloudyfolks.io/gateway"
	IPPoolAnnotationTemplate        = "%s.cloudyfolks.io/ip_pool"
	LogicalSwitchAnnotationTemplate = "%s.cloudyfolks.io/logical_switch"
	LogicalRouterAnnotationTemplate = "%s.cloudyfolks.io/logical_router"
	VlanIDAnnotationTemplate        = "%s.cloudyfolks.io/vlan_id"
	IngressRateAnnotationTemplate   = "%s.cloudyfolks.io/ingress_rate"
	EgressRateAnnotationTemplate    = "%s.cloudyfolks.io/egress_rate"
	IngressBurstAnnotationTemplate  = "%s.cloudyfolks.io/ingress_burst"
	EgressBurstAnnotationTemplate   = "%s.cloudyfolks.io/egress_burst"
	SecurityGroupAnnotationTemplate = "%s.cloudyfolks.io/security_groups"
	DefaultRouteAnnotationTemplate  = "%s.cloudyfolks.io/default_route"
	VfRepresentorNameTemplate       = "%s.cloudyfolks.io/vf_representor"
	VfNameTemplate                  = "%s.cloudyfolks.io/vf"
	ActivationStrategyTemplate      = "%s.cloudyfolks.io/activation_strategy"
	DHCPv4OptionsAnnotationTemplate = "%s.cloudyfolks.io/dhcp-v4-options"
	DHCPv6OptionsAnnotationTemplate = "%s.cloudyfolks.io/dhcp-v6-options"

	ProviderNetworkTemplate           = "%s.cloudyfolks.io/provider_network"
	ProviderNetworkErrMessageTemplate = "%s.provider-network.cloudyfolks.io/err_mesg"
	ProviderNetworkReadyTemplate      = "%s.provider-network.cloudyfolks.io/ready"
	ProviderNetworkExcludeTemplate    = "%s.provider-network.cloudyfolks.io/exclude"
	ProviderNetworkInterfaceTemplate  = "%s.provider-network.cloudyfolks.io/interface"
	ProviderNetworkMtuTemplate        = "%s.provider-network.cloudyfolks.io/mtu"
	ProviderNetworkVlanIntTemplate    = "%s.provider-network.cloudyfolks.io/vlan_interfaces"
	MirrorControlAnnotationTemplate   = "%s.cloudyfolks.io/mirror"
	PodNicAnnotationTemplate          = "%s.cloudyfolks.io/pod_nic_type"
	VMAnnotationTemplate              = "%s.cloudyfolks.io/virtualmachine"

	ExcludeIpsAnnotation = "fabric.cloudyfolks.io/exclude_ips"

	IngressRateAnnotation  = "fabric.cloudyfolks.io/ingress_rate"
	EgressRateAnnotation   = "fabric.cloudyfolks.io/egress_rate"
	IngressBurstAnnotation = "fabric.cloudyfolks.io/ingress_burst"
	EgressBurstAnnotation  = "fabric.cloudyfolks.io/egress_burst"

	PortNameAnnotation      = "fabric.cloudyfolks.io/port_name"
	LogicalSwitchAnnotation = "fabric.cloudyfolks.io/logical_switch"

	TunnelInterfaceAnnotation = "fabric.cloudyfolks.io/tunnel_interface"
	NodeNetworksAnnotation    = "fabric.cloudyfolks.io/node_networks"

	OvsDpTypeLabel = "fabric.cloudyfolks.io/ovs_dp_type"

	VpcNameLabel                       = "fabric.cloudyfolks.io/vpc"
	SubnetNameLabel                    = "fabric.cloudyfolks.io/subnet"
	ICGatewayLabel                     = "fabric.cloudyfolks.io/ic-gw"
	ExGatewayLabel                     = "fabric.cloudyfolks.io/external-gw"
	NodeExtGwLabel                     = "fabric.cloudyfolks.io/node-ext-gw"
	IPReservedLabel                    = "fabric.cloudyfolks.io/ip_reserved"
	VpcLbLabel                         = "fabric.cloudyfolks.io/vpc_lb"
	VpcDNSNameLabel                    = "fabric.cloudyfolks.io/vpc-dns"
	NodeNameLabel                      = "fabric.cloudyfolks.io/node-name"
	NetworkPolicyLogAnnotation         = "fabric.cloudyfolks.io/enable_log"
	NetworkPolicyEnforcementAnnotation = "fabric.cloudyfolks.io/network_policy_enforcement"
	NetworkPolicyForAnnotation         = "fabric.cloudyfolks.io/network_policy_for"
	ACLActionsLogAnnotation            = "fabric.cloudyfolks.io/log_acl_actions"
	ACLLogMeterAnnotation              = "fabric.cloudyfolks.io/acl_log_meter_rate"

	GenerateHashAnnotation = "fabric.cloudyfolks.io/generate-hash"

	ServiceExternalIPFromSubnetAnnotation = "fabric.cloudyfolks.io/service_external_ip_from_subnet"
	ServiceHealthCheck                    = "fabric.cloudyfolks.io/service_health_check"

	ProtocolTCP  = "tcp"
	ProtocolUDP  = "udp"
	ProtocolSCTP = "sctp"

	NetworkTypeVlan   = "vlan"
	NetworkTypeGeneve = "geneve"
	NetworkTypeVxlan  = "vxlan"

	LoNic         = "lo"
	NodeGwNic     = "ovnext0"
	NodeGwNs      = "ovnext"
	NodeGwNsPath  = "/var/run/netns/ovnext"
	BindMountPath = "/run/netns"

	NodeNic           = "ovn0"
	NodeLspPrefix     = "node-"
	NodeAllowPriority = "3000"

	VxlanNic  = "vxlan_sys_4789"
	GeneveNic = "genev_sys_6081"

	// Allow 16384 priorities with base set to 2100.
	SecurityGroupHighestPriority = "18484"
	SecurityGroupBasePriority    = "2005"
	SecurityGroupAllowPriority   = "2004"
	SecurityGroupDropPriority    = "2003"

	// SecurityGroup API limits for priority and tier
	SecurityGroupPriorityMax = 16384
	SecurityGroupPriorityMin = 1

	SecurityGroupOvnTierBase    = 2
	SecurityGroupAPITierMinimum = 0
	SecurityGroupAPITierMaximum = 1

	IngressAllowPriority = "2001"
	IngressDefaultDrop   = "2000"

	EgressAllowPriority = "2001"
	EgressDefaultDrop   = "2000"

	AllowEWTrafficPriority = "1900"

	SubnetAllowPriority = "1001"
	DefaultDropPriority = "1000"

	GwChassisMaxPriority = 100

	// ClusterNetworkPolicy
	CnpMaxRules       = 25
	CnpMaxPriority    = 399
	CnpACLMaxPriority = 30000
	CnpMaxDomains     = 25
	CnpMaxNetworks    = 25

	AnpMaxRules        = 100
	AnpMaxPriority     = 99
	AnpACLMaxPriority  = 30000
	BanpACLMaxPriority = 1800
	AnpACLTier         = 1
	NetpolACLTier      = 2
	BanpACLTier        = 3

	DefaultMTU         = 1500
	GeneveHeaderLength = 100
	VxlanHeaderLength  = 50
	TCPIPHeaderLength  = 40
	// IPv6MinMTU is the minimum MTU required by IPv6 (RFC 8200).
	// Linux refuses to initialize inet6_dev on interfaces below this value,
	// silently dropping every IPv6 packet.
	IPv6MinMTU = 1280

	OvnProvider                         = "fabric"
	DefaultNetworkAnnotation            = "v1.multus-cni.io/default-network"
	AttachNetworkResourceNameAnnotation = "k8s.v1.cni.cncf.io/resourceName"

	SRIOVResourceName = "mellanox.com/cx5_sriov_switchdev"

	SriovNicType = "sriov"

	InterconnectionConfig  = "ovn-ic-config"
	InterconnectionSwitch  = "ts"
	VpcLbNetworkAttachment = "ovn-vpc-lb"
	VpcDNSConfig           = "vpc-dns-config"
	VpcDNSDepTemplate      = "vpc-dns-dep"

	DefaultSecurityGroupName = "default-securitygroup"

	DefaultVpc    = "ovn-cluster"
	DefaultSubnet = "ovn-default"

	NormalRouteType    = "normal"
	EcmpRouteType      = "ecmp"
	StaticRouteBfdEcmp = "ecmp_symmetric_reply"

	Vip = "vip"

	OvnEipTypeLRP = "lrp"
	OvnEipTypeLSP = "lsp"
	OvnEipTypeNAT = "nat"

	FipUsingEip  = "fip"
	SnatUsingEip = "snat"
	DnatUsingEip = "dnat"

	OvnFip = "ovn"

	GatewayRouterPolicyPriority      = 29000
	EgressGatewayDropPolicyPriority  = 29090
	EgressGatewayPolicyPriority      = 29100
	EgressGatewayLocalPolicyPriority = 29150
	NorthGatewayRoutePolicyPriority  = 29250
	U2OSubnetPolicyPriority          = 29400
	OvnICPolicyPriority              = 29500
	NodeRouterPolicyPriority         = 30000
	U2OPhysicalGatewayPolicyPriority = 30050
	U2OSameSubnetPolicyPriority      = 30060
	NodeLocalDNSPolicyPriority       = 30100
	SubnetRouterPolicyPriority       = 31000

	OffloadType = "offload-port"
	DpdkType    = "dpdk-port"
	VethType    = "veth-pair"

	MirrorsRetryMaxTimes = 5
	MirrorsRetryInterval = 1

	ChassisRetryMaxTimes           = 5
	ChassisCniDaemonRetryInterval  = 1
	ChassisControllerRetryInterval = 3

	MirrorControlAnnotation = "fabric.cloudyfolks.io/mirror"
	MirrorDefaultName       = "m0"

	DenyAllSecurityGroup = "kubeovn_deny_all"

	NetemQosLatencyAnnotation = "fabric.cloudyfolks.io/latency"
	NetemQosLimitAnnotation   = "fabric.cloudyfolks.io/limit"
	NetemQosLossAnnotation    = "fabric.cloudyfolks.io/loss"
	NetemQosJitterAnnotation  = "fabric.cloudyfolks.io/jitter"

	NetemQosLatencyAnnotationTemplate = "%s.cloudyfolks.io/latency"
	NetemQosLimitAnnotationTemplate   = "%s.cloudyfolks.io/limit"
	NetemQosLossAnnotationTemplate    = "%s.cloudyfolks.io/loss"
	NetemQosJitterAnnotationTemplate  = "%s.cloudyfolks.io/jitter"

	AttachmentProvider = "fabric.cloudyfolks.io/attachmentprovider"
	LbSvcPodImg        = "fabric.cloudyfolks.io/lb_svc_img"

	OvnICKey       = "origin"
	OvnICConnected = "connected"
	OvnICStatic    = "static"
	OvnICNone      = ""

	MatchV4Src = "ip4.src"
	MatchV4Dst = "ip4.dst"
	MatchV6Src = "ip6.src"
	MatchV6Dst = "ip6.dst"

	U2OInterconnName = "u2o-interconnection.%s.%s"
	U2OExcludeIPAg   = "%s.u2o_exclude_ip.%s"
	U2OOverlayCIDRs  = "%s.u2o_overlay_cidrs.%s"

	McastQuerierName = "mcast-querier.%s"

	DefaultServiceSessionStickinessTimeout = 10800

	OvnSubnetGatewayIptables = "ovn-subnet-gateway"

	MainRouteTable = ""

	NatPolicyRuleActionNat     = "nat"
	NatPolicyRuleActionForward = "forward"
	NatPolicyRuleIDLength      = 12

	NAT                        = "nat"
	Mangle                     = "mangle"
	Prerouting                 = "PREROUTING"
	Postrouting                = "POSTROUTING"
	Output                     = "OUTPUT"
	OvnPrerouting              = "OVN-PREROUTING"
	OvnPostrouting             = "OVN-POSTROUTING"
	OvnOutput                  = "OVN-OUTPUT"
	OvnMasquerade              = "OVN-MASQUERADE"
	OvnNatOutGoingPolicy       = "OVN-NAT-POLICY"
	OvnNatOutGoingPolicySubnet = "OVN-NAT-PSUBNET-"

	TProxyListenPort = 8102
	TProxyRouteTable = 10001

	TProxyOutputMark     = 0x90003
	TProxyOutputMask     = 0x90003
	TProxyPreroutingMark = 0x90004
	TProxyPreroutingMask = 0x90004

	HealthCheckNamedVipTemplate = "%s:%s" // ip name, health check vip

	ConsumptionKubevirt       = "kubevirt"
	VhostUserSocketVolumeName = "vhostuser-sockets"

	DefaultOVNIPSecCA       = "ovn-ipsec-ca"
	DefaultOVSCACertPath    = "/var/lib/openvswitch/pki/switchca/cacert.pem"
	DefaultOVSCACertKeyPath = "/var/lib/openvswitch/pki/switchca/private/cakey.pem"

	SignerName = "fabric.cloudyfolks.io/signer"

	UnderlaySvcLocalOpenFlowPriority = 10000
	U2OKeepSrcMacPriority            = 10001

	UnderlaySvcLocalOpenFlowCookieV4 = 0x1000
	UnderlaySvcLocalOpenFlowCookieV6 = 0x1001

	MasqueradeExternalLBAccessMac = "00:00:00:01:00:01"
	MasqueradeCheckIP             = "0.0.0.0"
)

const (
	EnvKubernetesServiceHost = "KUBERNETES_SERVICE_HOST"
	EnvKubernetesServicePort = "KUBERNETES_SERVICE_PORT"

	EnvPodName      = "POD_NAME"
	EnvPodNamespace = "POD_NAMESPACE"
	EnvPodIP        = "POD_IP"
	EnvPodIPs       = "POD_IPS"
	EnvNodeName     = "NODE_NAME"
	EnvHostIP       = "HOST_IP"
	EnvHostIPs      = "HOST_IPS"

	EnvSSLEnabled                 = "ENABLE_SSL"
	EnvKubeOVNTLSRotationInterval = "KUBE_OVN_TLS_ROTATION_INTERVAL"
	EnvGatewayName                = "GATEWAY_NAME"
)

const (
	DatabaseICNB = "OVN_IC_Northbound"
	DatabaseICSB = "OVN_IC_Southbound"
)

const (
	NBDatabasePort = int32(6641)
	SBDatabasePort = int32(6642)
	NBRaftPort     = int32(6643)
	SBRaftPort     = int32(6644)
)

const (
	ICNBDatabasePort = int32(6645)
	ICSBDatabasePort = int32(6646)
	ICNBRaftPort     = int32(6647)
	ICSBRaftPort     = int32(6648)
)

const (
	SslCACert   = "/var/run/tls/cacert"
	SslCertPath = "/var/run/tls/cert"
	SslKeyPath  = "/var/run/tls/key"
)

const (
	ContentTypeJSON     = "application/json"
	ContentTypeProtobuf = runtime.ContentTypeProtobuf
	AcceptContentTypes  = runtime.ContentTypeProtobuf + "," + "application/json"
)

// Readonly kinds of Kubernetes objects
var (
	KindNode = ObjectKind[*corev1.Node]()
	KindPod  = ObjectKind[*corev1.Pod]()

	KindDeployment  = ObjectKind[*appsv1.Deployment]()
	KindDaemonSet   = ObjectKind[*appsv1.DaemonSet]()
	KindStatefulSet = ObjectKind[*appsv1.StatefulSet]()
	KindJob         = ObjectKind[*batchv1.Job]()
	KindCronJob     = ObjectKind[*batchv1.CronJob]()

	KindIP               = ObjectKind[*kubeovnv1.IP]()
	KindLoadBalancerPool = ObjectKind[*kubeovnv1.LoadBalancerPool]()
	KindOvnEip           = ObjectKind[*kubeovnv1.OvnEip]()
	KindOvnFip           = ObjectKind[*kubeovnv1.OvnFip]()
	KindOvnDnatRule      = ObjectKind[*kubeovnv1.OvnDnatRule]()
	KindOvnSnatRule      = ObjectKind[*kubeovnv1.OvnSnatRule]()
	KindSubnet           = ObjectKind[*kubeovnv1.Subnet]()
	KindVip              = ObjectKind[*kubeovnv1.Vip]()
	KindVpc              = ObjectKind[*kubeovnv1.Vpc]()

	KindVirtualMachine                  = ObjectKind[*kubevirtv1.VirtualMachine]()
	KindVirtualMachineInstance          = ObjectKind[*kubevirtv1.VirtualMachineInstance]()
	KindVirtualMachineInstanceMigration = ObjectKind[*kubevirtv1.VirtualMachineInstanceMigration]()
)
