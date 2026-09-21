package util

import (
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
)

func ValidateSubnet(subnet fabricv1.Subnet) error {
	isUnderlayWithoutCIDR := subnet.Spec.Vlan != "" && subnet.Spec.CIDRBlock == ""

	if isUnderlayWithoutCIDR {
		if err := validateMacOnlySubnet(subnet); err != nil {
			return err
		}
	} else if err := validateSubnetCIDR(subnet); err != nil {
		return err
	}

	excludeIps := subnet.Spec.ExcludeIps
	for _, ipr := range excludeIps {
		if ContainsUppercase(ipr) {
			err := fmt.Errorf("subnet exclude ip %s can not contain upper case", ipr)
			return err
		}
		ips := strings.Split(ipr, "..")
		if len(ips) > 2 {
			return fmt.Errorf("%s in excludeIps is not a valid ip range", ipr)
		}

		if len(ips) == 1 {
			if net.ParseIP(ips[0]) == nil {
				return fmt.Errorf("ip %s in excludeIps is not a valid address", ips[0])
			}
		}

		if len(ips) == 2 {
			for _, ip := range ips {
				if net.ParseIP(ip) == nil {
					return fmt.Errorf("ip %s in excludeIps is not a valid address", ip)
				}
			}
			if IP2BigInt(ips[0]).Cmp(IP2BigInt(ips[1])) == 1 {
				return fmt.Errorf("%s in excludeIps is not a valid ip range", ipr)
			}
		}
	}

	if !isUnderlayWithoutCIDR {
		if err := validateSubnetCIDRBlocks(subnet); err != nil {
			return err
		}
	}

	allow := subnet.Spec.AllowSubnets
	for _, cidr := range allow {
		if ContainsUppercase(cidr) {
			err := fmt.Errorf("subnet %s allow subnet %s v6 ip address can not contain upper case", subnet.Name, cidr)
			klog.Error(err)
			return err
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			klog.Error(err)
			return fmt.Errorf("%s in allowSubnets is not a valid address", cidr)
		}
	}

	gwType := subnet.Spec.GatewayType
	if gwType != "" && gwType != fabricv1.GWDistributedType && gwType != fabricv1.GWCentralizedType {
		return fmt.Errorf("%s is not a valid gateway type", gwType)
	}

	protocol := subnet.Spec.Protocol
	if protocol != "" && protocol != fabricv1.ProtocolIPv4 &&
		protocol != fabricv1.ProtocolIPv6 &&
		protocol != fabricv1.ProtocolDual &&
		protocol != fabricv1.ProtocolMac {
		return fmt.Errorf("%s is not a valid protocol type", protocol)
	}

	if subnet.Spec.Mtu > 0 && subnet.Spec.Mtu < IPv6MinMTU {
		cidrProtocol := CheckProtocol(subnet.Spec.CIDRBlock)
		if cidrProtocol == fabricv1.ProtocolIPv6 || cidrProtocol == fabricv1.ProtocolDual {
			return fmt.Errorf("subnet %s mtu %d is below the IPv6 minimum %d", subnet.Name, subnet.Spec.Mtu, IPv6MinMTU)
		}
	}

	if subnet.Spec.Vpc == subnet.Name {
		return fmt.Errorf("subnet %s and vpc %s cannot have the same name", subnet.Name, subnet.Spec.Vpc)
	}

	if !isUnderlayWithoutCIDR && subnet.Spec.Vpc == DefaultVpc {
		k8sAPIServer := os.Getenv(EnvKubernetesServiceHost)
		if k8sAPIServer != "" && CIDRContainIP(subnet.Spec.CIDRBlock, k8sAPIServer) {
			return fmt.Errorf("subnet %s cidr %s conflicts with k8s apiserver svc ip %s", subnet.Name, subnet.Spec.CIDRBlock, k8sAPIServer)
		}
	}

	if egw := subnet.Spec.ExternalEgressGateway; egw != "" {
		if subnet.Spec.NatOutgoing {
			return errors.New("conflict configuration: natOutgoing and externalEgressGateway")
		}

		if ContainsUppercase(egw) {
			err := fmt.Errorf("subnet %s external egress gateway %s v6 ip address can not contain upper case", subnet.Name, egw)
			klog.Error(err)
			return err
		}
		ips := strings.Split(egw, ",")
		if len(ips) > 2 {
			return errors.New("invalid external egress gateway configuration")
		}
		for _, ip := range ips {
			if net.ParseIP(ip) == nil {
				return fmt.Errorf("IP %s in externalEgressGateway is not a valid address", ip)
			}
		}
		egwProtocol, cidrProtocol := CheckProtocol(egw), CheckProtocol(subnet.Spec.CIDRBlock)
		if !isUnderlayWithoutCIDR && egwProtocol != cidrProtocol && cidrProtocol != fabricv1.ProtocolDual {
			return errors.New("invalid external egress gateway configuration: address family is conflict with CIDR")
		}
	}

	if len(subnet.Spec.Vips) != 0 {
		if isUnderlayWithoutCIDR {
			return fmt.Errorf("vips are not supported for underlay subnet %s without cidrBlock", subnet.Name)
		}
		for _, vip := range subnet.Spec.Vips {
			if ContainsUppercase(vip) {
				err := fmt.Errorf("subnet %s vips %s v6 ip address can not contain upper case", subnet.Name, vip)
				klog.Error(err)
				return err
			}
			if !CIDRContainIP(subnet.Spec.CIDRBlock, vip) {
				return fmt.Errorf("vip %s conflicts with subnet %s cidr %s", vip, subnet.Name, subnet.Spec.CIDRBlock)
			}
		}
	}

	if subnet.Spec.LogicalGateway && subnet.Spec.U2OInterconnection {
		return errors.New("logicalGateway and u2oInterconnection can't be opened at the same time")
	}

	if subnet.Spec.U2OFeatures.OverlayOnlyRouting {
		if !subnet.Spec.U2OInterconnection {
			return errors.New("u2oFeatures.overlayOnlyRouting can only be enabled when u2OInterconnection is true")
		}
		if subnet.Spec.Vlan == "" {
			return errors.New("u2oFeatures.overlayOnlyRouting can only be enabled on underlay subnets (vlan must be set)")
		}
	}

	if len(subnet.Spec.NatOutgoingPolicyRules) != 0 {
		if err := validateNatOutgoingPolicyRules(subnet); err != nil {
			klog.Error(err)
			return err
		}
	}

	if subnet.Spec.U2OInterconnectionIP != "" {
		if isUnderlayWithoutCIDR {
			return fmt.Errorf("u2oInterconnectionIP is not supported for underlay subnet %s without cidrBlock", subnet.Name)
		}

		if ContainsUppercase(subnet.Spec.U2OInterconnectionIP) {
			err := fmt.Errorf("subnet %s U2O interconnection ip %s v6 ip address can not contain upper case", subnet.Name, subnet.Spec.U2OInterconnectionIP)
			klog.Error(err)
			return err
		}
		if !CIDRContainIP(subnet.Spec.CIDRBlock, subnet.Spec.U2OInterconnectionIP) {
			return fmt.Errorf("u2oInterconnectionIP %s is not in subnet %s cidr %s",
				subnet.Spec.U2OInterconnectionIP,
				subnet.Name, subnet.Spec.CIDRBlock)
		}
		gatewayV4, gatewayV6 := SplitStringIP(subnet.Spec.Gateway)
		u2oV4, u2oV6 := SplitStringIP(subnet.Spec.U2OInterconnectionIP)
		gatewayV4IP, gatewayV6IP := net.ParseIP(gatewayV4), net.ParseIP(gatewayV6)
		u2oV4IP, u2oV6IP := net.ParseIP(u2oV4), net.ParseIP(u2oV6)
		if (u2oV4IP != nil && gatewayV4IP != nil && u2oV4IP.Equal(gatewayV4IP)) ||
			(u2oV6IP != nil && gatewayV6IP != nil && u2oV6IP.Equal(gatewayV6IP)) {
			return fmt.Errorf("u2oInterconnectionIP %s conflicts with subnet gateway %s",
				subnet.Spec.U2OInterconnectionIP, subnet.Spec.Gateway)
		}
	}

	return nil
}

func validateMacOnlySubnet(subnet fabricv1.Subnet) error {
	if subnet.Spec.Gateway != "" {
		return fmt.Errorf("gateway must be empty for underlay subnet %s without cidrBlock", subnet.Name)
	}

	if len(subnet.Spec.ExcludeIps) != 0 {
		return fmt.Errorf("excludeIps must be empty for underlay subnet %s without cidrBlock", subnet.Name)
	}
	return nil
}

func validateSubnetCIDR(subnet fabricv1.Subnet) error {
	if subnet.Spec.Gateway != "" {
		if ContainsUppercase(subnet.Spec.Gateway) {
			err := fmt.Errorf("subnet gateway %s v6 ip address can not contain upper case", subnet.Spec.Gateway)
			klog.Error(err)
			return err
		}
		if !CIDRContainIP(subnet.Spec.CIDRBlock, subnet.Spec.Gateway) {
			return fmt.Errorf("gateway %s is not in cidr %s", subnet.Spec.Gateway, subnet.Spec.CIDRBlock)
		}
		if err := ValidateNetworkBroadcast(subnet.Spec.CIDRBlock, subnet.Spec.Gateway); err != nil {
			klog.Error(err)
			return fmt.Errorf("validate gateway %s for cidr %s failed: %w", subnet.Spec.Gateway, subnet.Spec.CIDRBlock, err)
		}
	}

	if err := CIDRGlobalUnicast(subnet.Spec.CIDRBlock); err != nil {
		klog.Error(err)
		return err
	}
	if CheckProtocol(subnet.Spec.CIDRBlock) == "" {
		return fmt.Errorf("CIDRBlock: %q format error", subnet.Spec.CIDRBlock)
	}
	return nil
}

func validateSubnetCIDRBlocks(subnet fabricv1.Subnet) error {
	for cidr := range strings.SplitSeq(subnet.Spec.CIDRBlock, ",") {
		if ContainsUppercase(subnet.Spec.CIDRBlock) {
			err := fmt.Errorf("subnet cidr block %s v6 ip address can not contain upper case", subnet.Spec.CIDRBlock)
			klog.Error(err)
			return err
		}
		if err := InvalidSpecialCIDR(cidr); err != nil {
			klog.Errorf("invalid subnet %s cidr %s, %s", subnet.Name, cidr, err)
			return err
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			err = fmt.Errorf("subnet %s cidr %s is invalid, due to %w", subnet.Name, cidr, err)
			klog.Error(err)
			return err
		}

		if err = InvalidNetworkMask(network); err != nil {
			err = fmt.Errorf("subnet %s cidr %s mask is invalid, due to %w", subnet.Name, cidr, err)
			klog.Error(err)
			return err
		}
	}
	return nil
}

func validateNatOutgoingPolicyRules(subnet fabricv1.Subnet) error {
	for _, rule := range subnet.Spec.NatOutgoingPolicyRules {
		var srcProtocol, dstProtocol string
		var err error

		if rule.Match.SrcIPs != "" {
			if srcProtocol, err = validateNatOutgoingPolicyRuleIPs(rule.Match.SrcIPs); err != nil {
				klog.Error(err)
				return fmt.Errorf("validate nat policy rules src ips %s failed with err %w", rule.Match.SrcIPs, err)
			}
		}
		if rule.Match.DstIPs != "" {
			if dstProtocol, err = validateNatOutgoingPolicyRuleIPs(rule.Match.DstIPs); err != nil {
				klog.Error(err)
				return fmt.Errorf("validate nat policy rules dst ips %s failed with err %w", rule.Match.DstIPs, err)
			}
		}

		if srcProtocol != "" && dstProtocol != "" && srcProtocol != dstProtocol {
			return fmt.Errorf("Match.SrcIPS protocol %s not equal to Match.DstIPs protocol %s", srcProtocol, dstProtocol)
		}
	}
	return nil
}

func validateNatOutgoingPolicyRuleIPs(matchIPStr string) (string, error) {
	if matchIPStr = strings.TrimSpace(matchIPStr); matchIPStr == "" {
		return "", errors.New("IPStr should not be empty")
	}

	ipItems := strings.Split(matchIPStr, ",")
	lastProtocol := ""
	checkProtocolConsistent := func(ipCidr string) bool {
		currentProtocol := CheckProtocol(ipCidr)
		if lastProtocol != "" && lastProtocol != currentProtocol {
			return false
		}
		lastProtocol = currentProtocol
		return true
	}

	for _, ipItem := range ipItems {
		_, ipCidr, err := net.ParseCIDR(ipItem)
		if err == nil {
			if !checkProtocolConsistent(ipCidr.String()) {
				return "", fmt.Errorf("match ips %s protocol is not consistent", matchIPStr)
			}
			continue
		}

		if IsValidIP(ipItem) {
			if !checkProtocolConsistent(ipItem) {
				return "", fmt.Errorf("match ips %s protocol is not consistent", matchIPStr)
			}
			continue
		}

		return "", fmt.Errorf("match ips %s is not ip or ipcidr", matchIPStr)
	}
	return lastProtocol, nil
}

func ValidatePodNetwork(annotations map[string]string) error {
	errors := []error{}

	for key, family := range annotations {
		if !isIPFamilyAnnotationKey(key) || family == "" {
			continue
		}
		if family != strings.ToLower(fabricv1.ProtocolIPv4) && family != strings.ToLower(fabricv1.ProtocolIPv6) {
			errors = append(errors, fmt.Errorf("%s is not a valid %s", family, key))
			continue
		}
		normalizedFamily := NormalizeIPFamily(family)
		for _, ipAddress := range ipAddressAnnotationsForIPFamily(annotations, key) {
			for ip := range strings.SplitSeq(ipAddress, ",") {
				if CheckProtocol(ip) != normalizedFamily {
					errors = append(errors, fmt.Errorf("%s does not match %s %s", ip, key, family))
				}
			}
		}
	}

	if ipAddress := annotations[IPAddressAnnotation]; ipAddress != "" {
		for ip := range strings.SplitSeq(ipAddress, ",") {
			if strings.Contains(ip, "/") {
				if _, _, err := net.ParseCIDR(ip); err != nil {
					klog.Error(err)
					errors = append(errors, fmt.Errorf("%s is not a valid %s", ip, IPAddressAnnotation))
					continue
				}
			} else {
				if net.ParseIP(ip) == nil {
					errors = append(errors, fmt.Errorf("%s is not a valid %s", ip, IPAddressAnnotation))
					continue
				}
			}

			if cidrStr := annotations[CidrAnnotation]; cidrStr != "" {
				if err := CheckCidrs(cidrStr); err != nil {
					klog.Error(err)
					errors = append(errors, fmt.Errorf("invalid cidr %s", cidrStr))
					continue
				}

				if !CIDRContainIP(cidrStr, ip) {
					errors = append(errors, fmt.Errorf("%s not in cidr %s", ip, cidrStr))
					continue
				}
			}
		}
	}

	mac := annotations[MacAddressAnnotation]
	if mac != "" {
		if _, err := net.ParseMAC(mac); err != nil {
			klog.Error(err)
			errors = append(errors, fmt.Errorf("%s is not a valid %s", mac, MacAddressAnnotation))
		}
	}

	ipPool := annotations[IPPoolAnnotation]
	if ipPool != "" {
		if strings.ContainsRune(ipPool, ';') || strings.ContainsRune(ipPool, ',') || net.ParseIP(ipPool) != nil {
			for ips := range strings.SplitSeq(ipPool, ";") {
				found := false
				for ip := range strings.SplitSeq(ips, ",") {
					if net.ParseIP(strings.TrimSpace(ip)) == nil {
						errors = append(errors, fmt.Errorf("%s in %s is not a valid address", ip, IPPoolAnnotation))
					}

					if cidrStr := annotations[CidrAnnotation]; cidrStr != "" {
						if CIDRContainIP(cidrStr, ip) {
							found = true
							break
						}
					} else {
						found = true
						break
					}
				}

				if !found {
					errors = append(errors, fmt.Errorf("%s not in cidr %s", ips, annotations[CidrAnnotation]))
					continue
				}
			}
		}
	}

	bandwidthSuffixes := []string{
		".cloudyfolks.io/ingress_rate",
		".cloudyfolks.io/egress_rate",
		".cloudyfolks.io/ingress_burst",
		".cloudyfolks.io/egress_burst",
	}
	for k, v := range annotations {
		if v == "" {
			continue
		}
		matched := false
		for _, s := range bandwidthSuffixes {
			if strings.HasSuffix(k, s) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			errors = append(errors, fmt.Errorf("%s is not a valid %s", v, k))
		}
	}

	return utilerrors.NewAggregate(errors)
}

func isIPFamilyAnnotationKey(key string) bool {
	return key == IPFamilyAnnotation || strings.HasSuffix(key, ".cloudyfolks.io/ip_family")
}

func ipAddressAnnotationKeyForIPFamily(key string) string {
	if key == IPFamilyAnnotation {
		return IPAddressAnnotation
	}
	return strings.TrimSuffix(key, "/ip_family") + "/ip_address"
}

func ipAddressAnnotationsForIPFamily(annotations map[string]string, key string) []string {
	ipAddressKeys := ipAddressAnnotationKeysForIPFamily(key)
	ipAddresses := []string{}
	for annotationKey, ipAddress := range annotations {
		if ipAddress == "" {
			continue
		}
		for _, ipAddressKey := range ipAddressKeys {
			if annotationKey == ipAddressKey || strings.HasPrefix(annotationKey, ipAddressKey+".") {
				ipAddresses = append(ipAddresses, ipAddress)
				break
			}
		}
	}
	return ipAddresses
}

func ipAddressAnnotationKeysForIPFamily(key string) []string {
	keys := []string{ipAddressAnnotationKeyForIPFamily(key)}
	provider, ok := strings.CutSuffix(key, ".cloudyfolks.io/ip_family")
	if !ok {
		return keys
	}
	parts := strings.Split(provider, ".")
	if len(parts) > 3 && parts[2] == OvnProvider {
		ifName := parts[len(parts)-1]
		keys = append(keys, fmt.Sprintf("%s.%s.cloudyfolks.io/ip_address.%s", parts[0], parts[1], ifName))
	}
	return keys
}

func ValidateNetworkBroadcast(cidr, ip string) error {
	if cidr == "" || ip == "" {
		return nil
	}
	for cidrBlock := range strings.SplitSeq(cidr, ",") {
		for ipAddr := range strings.SplitSeq(ip, ",") {
			if CheckProtocol(cidrBlock) != CheckProtocol(ipAddr) {
				continue
			}
			_, network, _ := net.ParseCIDR(cidrBlock)
			if network == nil {
				continue
			}
			if AddressCountBigInt(network).EqualInt64(1) {
				return fmt.Errorf("subnet %s is configured with /32 or /128 netmask", cidrBlock)
			}

			ipStr := IPToString(ipAddr)

			if CheckProtocol(cidrBlock) == fabricv1.ProtocolIPv4 && SubnetBroadcast(cidrBlock) == ipStr {
				return fmt.Errorf("%s is the broadcast ip in cidr %s", ipStr, cidrBlock)
			}
			if SubnetNumber(cidrBlock) == ipStr {
				return fmt.Errorf("%s is the network number ip in cidr %s", ipStr, cidrBlock)
			}
		}
	}
	return nil
}

func ValidateCidrConflict(subnet fabricv1.Subnet, subnetList []fabricv1.Subnet) error {
	for _, sub := range subnetList {
		if sub.Spec.Vpc != subnet.Spec.Vpc || sub.Spec.Vlan != subnet.Spec.Vlan || sub.Name == subnet.Name {
			continue
		}

		if subnet.Spec.CIDRBlock != "" && sub.Spec.CIDRBlock != "" {
			if CIDROverlap(sub.Spec.CIDRBlock, subnet.Spec.CIDRBlock) {
				err := fmt.Errorf("subnet %s cidr %s is conflict with subnet %s cidr %s", subnet.Name, subnet.Spec.CIDRBlock, sub.Name, sub.Spec.CIDRBlock)
				return err
			}
		}

		if subnet.Spec.ExternalEgressGateway != "" && sub.Spec.ExternalEgressGateway != "" &&
			subnet.Spec.PolicyRoutingTableID == sub.Spec.PolicyRoutingTableID {
			err := fmt.Errorf("subnet %s policy routing table ID %d is conflict with subnet %s policy routing table ID %d", subnet.Name, subnet.Spec.PolicyRoutingTableID, sub.Name, sub.Spec.PolicyRoutingTableID)
			return err
		}
	}
	return nil
}

func ValidateVpc(vpc *fabricv1.Vpc) error {
	for _, item := range vpc.Spec.StaticRoutes {
		if item.Policy != "" && item.Policy != fabricv1.PolicyDst && item.Policy != fabricv1.PolicySrc {
			return fmt.Errorf("unknown policy type: %s", item.Policy)
		}

		if strings.Contains(item.CIDR, "/") {
			if _, _, err := net.ParseCIDR(item.CIDR); err != nil {
				klog.Error(err)
				return fmt.Errorf("invalid cidr %s: %w", item.CIDR, err)
			}
		} else if ip := net.ParseIP(item.CIDR); ip == nil {
			return fmt.Errorf("invalid IP %s", item.CIDR)
		}

		if ip := net.ParseIP(item.NextHopIP); ip == nil {
			return fmt.Errorf("invalid next hop IP %s", item.NextHopIP)
		}
	}

	for _, item := range vpc.Spec.PolicyRoutes {
		if item.Action != fabricv1.PolicyRouteActionReroute &&
			item.Action != fabricv1.PolicyRouteActionAllow &&
			item.Action != fabricv1.PolicyRouteActionDrop {
			return fmt.Errorf("unknown policy action: %s", item.Action)
		}

		if item.Action == fabricv1.PolicyRouteActionReroute {
			for ipStr := range strings.SplitSeq(item.NextHopIP, ",") {
				if ip := net.ParseIP(ipStr); ip == nil {
					return fmt.Errorf("invalid next hop ips: %s", item.NextHopIP)
				}
			}
		}
	}

	for _, item := range vpc.Spec.VpcPeerings {
		if err := CheckCidrs(item.LocalConnectIP); err != nil {
			klog.Error(err)
			return fmt.Errorf("invalid cidr %s", item.LocalConnectIP)
		}
	}

	if dr := vpc.Spec.DynamicRouting; dr.IsEnabled() {
		if !vpc.Spec.EnableExternal {
			return errors.New("dynamic routing requires enableExternal: the VPC advertises through its external gateway LRP")
		}
		if dr.ExternalSubnet == "" && len(vpc.Spec.ExtraExternalSubnets) > 1 {
			return fmt.Errorf("dynamicRouting.externalSubnet must name the external subnet whose LRP is the BGP next hop: extraExternalSubnets has %d entries", len(vpc.Spec.ExtraExternalSubnets))
		}
		if dr.ExternalSubnet != "" && len(vpc.Spec.ExtraExternalSubnets) > 0 && !slices.Contains(vpc.Spec.ExtraExternalSubnets, dr.ExternalSubnet) {
			return fmt.Errorf("dynamicRouting.externalSubnet %s is not an external subnet of the VPC: extraExternalSubnets is %v", dr.ExternalSubnet, vpc.Spec.ExtraExternalSubnets)
		}
		if len(dr.Redistribute) == 0 {
			return errors.New("redistribute must be set explicitly when dynamic routing is enabled")
		}
		seen := make(map[fabricv1.RedistributeType]struct{}, len(dr.Redistribute))
		for _, t := range dr.Redistribute {
			switch t {
			case fabricv1.RedistributeConnected, fabricv1.RedistributeConnectedAsHost,
				fabricv1.RedistributeStatic, fabricv1.RedistributeNAT, fabricv1.RedistributeLB:
			default:
				return fmt.Errorf("unknown redistribute type: %s", t)
			}
			if _, ok := seen[t]; ok {
				return fmt.Errorf("duplicate redistribute type: %s", t)
			}
			seen[t] = struct{}{}
		}
		if dr.VrfName != "" && (len(dr.VrfName) > 15 || strings.ContainsAny(dr.VrfName, "/ \t\n\f\r")) {
			return fmt.Errorf("vrfName %q must be a valid linux interface name (no '/' or whitespace, max 15 chars)", dr.VrfName)
		}
		if dr.VrfID == 0 {
			return errors.New("vrfId must be set when dynamic routing is enabled")
		}
		if dr.VrfID >= ReservedRoutingTableIDStart && dr.VrfID <= ReservedRoutingTableIDEnd {
			return fmt.Errorf("vrfId %d is in the reserved linux routing table range %d-%d", dr.VrfID, ReservedRoutingTableIDStart, ReservedRoutingTableIDEnd)
		}
		if dr.VrfID > MaxTableDirectID {
			return fmt.Errorf("vrfId %d is above %d: FRR redistributes the VRF table with table-direct, which addresses tables 1-%d", dr.VrfID, MaxTableDirectID, MaxTableDirectID)
		}
	}

	return nil
}

func ValidateVpcVrfID(vpc *fabricv1.Vpc, vpcs []fabricv1.Vpc) error {
	dr := vpc.Spec.DynamicRouting
	if !dr.IsEnabled() {
		return nil
	}
	for _, other := range vpcs {
		if other.Name == vpc.Name {
			continue
		}
		if odr := other.Spec.DynamicRouting; odr.IsEnabled() && odr.VrfID == dr.VrfID {
			return fmt.Errorf("vrfId %d is already used by vpc %s", dr.VrfID, other.Name)
		}
	}
	return nil
}
