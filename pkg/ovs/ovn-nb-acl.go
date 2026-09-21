package ovs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	v1alpha1 "sigs.k8s.io/network-policy-api/apis/v1alpha1"
	v1alpha2 "sigs.k8s.io/network-policy-api/apis/v1alpha2"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	ovsclient "github.com/cloudyfolks-labs/fabric/pkg/ovsdb/client"
	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

type ACLErrorType int

const (
	ACLErrorNotFound ACLErrorType = iota
	ACLErrorDuplicated
	ACLErrorDatabase
)

type ACLError struct {
	Type ACLErrorType
	Msg  string
}

func (e *ACLError) Error() string {
	return e.Msg
}

func NewACLError(errType ACLErrorType, msg string) *ACLError {
	return &ACLError{Type: errType, Msg: msg}
}

func setACLName(acl *ovnnb.ACL, name string) {
	if len(name) > 63 {
		name = name[:60] + "..."
	}
	acl.Name = new(name)
}

func (c *OVNNbClient) UpdateDefaultBlockACLOps(npName, pgName, direction string, loggingEnabled, lax bool, logRate int) ([]ovsdb.Operation, error) {
	portDirection := "outport"
	priority := util.IngressDefaultDrop
	meterName := fmt.Sprintf("%s_%s_meter", pgName, direction)

	if direction == ovnnb.ACLDirectionFromLport {
		portDirection = "inport"
		priority = util.EgressDefaultDrop
	}

	var match ACLMatch

	if lax {
		match = NewAndACLMatch(
			NewACLMatch(portDirection, "==", "@"+pgName, ""),
			NewACLMatch("(tcp || udp || sctp)", "", "", ""),
		)
	} else {
		match = NewAndACLMatch(
			NewACLMatch(portDirection, "==", "@"+pgName, ""),
			NewACLMatch("ip", "", "", ""),
		)
	}

	options := func(acl *ovnnb.ACL) {
		setACLName(acl, npName)
		if loggingEnabled {
			acl.Log = true
			acl.Severity = ptr.To(ovnnb.ACLSeverityWarning)
			if loggingEnabled && logRate > 0 {
				acl.Meter = new(meterName)
			}
		}

		if direction == ovnnb.ACLDirectionFromLport {
			if acl.Options == nil {
				acl.Options = make(map[string]string)
			}
			acl.Options["apply-after-lb"] = "true"
		}
	}

	if loggingEnabled && logRate > 0 {
		if err := c.CreateOrUpdateMeter(meterName, ovnnb.MeterUnitPktps, logRate, 1); err != nil {
			klog.Errorf("failed to create meter %s: %v", meterName, err)
			return nil, fmt.Errorf("create meter %s: %w", meterName, err)
		}
	} else {
		if err := c.DeleteMeter(meterName); err != nil {
			klog.Errorf("failed to delete meter %s: %v", meterName, err)
		}
	}

	defaultDropACL, err := c.newACLWithoutCheck(pgName, direction, priority, match.String(), ovnnb.ACLActionDrop, util.NetpolACLTier, options)
	if err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("failed to create drop acl for port group %s: %w", pgName, err)
	}

	ops, err := c.CreateAclsOps(pgName, portGroupKey, defaultDropACL)
	if err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("failed to create default drop acl ops for port group %s: %w", pgName, err)
	}

	return ops, nil
}

func (c *OVNNbClient) UpdateDefaultBlockExceptionsACLOps(npName, pgName, npNamespace, direction string) ([]ovsdb.Operation, error) {
	portDirection := "outport"
	priority := util.IngressAllowPriority
	dhcpv4UdpSrc, dhcpv4UdpDst := "67", "68"
	dhcpv6UdpSrc, dhcpv6UdpDst := "547", "546"

	if direction == ovnnb.ACLDirectionFromLport {
		portDirection = "inport"
		priority = util.EgressAllowPriority
		dhcpv4UdpSrc, dhcpv4UdpDst = dhcpv4UdpDst, dhcpv4UdpSrc
		dhcpv6UdpSrc, dhcpv6UdpDst = dhcpv6UdpDst, dhcpv6UdpSrc
	}

	acls := make([]*ovnnb.ACL, 0)

	newACL := func(match string) {
		options := func(acl *ovnnb.ACL) {
			setACLName(acl, npName)
			if direction == ovnnb.ACLDirectionFromLport {
				if acl.Options == nil {
					acl.Options = make(map[string]string)
				}
				acl.Options["apply-after-lb"] = "true"
			}
		}

		acl, err := c.newACLWithoutCheck(pgName, direction, priority, match, ovnnb.ACLActionAllowRelated, util.NetpolACLTier, options)
		if err != nil {
			klog.Error(err)
			klog.Errorf("failed to create new block exceptions acl for network policy %s/%s: %v", npNamespace, npName, err)
			return
		}
		acls = append(acls, acl)
	}

	dhcpv6Match := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("udp.src", "==", dhcpv6UdpSrc, ""),
		NewACLMatch("udp.dst", "==", dhcpv6UdpDst, ""),
		NewACLMatch("ip6", "", "", ""),
	)
	newACL(dhcpv6Match.String())

	dhcpv4Match := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("udp.src", "==", dhcpv4UdpSrc, ""),
		NewACLMatch("udp.dst", "==", dhcpv4UdpDst, ""),
		NewACLMatch("ip4", "", "", ""),
	)
	newACL(dhcpv4Match.String())

	ops, err := c.CreateAclsOps(pgName, portGroupKey, acls...)
	if err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("failed to create block exceptions acl for port group %s: %w", pgName, err)
	}
	return ops, nil
}

func (c *OVNNbClient) UpdateIngressACLOps(pgName, asIngressName, asExceptName, protocol, aclName string, npp []netv1.NetworkPolicyPort, logEnable bool, logACLActions []ovnnb.ACLAction, logRate int, namedPortMap map[string]*util.NamedPortInfo) ([]ovsdb.Operation, error) {
	acls := make([]*ovnnb.ACL, 0)
	meterName := fmt.Sprintf("%s_%s_meter", pgName, ovnnb.ACLDirectionToLport)
	if logEnable && logRate > 0 {
		if err := c.CreateOrUpdateMeter(meterName, ovnnb.MeterUnitPktps, logRate, 1); err != nil {
			return nil, fmt.Errorf("create ingress meter %s: %w", meterName, err)
		}
	} else {
		if err := c.DeleteMeter(meterName); err != nil {
			klog.Errorf("failed to delete ingress meter %s: %v", meterName, err)
		}
	}

	matches := newNetworkPolicyACLMatch(pgName, asIngressName, asExceptName, protocol, ovnnb.ACLDirectionToLport, npp, namedPortMap)
	for _, m := range matches {
		options := func(acl *ovnnb.ACL) {
			setACLName(acl, aclName)
			if logEnable && slices.Contains(logACLActions, ovnnb.ACLActionAllow) {
				acl.Log = true
				if logEnable && logRate > 0 {
					acl.Meter = new(meterName)
				}
			}
		}

		allowACL, err := c.newACLWithoutCheck(pgName, ovnnb.ACLDirectionToLport, util.IngressAllowPriority, m, ovnnb.ACLActionAllowRelated, util.NetpolACLTier, options)
		if err != nil {
			klog.Error(err)
			return nil, fmt.Errorf("new allow ingress acl for port group %s: %w", pgName, err)
		}

		acls = append(acls, allowACL)
	}

	ops, err := c.CreateAclsOps(pgName, portGroupKey, acls...)
	if err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("failed to create ingress acl for port group %s: %w", pgName, err)
	}

	return ops, nil
}

func (c *OVNNbClient) UpdateEgressACLOps(pgName, asEgressName, asExceptName, protocol, aclName string, npp []netv1.NetworkPolicyPort, logEnable bool, logACLActions []ovnnb.ACLAction, logRate int, namedPortMap map[string]*util.NamedPortInfo) ([]ovsdb.Operation, error) {
	acls := make([]*ovnnb.ACL, 0)
	meterName := fmt.Sprintf("%s_%s_meter", pgName, ovnnb.ACLDirectionFromLport)
	if logEnable && logRate > 0 {
		if err := c.CreateOrUpdateMeter(meterName, ovnnb.MeterUnitPktps, logRate, 1); err != nil {
			return nil, fmt.Errorf("create egress meter %s: %w", meterName, err)
		}
	} else {
		if err := c.DeleteMeter(meterName); err != nil {
			klog.Errorf("failed to delete egress meter %s: %v", meterName, err)
		}
	}

	matches := newNetworkPolicyACLMatch(pgName, asEgressName, asExceptName, protocol, ovnnb.ACLDirectionFromLport, npp, namedPortMap)
	for _, m := range matches {
		allowACL, err := c.newACLWithoutCheck(pgName, ovnnb.ACLDirectionFromLport, util.EgressAllowPriority, m, ovnnb.ACLActionAllowRelated, util.NetpolACLTier, func(acl *ovnnb.ACL) {
			setACLName(acl, aclName)
			if acl.Options == nil {
				acl.Options = make(map[string]string)
			}
			acl.Options["apply-after-lb"] = "true"
			if logEnable && slices.Contains(logACLActions, ovnnb.ACLActionAllow) {
				acl.Log = true
				if logEnable && logRate > 0 {
					acl.Meter = new(meterName)
				}
			}
		})
		if err != nil {
			klog.Error(err)
			return nil, fmt.Errorf("new allow egress acl for port group %s: %w", pgName, err)
		}

		acls = append(acls, allowACL)
	}

	ops, err := c.CreateAclsOps(pgName, portGroupKey, acls...)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	return ops, nil
}

func (c *OVNNbClient) CreateGatewayACL(lsName, pgName string) error {
	var parentName, parentType string
	switch {
	case len(pgName) != 0:
		parentName, parentType = pgName, portGroupKey
	case len(lsName) != 0:
		parentName, parentType = lsName, LogicalSwitchKey
	default:
		return errors.New("one of port group name and logical switch name must be specified")
	}

	options := func(acl *ovnnb.ACL) {
		if acl.Options == nil {
			acl.Options = make(map[string]string)
		}
		acl.Options["apply-after-lb"] = "true"
	}

	icmpv6EgressACL, err := c.newACL(parentName, ovnnb.ACLDirectionFromLport, util.EgressAllowPriority, "icmp6", ovnnb.ACLActionAllowStateless, util.NetpolACLTier, options)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("new icmpv6 egress acl for %s: %w", parentName, err)
	}

	icmpv6IngressACL, err := c.newACL(parentName, ovnnb.ACLDirectionToLport, util.IngressAllowPriority, "icmp6", ovnnb.ACLActionAllowStateless, util.NetpolACLTier)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("new icmpv6 ingress acl for %s: %w", parentName, err)
	}

	if err := c.CreateAcls(parentName, parentType, icmpv6EgressACL, icmpv6IngressACL); err != nil {
		klog.Error(err)
		return fmt.Errorf("add gateway acls to %s: %w", parentName, err)
	}

	return nil
}

func (c *OVNNbClient) CreateNodeACL(pgName, nodeIPStr, joinIPStr string) error {
	acls := make([]*ovnnb.ACL, 0)
	nodeIPs := strings.Split(nodeIPStr, ",")
	for _, nodeIP := range nodeIPs {
		protocol := util.CheckProtocol(nodeIP)
		ipSuffix := "ip4"
		if protocol == fabricv1.ProtocolIPv6 {
			ipSuffix = "ip6"
		}
		pgAs := fmt.Sprintf("%s_%s", pgName, ipSuffix)

		allowIngressACL, err := c.newACL(pgName, ovnnb.ACLDirectionToLport, util.NodeAllowPriority, fmt.Sprintf("%s.src == %s && %s.dst == $%s", ipSuffix, nodeIP, ipSuffix, pgAs), ovnnb.ACLActionAllowRelated, util.NetpolACLTier)
		if err != nil {
			klog.Error(err)
			return fmt.Errorf("new allow ingress acl for port group %s: %w", pgName, err)
		}

		options := func(acl *ovnnb.ACL) {
			if acl.Options == nil {
				acl.Options = make(map[string]string)
			}
			acl.Options["apply-after-lb"] = "true"
		}

		allowEgressACL, err := c.newACL(pgName, ovnnb.ACLDirectionFromLport, util.NodeAllowPriority, fmt.Sprintf("%s.dst == %s && %s.src == $%s", ipSuffix, nodeIP, ipSuffix, pgAs), ovnnb.ACLActionAllowRelated, util.NetpolACLTier, options)
		if err != nil {
			klog.Error(err)
			return fmt.Errorf("new allow egress acl for port group %s: %w", pgName, err)
		}

		acls = append(acls, allowIngressACL, allowEgressACL)
	}

	for joinIP := range strings.SplitSeq(joinIPStr, ",") {
		if slices.Contains(nodeIPs, joinIP) {
			continue
		}

		protocol := util.CheckProtocol(joinIP)
		ipSuffix := "ip4"
		if protocol == fabricv1.ProtocolIPv6 {
			ipSuffix = "ip6"
		}

		pgAs := fmt.Sprintf("%s_%s", pgName, ipSuffix)

		if err := c.DeleteACL(pgName, portGroupKey, ovnnb.ACLDirectionToLport, util.NodeAllowPriority, fmt.Sprintf("%s.src == %s && %s.dst == $%s", ipSuffix, joinIP, ipSuffix, pgAs), util.NetpolACLTier); err != nil {
			klog.Errorf("delete ingress acl from port group %s: %v", pgName, err)
			return err
		}

		if err := c.DeleteACL(pgName, portGroupKey, ovnnb.ACLDirectionFromLport, util.NodeAllowPriority, fmt.Sprintf("%s.dst == %s && %s.src == $%s", ipSuffix, joinIP, ipSuffix, pgAs), util.NetpolACLTier); err != nil {
			klog.Errorf("delete egress acl from port group %s: %v", pgName, err)
			return err
		}
	}

	if err := c.CreateAcls(pgName, portGroupKey, acls...); err != nil {
		return fmt.Errorf("add node acls to port group %s: %w", pgName, err)
	}

	return nil
}

func (c *OVNNbClient) CreateSgDenyAllACL(sgName string) error {
	pgName := GetSgPortGroupName(sgName)

	acls := make([]*ovnnb.ACL, 0)

	for tier := util.SecurityGroupAPITierMinimum; tier <= util.SecurityGroupAPITierMaximum; tier++ {
		ovnTier := util.ConvertSGTierToOvnTier(tier)
		ingressACL, err := c.newACL(pgName, ovnnb.ACLDirectionToLport, util.SecurityGroupDropPriority, fmt.Sprintf("outport == @%s && ip", pgName), ovnnb.ACLActionDrop, ovnTier)
		if err != nil {
			klog.Error(err)
			return fmt.Errorf("new deny all ingress acl for security group %s: %w", sgName, err)
		}
		acls = append(acls, ingressACL)

		egressACL, err := c.newACL(pgName, ovnnb.ACLDirectionFromLport, util.SecurityGroupDropPriority, fmt.Sprintf("inport == @%s && ip", pgName), ovnnb.ACLActionDrop, ovnTier)
		if err != nil {
			klog.Error(err)
			return fmt.Errorf("new deny all egress acl for security group %s: %w", sgName, err)
		}
		acls = append(acls, egressACL)
	}

	err := c.CreateAcls(pgName, portGroupKey, acls...)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("add deny all acl to port group %s: %w", pgName, err)
	}

	return nil
}

func (c *OVNNbClient) CreateSgBaseACL(sgName, direction string) error {
	pgName := GetSgPortGroupName(sgName)

	portDirection := "outport"
	dhcpv4UdpSrc, dhcpv4UdpDst := "67", "68"
	dhcpv6UdpSrc, dhcpv6UdpDst := "547", "546"
	icmpv6Type := "{130, 134, 135, 136}"

	if direction == ovnnb.ACLDirectionFromLport {
		portDirection = "inport"
		dhcpv4UdpSrc, dhcpv4UdpDst = dhcpv4UdpDst, dhcpv4UdpSrc
		dhcpv6UdpSrc, dhcpv6UdpDst = dhcpv6UdpDst, dhcpv6UdpSrc
		icmpv6Type = "{130, 133, 135, 136}"
	}

	acls := make([]*ovnnb.ACL, 0)

	newACL := func(match string) {
		for tier := util.SecurityGroupAPITierMinimum; tier <= util.SecurityGroupAPITierMaximum; tier++ {
			acl, err := c.newACL(pgName, direction, util.SecurityGroupBasePriority, match, ovnnb.ACLActionAllowRelated, util.ConvertSGTierToOvnTier(tier))
			if err != nil {
				klog.Error(err)
				klog.Errorf("failed to create new base ingress acl for security group %s: %v", sgName, err)
				return
			}
			acls = append(acls, acl)
		}
	}

	allArpMatch := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("arp", "", "", ""),
	)
	newACL(allArpMatch.String())

	icmpv6Match := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("icmp6.type", "==", icmpv6Type, ""),
		NewACLMatch("icmp6.code", "==", "0", ""),
		NewACLMatch("ip.ttl", "==", "255", ""),
	)
	newACL(icmpv6Match.String())

	dhcpv4Match := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("udp.src", "==", dhcpv4UdpSrc, ""),
		NewACLMatch("udp.dst", "==", dhcpv4UdpDst, ""),
		NewACLMatch("ip4", "", "", ""),
	)
	newACL(dhcpv4Match.String())

	dhcpv6Match := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("udp.src", "==", dhcpv6UdpSrc, ""),
		NewACLMatch("udp.dst", "==", dhcpv6UdpDst, ""),
		NewACLMatch("ip6", "", "", ""),
	)
	newACL(dhcpv6Match.String())

	vrrpMatch := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("ip.proto", "==", "112", ""),
	)
	newACL(vrrpMatch.String())

	if err := c.CreateAcls(pgName, portGroupKey, acls...); err != nil {
		klog.Error(err)
		return fmt.Errorf("add ingress acls to port group %s: %w", pgName, err)
	}
	return nil
}

func (c *OVNNbClient) UpdateSgACL(sg *fabricv1.SecurityGroup, direction string) error {
	pgName := GetSgPortGroupName(sg.Name)

	if err := c.DeleteAcls(pgName, portGroupKey, direction, nil); err != nil {
		klog.Error(err)
		return fmt.Errorf("delete direction '%s' acls from port group %s: %w", direction, pgName, err)
	}

	acls := make([]*ovnnb.ACL, 0, 2)

	srcOrDst, portDirection, sgRules := "src", "outport", sg.Spec.IngressRules
	if direction == ovnnb.ACLDirectionFromLport {
		srcOrDst = "dst"
		portDirection = "inport"
		sgRules = sg.Spec.EgressRules
	}

	if sg.Spec.AllowSameGroupTraffic {
		asName := GetSgV4AssociatedName(sg.Name)
		for _, ipSuffix := range []string{"ip4", "ip6"} {
			if ipSuffix == "ip6" {
				asName = GetSgV6AssociatedName(sg.Name)
			}

			match := NewAndACLMatch(
				NewACLMatch(portDirection, "==", "@"+pgName, ""),
				NewACLMatch(ipSuffix, "", "", ""),
				NewACLMatch(ipSuffix+"."+srcOrDst, "==", "$"+asName, ""),
			)
			acl, err := c.newACL(pgName, direction, util.SecurityGroupAllowPriority, match.String(), ovnnb.ACLActionAllowRelated, util.ConvertSGTierToOvnTier(sg.Spec.Tier))
			if err != nil {
				klog.Error(err)
				return fmt.Errorf("new allow acl for security group %s: %w", sg.Name, err)
			}

			acls = append(acls, acl)
		}
	}

	for _, rule := range sgRules {
		acl, err := c.newSgRuleACL(sg.Name, direction, rule, util.ConvertSGTierToOvnTier(sg.Spec.Tier))
		if err != nil {
			klog.Error(err)
			return fmt.Errorf("new rule acl for security group %s: %w", sg.Name, err)
		}
		acls = append(acls, acl)
	}

	if err := c.CreateAcls(pgName, portGroupKey, acls...); err != nil {
		klog.Error(err)
		return fmt.Errorf("add acl to port group %s: %w", pgName, err)
	}

	return nil
}

func (c *OVNNbClient) UpdateLogicalSwitchACL(lsName, cidrBlock string, subnetAcls []fabricv1.ACL, allowEWTraffic bool) error {
	if len(subnetAcls) == 0 {
		if err := c.DeleteAcls(lsName, LogicalSwitchKey, "", map[string]string{"subnet": lsName}); err != nil {
			klog.Error(err)
			return fmt.Errorf("delete subnet acls from %s: %w", lsName, err)
		}
		return nil
	}

	acls := make([]*ovnnb.ACL, 0)

	options := func(acl *ovnnb.ACL) {
		if acl.ExternalIDs == nil {
			acl.ExternalIDs = make(map[string]string)
		}
		acl.ExternalIDs["subnet"] = lsName
	}

	if allowEWTraffic {
		for cidr := range strings.SplitSeq(cidrBlock, ",") {
			protocol := util.CheckProtocol(cidr)

			ipSuffix := "ip4"
			if protocol == fabricv1.ProtocolIPv6 {
				ipSuffix = "ip6"
			}

			sameSubnetMatch := NewAndACLMatch(
				NewACLMatch(ipSuffix+".src", "==", cidr, ""),
				NewACLMatch(ipSuffix+".dst", "==", cidr, ""),
			)

			ingressSameSubnetACL, err := c.newACL(lsName, ovnnb.ACLDirectionToLport, util.AllowEWTrafficPriority, sameSubnetMatch.String(), ovnnb.ACLActionAllow, util.NetpolACLTier, options)
			if err != nil {
				klog.Error(err)
				return fmt.Errorf("new same subnet ingress acl for logical switch %s: %w", lsName, err)
			}
			acls = append(acls, ingressSameSubnetACL)

			egressSameSubnetACL, err := c.newACL(lsName, ovnnb.ACLDirectionFromLport, util.AllowEWTrafficPriority, sameSubnetMatch.String(), ovnnb.ACLActionAllow, util.NetpolACLTier, options)
			if err != nil {
				klog.Error(err)
				return fmt.Errorf("new same subnet egress acl for logical switch %s: %w", lsName, err)
			}
			acls = append(acls, egressSameSubnetACL)
		}
	}

	for _, subnetACL := range subnetAcls {
		acl, err := c.newACL(lsName, subnetACL.Direction, strconv.Itoa(subnetACL.Priority), subnetACL.Match, subnetACL.Action, util.NetpolACLTier, options)
		if err != nil {
			klog.Error(err)
			return fmt.Errorf("new acl for logical switch %s: %w", lsName, err)
		}
		acls = append(acls, acl)
	}

	delOps, err := c.DeleteAclsOps(lsName, LogicalSwitchKey, "", map[string]string{"subnet": lsName})
	if err != nil {
		klog.Error(err)
		return err
	}

	addOps, err := c.CreateAclsOps(lsName, LogicalSwitchKey, acls...)
	if err != nil {
		klog.Error(err)
		return err
	}

	if err := c.Transact("acls-update", append(delOps, addOps...)); err != nil {
		klog.Error(err)
		return fmt.Errorf("update acls for logical switch %s: %w", lsName, err)
	}

	return nil
}

func (c *OVNNbClient) UpdateACL(acl *ovnnb.ACL, fields ...any) error {
	if acl == nil {
		return errors.New("address_set is nil")
	}

	op, err := c.Where(acl).Update(acl, fields...)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("generate operations for updating acl with 'direction %s priority %d match %s': %w", acl.Direction, acl.Priority, acl.Match, err)
	}

	if err = c.Transact("acl-update", op); err != nil {
		klog.Error(err)
		return fmt.Errorf("update acl with 'direction %s priority %d match %s': %w", acl.Direction, acl.Priority, acl.Match, err)
	}

	return nil
}

func (c *OVNNbClient) SetLogicalSwitchPrivate(lsName, cidrBlock, nodeSwitchCIDR string, allowSubnets []string) error {
	if err := c.DeleteAcls(lsName, LogicalSwitchKey, "", nil); err != nil {
		klog.Error(err)
		return fmt.Errorf("clear logical switch %s acls: %w", lsName, err)
	}

	acls := make([]*ovnnb.ACL, 0)

	allIPMatch := NewACLMatch("ip", "", "", "")

	options := func(acl *ovnnb.ACL) {
		setACLName(acl, lsName)
		acl.Log = true
		acl.Severity = ptr.To(ovnnb.ACLSeverityWarning)
	}

	defaultDropACL, err := c.newACL(lsName, ovnnb.ACLDirectionToLport, util.DefaultDropPriority, allIPMatch.String(), ovnnb.ACLActionDrop, util.NetpolACLTier, options)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("new default drop ingress acl for logical switch %s: %w", lsName, err)
	}

	acls = append(acls, defaultDropACL)

	nodeSubnetACLFunc := func(protocol, ipSuffix string) error {
		for nodeCidr := range strings.SplitSeq(nodeSwitchCIDR, ",") {
			if protocol != util.CheckProtocol(nodeCidr) {
				continue
			}

			match := NewACLMatch(ipSuffix+".src", "==", nodeCidr, "")

			acl, err := c.newACL(lsName, ovnnb.ACLDirectionToLport, util.NodeAllowPriority, match.String(), ovnnb.ACLActionAllowRelated, util.NetpolACLTier)
			if err != nil {
				klog.Error(err)
				return fmt.Errorf("new node subnet ingress acl for logical switch %s: %w", lsName, err)
			}

			acls = append(acls, acl)
		}

		return nil
	}

	allowSubnetACLFunc := func(protocol, ipSuffix, cidr string) error {
		for _, allowSubnet := range allowSubnets {
			subnet := strings.TrimSpace(allowSubnet)

			if len(subnet) == 0 {
				continue
			}

			if util.CheckProtocol(subnet) != protocol {
				continue
			}

			match := NewOrACLMatch(
				NewAndACLMatch(
					NewACLMatch(ipSuffix+".src", "==", cidr, ""),
					NewACLMatch(ipSuffix+".dst", "==", subnet, ""),
				),
				NewAndACLMatch(
					NewACLMatch(ipSuffix+".src", "==", subnet, ""),
					NewACLMatch(ipSuffix+".dst", "==", cidr, ""),
				),
			)

			acl, err := c.newACL(lsName, ovnnb.ACLDirectionToLport, util.SubnetAllowPriority, match.String(), ovnnb.ACLActionAllowRelated, util.NetpolACLTier)
			if err != nil {
				klog.Error(err)
				return fmt.Errorf("new allow subnet ingress acl for logical switch %s: %w", lsName, err)
			}

			acls = append(acls, acl)
		}
		return nil
	}

	for cidr := range strings.SplitSeq(cidrBlock, ",") {
		protocol := util.CheckProtocol(cidr)

		ipSuffix := "ip4"
		if protocol == fabricv1.ProtocolIPv6 {
			ipSuffix = "ip6"
		}

		sameSubnetMatch := NewAndACLMatch(
			NewACLMatch(ipSuffix+".src", "==", cidr, ""),
			NewACLMatch(ipSuffix+".dst", "==", cidr, ""),
		)

		sameSubnetACL, err := c.newACL(lsName, ovnnb.ACLDirectionToLport, util.SubnetAllowPriority, sameSubnetMatch.String(), ovnnb.ACLActionAllowRelated, util.NetpolACLTier)
		if err != nil {
			klog.Error(err)
			return fmt.Errorf("new same subnet ingress acl for logical switch %s: %w", lsName, err)
		}

		acls = append(acls, sameSubnetACL)

		if err := nodeSubnetACLFunc(protocol, ipSuffix); err != nil {
			klog.Error(err)
			return err
		}

		if err := allowSubnetACLFunc(protocol, ipSuffix, cidr); err != nil {
			klog.Error(err)
			return err
		}
	}

	if err := c.CreateAcls(lsName, LogicalSwitchKey, acls...); err != nil {
		klog.Error(err)
		return fmt.Errorf("add ingress acls to logical switch %s: %w", lsName, err)
	}

	return nil
}

func (c *OVNNbClient) SetNetPolACLLog(pgName string, logEnable, isIngress bool) error {
	direction := ovnnb.ACLDirectionToLport
	portDirection := "outport"
	if !isIngress {
		direction = ovnnb.ACLDirectionFromLport
		portDirection = "inport"
	}

	allIPMatch := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("ip", "", "", ""),
	)

	acl, err := c.GetACL(pgName, direction, util.IngressDefaultDrop, allIPMatch.String(), util.NetpolACLTier, true)
	if err != nil {
		klog.Error(err)
		return err
	}

	if acl == nil {
		return nil
	}

	if acl.Log == logEnable {
		return nil
	}
	acl.Log = logEnable

	err = c.UpdateACL(acl, &acl.Log)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("update acl: %w", err)
	}

	return nil
}

func (c *OVNNbClient) CreateAcls(parentName, parentType string, acls ...*ovnnb.ACL) error {
	ops, err := c.CreateAclsOps(parentName, parentType, acls...)
	if err != nil {
		klog.Error(err)
		return err
	}

	if err = c.Transact("acls-add", ops); err != nil {
		return fmt.Errorf("add acls to type %s %s: %w", parentType, parentName, err)
	}

	return nil
}

func (c *OVNNbClient) CreateBareACL(parentName, direction, priority, match, action string) error {
	acl, err := c.newACL(parentName, direction, priority, match, action, util.NetpolACLTier)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("new acl direction %s priority %s match %s action %s: %w", direction, priority, match, action, err)
	}

	op, err := c.Create(acl)
	if err != nil {
		klog.Error(err)
		return fmt.Errorf("generate operations for creating acl direction %s priority %s match %s action %s: %w", direction, priority, match, action, err)
	}

	if err = c.Transact("acl-create", op); err != nil {
		klog.Error(err)
		return fmt.Errorf("create acl direction %s priority %s match %s action %s: %w", direction, priority, match, action, err)
	}

	return nil
}

func (c *OVNNbClient) DeleteAcls(parentName, parentType, direction string, externalIDs map[string]string) error {
	ops, err := c.DeleteAclsOps(parentName, parentType, direction, externalIDs)
	if err != nil {
		klog.Error(err)
		return err
	}

	if err = c.Transact("acls-del", ops); err != nil {
		klog.Error(err)
		return fmt.Errorf("del acls from type %s %s: %w", parentType, parentName, err)
	}

	return nil
}

func (c *OVNNbClient) DeleteACL(parentName, parentType, direction, priority, match string, tier int) error {
	acl, err := c.GetACL(parentName, direction, priority, match, tier, true)
	if err != nil {
		klog.Error(err)
		return err
	}

	if acl == nil {
		return nil
	}

	var removeACLOp []ovsdb.Operation
	if parentType == portGroupKey {
		removeACLOp, err = c.portGroupUpdateACLOp(parentName, []string{acl.UUID}, ovsdb.MutateOperationDelete)
		if err != nil {
			klog.Error(err)
			return fmt.Errorf("generate operations for deleting acl from port group %s: %w", parentName, err)
		}
	} else {
		removeACLOp, err = c.logicalSwitchUpdateACLOp(parentName, []string{acl.UUID}, ovsdb.MutateOperationDelete)
		if err != nil {
			klog.Error(err)
			return fmt.Errorf("generate operations for deleting acl from logical switch %s: %w", parentName, err)
		}
	}

	if err = c.Transact("acls-del", removeACLOp); err != nil {
		klog.Error(err)
		return fmt.Errorf("del acls from type %s %s: %w", parentType, parentName, err)
	}

	return nil
}

func (c *OVNNbClient) GetACL(parent, direction, priority, match string, tier int, ignoreNotFound bool) (*ovnnb.ACL, error) {
	if len(parent) == 0 {
		return nil, errors.New("the port group name or logical switch name is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	intPriority, _ := strconv.Atoi(priority)

	aclList := make([]ovnnb.ACL, 0)
	if err := c.ovsDbClient.WhereCache(func(acl *ovnnb.ACL) bool {
		return len(acl.ExternalIDs) != 0 && acl.ExternalIDs[aclParentKey] == parent && acl.Direction == direction && acl.Priority == intPriority && acl.Match == match && tier == acl.Tier
	}).List(ctx, &aclList); err != nil {
		klog.Error(err)
		return nil, NewACLError(ACLErrorDatabase, fmt.Sprintf("get acl with 'parent %s direction %s priority %s match %s tier %d': %v", parent, direction, priority, match, tier, err))
	}

	if len(aclList) == 0 {
		if ignoreNotFound {
			return nil, nil
		}
		return nil, NewACLError(ACLErrorNotFound, fmt.Sprintf("not found acl with 'parent %s direction %s priority %s match %s tier %d '", parent, direction, priority, match, tier))
	}

	if len(aclList) > 1 {
		return nil, NewACLError(ACLErrorDuplicated, fmt.Sprintf("more than one acl with same 'parent %s direction %s priority %s match %s tier %d '", parent, direction, priority, match, tier))
	}

	// #nosec G602
	return &aclList[0], nil
}

func (c *OVNNbClient) ListAcls(direction string, externalIDs map[string]string) ([]ovnnb.ACL, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	aclList := make([]ovnnb.ACL, 0)

	if err := c.WhereCache(aclFilter(direction, externalIDs)).List(ctx, &aclList); err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("list acls: %w", err)
	}

	return aclList, nil
}

func (c *OVNNbClient) ACLExists(parent, direction, priority, match string, tier int) (bool, error) {
	acl, err := c.GetACL(parent, direction, priority, match, tier, true)
	if err != nil {
		var aclErr *ACLError
		if errors.As(err, &aclErr) && aclErr.Type == ACLErrorDuplicated {
			return true, nil
		}
		return false, err
	}
	return acl != nil, nil
}

func (c *OVNNbClient) newACL(parent, direction, priority, match, action string, tier int, options ...func(acl *ovnnb.ACL)) (*ovnnb.ACL, error) {
	if len(parent) == 0 {
		return nil, errors.New("the port group name or logical switch name is required")
	}

	if len(direction) == 0 || len(priority) == 0 || len(match) == 0 || len(action) == 0 {
		return nil, fmt.Errorf("acl 'direction %s' and 'priority %s' and 'match %s' and 'action %s' is required", direction, priority, match, action)
	}

	exists, err := c.ACLExists(parent, direction, priority, match, tier)
	if err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("get parent %s acl: %w", parent, err)
	}

	if exists {
		return nil, nil
	}

	intPriority, _ := strconv.Atoi(priority)

	acl := &ovnnb.ACL{
		UUID:      ovsclient.NamedUUID(),
		Action:    action,
		Direction: direction,
		Match:     match,
		Priority:  intPriority,
		ExternalIDs: map[string]string{
			aclParentKey: parent,
			"vendor":     util.VendorTag,
		},
		Tier: tier,
	}

	for _, option := range options {
		option(acl)
	}

	return acl, nil
}

func (c *OVNNbClient) newACLWithoutCheck(parent, direction, priority, match, action string, tier int, options ...func(acl *ovnnb.ACL)) (*ovnnb.ACL, error) {
	if len(parent) == 0 {
		return nil, errors.New("the port group name or logical switch name is required")
	}

	if len(direction) == 0 || len(priority) == 0 || len(match) == 0 || len(action) == 0 {
		return nil, fmt.Errorf("acl 'direction %s' and 'priority %s' and 'match %s' and 'action %s' is required", direction, priority, match, action)
	}

	intPriority, _ := strconv.Atoi(priority)

	acl := &ovnnb.ACL{
		UUID:      ovsclient.NamedUUID(),
		Action:    action,
		Direction: direction,
		Match:     match,
		Priority:  intPriority,
		ExternalIDs: map[string]string{
			aclParentKey: parent,
			"vendor":     util.VendorTag,
		},
		Tier: tier,
	}

	for _, option := range options {
		option(acl)
	}

	return acl, nil
}

func (c *OVNNbClient) newSgRuleACL(sgName, direction string, rule fabricv1.SecurityGroupRule, tier int) (*ovnnb.ACL, error) {
	ipSuffix := "ip4"
	if rule.IPVersion == "ipv6" {
		ipSuffix = "ip6"
	}

	pgName := GetSgPortGroupName(sgName)

	localSrcOrDst, remoteSrcOrDst, portDirection := "dst", "src", "outport"
	if direction == ovnnb.ACLDirectionFromLport {
		remoteSrcOrDst = "dst"
		localSrcOrDst = "src"
		portDirection = "inport"
	}

	remoteIPKey := ipSuffix + "." + remoteSrcOrDst
	localIPKey := ipSuffix + "." + localSrcOrDst

	allIPMatch := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch(ipSuffix, "", "", ""),
	)

	allowedIPMatch := NewAndACLMatch(
		allIPMatch,
		NewACLMatch(remoteIPKey, "==", rule.RemoteAddress, ""),
	)

	remotePgName := GetSgV4AssociatedName(rule.RemoteSecurityGroup)
	if rule.IPVersion == "ipv6" {
		remotePgName = GetSgV6AssociatedName(rule.RemoteSecurityGroup)
	}
	if rule.RemoteType == fabricv1.SgRemoteTypeSg {
		allowedIPMatch = NewAndACLMatch(
			allIPMatch,
			NewACLMatch(remoteIPKey, "==", "$"+remotePgName, ""),
		)
	}

	if rule.LocalAddress != "" {
		allowedIPMatch = NewAndACLMatch(
			allowedIPMatch,
			NewACLMatch(localIPKey, "==", rule.LocalAddress, ""),
		)
	}

	match := allowedIPMatch

	switch rule.Protocol {
	case fabricv1.SgProtocolICMP:
		match = NewAndACLMatch(
			allowedIPMatch,
			NewACLMatch("icmp4", "", "", ""),
		)
		if ipSuffix == "ip6" {
			match = NewAndACLMatch(
				allowedIPMatch,
				NewACLMatch("icmp6", "", "", ""),
			)
		}
	case fabricv1.SgProtocolTCP, fabricv1.SgProtocolUDP:
		match = NewAndACLMatch(
			allowedIPMatch,
			NewACLMatch(string(rule.Protocol)+".dst", "<=", strconv.Itoa(rule.PortRangeMin), strconv.Itoa(rule.PortRangeMax)),
		)

		if rule.LocalAddress != "" {
			match = NewAndACLMatch(
				match,
				NewACLMatch(string(rule.Protocol)+".src", "<=", strconv.Itoa(rule.SourcePortRangeMin), strconv.Itoa(rule.SourcePortRangeMax)),
			)
		}
	}

	var action string
	switch rule.Policy {
	case fabricv1.SgPolicyAllow:
		action = ovnnb.ACLActionAllowRelated
	case fabricv1.SgPolicyPass:
		action = ovnnb.ACLActionPass
	default:
		action = ovnnb.ACLActionDrop
	}

	highestPriority, _ := strconv.Atoi(util.SecurityGroupHighestPriority)

	acl, err := c.newACL(pgName, direction, strconv.Itoa(highestPriority-rule.Priority), match.String(), action, tier)
	if err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("new security group acl for port group %s: %w", pgName, err)
	}

	return acl, nil
}

func newNetworkPolicyACLMatch(pgName, asAllowName, asExceptName, protocol, direction string, npp []netv1.NetworkPolicyPort, namedPortMap map[string]*util.NamedPortInfo) []string {
	ipSuffix := "ip4"
	if protocol == fabricv1.ProtocolIPv6 {
		ipSuffix = "ip6"
	}

	srcOrDst, portDirection := "src", "outport"
	if direction == ovnnb.ACLDirectionFromLport {
		srcOrDst = "dst"
		portDirection = "inport"
	}

	ipKey := ipSuffix + "." + srcOrDst

	allIPMatch := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("ip", "", "", ""),
	)

	allowedIPMatch := NewAndACLMatch(
		allIPMatch,
		NewACLMatch(ipKey, "==", "$"+asAllowName, ""),
		NewACLMatch(ipKey, "!=", "$"+asExceptName, ""),
	)

	matches := make([]string, 0, len(npp))

	if len(npp) == 0 {
		return []string{allowedIPMatch.String()}
	}

	for _, port := range npp {
		protocol := strings.ToLower(string(*port.Protocol))

		if port.Port == nil {
			allLayer4Match := NewAndACLMatch(
				allowedIPMatch,
				NewACLMatch(protocol, "", "", ""),
			)

			matches = append(matches, allLayer4Match.String())
			continue
		}

		if port.EndPort == nil {
			tcpKey := protocol + ".dst"

			var portID int32
			if port.Port.Type == intstr.Int {
				portID = port.Port.IntVal
			} else {
				if namedPortMap == nil {
					continue
				}
				info, ok := namedPortMap[port.Port.StrVal]
				if !ok {
					klog.Errorf("no named port with name %s found in pg %s (%s)", port.Port.StrVal, pgName, direction)
					continue
				}
				portID = info.PortID
			}

			oneTCPMatch := NewAndACLMatch(
				allowedIPMatch,
				NewACLMatch(tcpKey, "==", strconv.Itoa(int(portID)), ""),
			)

			matches = append(matches, oneTCPMatch.String())

			continue
		}

		if port.Port.Type == intstr.String {
			klog.Errorf("named port %s with endPort is not supported in pg %s (%s), skipping", port.Port.StrVal, pgName, direction)
			continue
		}

		tcpKey := protocol + ".dst"
		severalTCPMatch := NewAndACLMatch(
			allowedIPMatch,
			NewACLMatch(tcpKey, "<=", strconv.Itoa(int(port.Port.IntVal)), strconv.Itoa(int(*port.EndPort))),
		)
		matches = append(matches, severalTCPMatch.String())
	}

	return matches
}

func newIPBlockACLMatch(pgName, protocol, direction string, ipBlocks []netv1.IPBlock, npp []netv1.NetworkPolicyPort, namedPortMap map[string]*util.NamedPortInfo) []string {
	ipSuffix := "ip4"
	if protocol == fabricv1.ProtocolIPv6 {
		ipSuffix = "ip6"
	}

	srcOrDst, portDirection := "src", "outport"
	if direction == ovnnb.ACLDirectionFromLport {
		srcOrDst = "dst"
		portDirection = "inport"
	}

	ipKey := ipSuffix + "." + srcOrDst

	var perBlockMatches []ACLMatch
	for i := range ipBlocks {
		block := ipBlocks[i]
		if util.CheckProtocol(block.CIDR) != protocol {
			continue
		}

		cidrMatch := NewACLMatch(ipKey, "==", block.CIDR, "")

		var filteredExcepts []string
		for _, e := range block.Except {
			if util.CheckProtocol(e) != protocol {
				continue
			}
			contained, err := util.CIDRContainsCIDR(block.CIDR, e)
			if err != nil {
				klog.Warningf("error checking containment for IPBlock except CIDR %s in main CIDR %s, skipping: %v", e, block.CIDR, err)
				continue
			}
			if !contained {
				klog.Warningf("IPBlock except CIDR %s is not contained in main CIDR %s, skipping", e, block.CIDR)
				continue
			}
			filteredExcepts = append(filteredExcepts, e)
		}

		if len(filteredExcepts) == 0 {
			perBlockMatches = append(perBlockMatches, cidrMatch)
		} else {
			exceptMatch := NewACLMatch(ipKey, "!=", "{"+strings.Join(filteredExcepts, ", ")+"}", "")
			perBlockMatches = append(perBlockMatches, NewAndACLMatch(cidrMatch, exceptMatch))
		}
	}

	if len(perBlockMatches) == 0 {
		return nil
	}

	ipBlockL3Match := perBlockMatches[0]
	if len(perBlockMatches) > 1 {
		ipBlockL3Match = NewOrACLMatch(perBlockMatches...)
	}

	allIPMatch := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("ip", "", "", ""),
	)

	allowedIPMatch := NewAndACLMatch(allIPMatch, NewGroupACLMatch(ipBlockL3Match))

	if len(npp) == 0 {
		return []string{allowedIPMatch.String()}
	}

	matches := make([]string, 0, len(npp))
	for _, port := range npp {
		protocol := strings.ToLower(string(*port.Protocol))

		if port.Port == nil {
			matches = append(matches, NewAndACLMatch(allowedIPMatch, NewACLMatch(protocol, "", "", "")).String())
			continue
		}

		if port.EndPort == nil {
			tcpKey := protocol + ".dst"
			var portID int32
			if port.Port.Type == intstr.Int {
				portID = port.Port.IntVal
			} else {
				if namedPortMap == nil {
					continue
				}
				info, ok := namedPortMap[port.Port.StrVal]
				if !ok {
					klog.Errorf("no named port with name %s found in pg %s (%s)", port.Port.StrVal, pgName, direction)
					continue
				}
				portID = info.PortID
			}
			matches = append(matches, NewAndACLMatch(allowedIPMatch, NewACLMatch(tcpKey, "==", strconv.Itoa(int(portID)), "")).String())
			continue
		}

		if port.Port.Type == intstr.String {
			klog.Errorf("named port %s with endPort is not supported in pg %s (%s), skipping", port.Port.StrVal, pgName, direction)
			continue
		}

		tcpKey := protocol + ".dst"
		matches = append(matches, NewAndACLMatch(allowedIPMatch, NewACLMatch(tcpKey, "<=", strconv.Itoa(int(port.Port.IntVal)), strconv.Itoa(int(*port.EndPort)))).String())
	}

	return matches
}

func (c *OVNNbClient) UpdateIngressIPBlockACLOps(pgName, protocol, aclName string, ipBlocks []netv1.IPBlock, npp []netv1.NetworkPolicyPort, logEnable bool, logACLActions []ovnnb.ACLAction, logRate int, namedPortMap map[string]*util.NamedPortInfo) ([]ovsdb.Operation, error) {
	meterName := fmt.Sprintf("%s_%s_meter", pgName, ovnnb.ACLDirectionToLport)
	matches := newIPBlockACLMatch(pgName, protocol, ovnnb.ACLDirectionToLport, ipBlocks, npp, namedPortMap)
	if len(matches) == 0 {
		return nil, nil
	}

	acls := make([]*ovnnb.ACL, 0, len(matches))
	for _, m := range matches {
		options := func(acl *ovnnb.ACL) {
			setACLName(acl, aclName)
			if logEnable && slices.Contains(logACLActions, ovnnb.ACLActionAllow) {
				acl.Log = true
				if logRate > 0 {
					acl.Meter = new(meterName)
				}
			}
		}

		allowACL, err := c.newACLWithoutCheck(pgName, ovnnb.ACLDirectionToLport, util.IngressAllowPriority, m, ovnnb.ACLActionAllowRelated, util.NetpolACLTier, options)
		if err != nil {
			return nil, fmt.Errorf("new ipBlock ingress acl for port group %s: %w", pgName, err)
		}
		acls = append(acls, allowACL)
	}

	return c.CreateAclsOps(pgName, portGroupKey, acls...)
}

func (c *OVNNbClient) UpdateEgressIPBlockACLOps(pgName, protocol, aclName string, ipBlocks []netv1.IPBlock, npp []netv1.NetworkPolicyPort, logEnable bool, logACLActions []ovnnb.ACLAction, logRate int, namedPortMap map[string]*util.NamedPortInfo) ([]ovsdb.Operation, error) {
	meterName := fmt.Sprintf("%s_%s_meter", pgName, ovnnb.ACLDirectionFromLport)
	matches := newIPBlockACLMatch(pgName, protocol, ovnnb.ACLDirectionFromLport, ipBlocks, npp, namedPortMap)
	if len(matches) == 0 {
		return nil, nil
	}

	acls := make([]*ovnnb.ACL, 0, len(matches))
	for _, m := range matches {
		allowACL, err := c.newACLWithoutCheck(pgName, ovnnb.ACLDirectionFromLport, util.EgressAllowPriority, m, ovnnb.ACLActionAllowRelated, util.NetpolACLTier, func(acl *ovnnb.ACL) {
			setACLName(acl, aclName)
			if acl.Options == nil {
				acl.Options = make(map[string]string)
			}
			acl.Options["apply-after-lb"] = "true"
			if logEnable && slices.Contains(logACLActions, ovnnb.ACLActionAllow) {
				acl.Log = true
				if logRate > 0 {
					acl.Meter = new(meterName)
				}
			}
		})
		if err != nil {
			return nil, fmt.Errorf("new ipBlock egress acl for port group %s: %w", pgName, err)
		}
		acls = append(acls, allowACL)
	}

	return c.CreateAclsOps(pgName, portGroupKey, acls...)
}

func aclFilter(direction string, externalIDs map[string]string) func(acl *ovnnb.ACL) bool {
	return func(acl *ovnnb.ACL) bool {
		if len(acl.ExternalIDs) < len(externalIDs) {
			return false
		}

		if len(acl.ExternalIDs) != 0 {
			for k, v := range externalIDs {
				if len(v) == 0 {
					if len(acl.ExternalIDs[k]) == 0 {
						return false
					}
				} else {
					if acl.ExternalIDs[k] != v {
						return false
					}
				}
			}
		}

		if len(direction) != 0 && acl.Direction != direction {
			return false
		}

		return true
	}
}

func (c *OVNNbClient) CreateAclsOps(parentName, parentType string, acls ...*ovnnb.ACL) ([]ovsdb.Operation, error) {
	if parentType != portGroupKey && parentType != LogicalSwitchKey {
		return nil, fmt.Errorf("acl parent type must be '%s' or '%s'", portGroupKey, LogicalSwitchKey)
	}

	if len(acls) == 0 {
		return nil, nil
	}

	models := make([]model.Model, 0, len(acls))
	aclUUIDs := make([]string, 0, len(acls))
	for _, acl := range acls {
		if acl != nil {
			models = append(models, model.Model(acl))
			aclUUIDs = append(aclUUIDs, acl.UUID)
		}
	}

	createAclsOp, err := c.Create(models...)
	if err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("generate operations for creating acls: %w", err)
	}

	var aclAddOp []ovsdb.Operation
	if parentType == portGroupKey {
		aclAddOp, err = c.portGroupUpdateACLOp(parentName, aclUUIDs, ovsdb.MutateOperationInsert)
		if err != nil {
			klog.Error(err)
			return nil, fmt.Errorf("generate operations for adding acls to port group %s: %w", parentName, err)
		}
	} else {
		aclAddOp, err = c.logicalSwitchUpdateACLOp(parentName, aclUUIDs, ovsdb.MutateOperationInsert)
		if err != nil {
			klog.Error(err)
			return nil, fmt.Errorf("generate operations for adding acls to logical switch %s: %w", parentName, err)
		}
	}

	ops := make([]ovsdb.Operation, 0, len(createAclsOp)+len(aclAddOp))
	ops = append(ops, createAclsOp...)
	ops = append(ops, aclAddOp...)

	return ops, nil
}

func (c *OVNNbClient) DeleteAclsOps(parentName, parentType, direction string, externalIDs map[string]string) ([]ovsdb.Operation, error) {
	if parentName == "" {
		return nil, errors.New("the port group name or logical switch name is required")
	}

	if externalIDs == nil {
		externalIDs = make(map[string]string)
	}

	externalIDs[aclParentKey] = parentName

	acls, err := c.ListAcls(direction, externalIDs)
	if err != nil {
		klog.Error(err)
		return nil, fmt.Errorf("list type %s %s acls: %w", parentType, parentName, err)
	}

	aclUUIDs := make([]string, 0, len(acls))
	for _, acl := range acls {
		aclUUIDs = append(aclUUIDs, acl.UUID)
	}

	var removeACLOp []ovsdb.Operation
	if parentType == portGroupKey {
		removeACLOp, err = c.portGroupUpdateACLOp(parentName, aclUUIDs, ovsdb.MutateOperationDelete)
		if err != nil {
			klog.Error(err)
			return nil, fmt.Errorf("generate operations for deleting acls from port group %s: %w", parentName, err)
		}
	} else {
		removeACLOp, err = c.logicalSwitchUpdateACLOp(parentName, aclUUIDs, ovsdb.MutateOperationDelete)
		if err != nil {
			klog.Error(err)
			return nil, fmt.Errorf("generate operations for deleting acls from logical switch %s: %w", parentName, err)
		}
	}

	return removeACLOp, nil
}

func (c *OVNNbClient) sgRuleNoACL(sgName, direction string, rule fabricv1.SecurityGroupRule, tier int) (bool, error) {
	ipSuffix := "ip4"
	if rule.IPVersion == "ipv6" {
		ipSuffix = "ip6"
	}

	pgName := GetSgPortGroupName(sgName)

	localSrcOrDst, remoteSrcOrDst, portDirection := "dst", "src", "outport"
	if direction == ovnnb.ACLDirectionFromLport {
		remoteSrcOrDst = "dst"
		localSrcOrDst = "src"
		portDirection = "inport"
	}

	ipKey := ipSuffix + "." + remoteSrcOrDst
	localIPKey := ipSuffix + "." + localSrcOrDst

	allIPMatch := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch(ipSuffix, "", "", ""),
	)

	allowedIPMatch := NewAndACLMatch(
		allIPMatch,
		NewACLMatch(ipKey, "==", rule.RemoteAddress, ""),
	)

	remotePgName := GetSgV4AssociatedName(rule.RemoteSecurityGroup)
	if rule.IPVersion == "ipv6" {
		remotePgName = GetSgV6AssociatedName(rule.RemoteSecurityGroup)
	}
	if rule.RemoteType == fabricv1.SgRemoteTypeSg {
		allowedIPMatch = NewAndACLMatch(
			allIPMatch,
			NewACLMatch(ipKey, "==", "$"+remotePgName, ""),
		)
	}

	if rule.LocalAddress != "" {
		allowedIPMatch = NewAndACLMatch(
			allowedIPMatch,
			NewACLMatch(localIPKey, "==", rule.LocalAddress, ""),
		)
	}

	match := allowedIPMatch

	switch rule.Protocol {
	case fabricv1.SgProtocolICMP:
		match = NewAndACLMatch(
			allowedIPMatch,
			NewACLMatch("icmp4", "", "", ""),
		)
		if ipSuffix == "ip6" {
			match = NewAndACLMatch(
				allowedIPMatch,
				NewACLMatch("icmp6", "", "", ""),
			)
		}
	case fabricv1.SgProtocolTCP, fabricv1.SgProtocolUDP:
		match = NewAndACLMatch(
			allowedIPMatch,
			NewACLMatch(string(rule.Protocol)+".dst", "<=", strconv.Itoa(rule.PortRangeMin), strconv.Itoa(rule.PortRangeMax)),
		)

		if rule.LocalAddress != "" {
			match = NewAndACLMatch(
				match,
				NewACLMatch(string(rule.Protocol)+".src", "<=", strconv.Itoa(rule.SourcePortRangeMin), strconv.Itoa(rule.SourcePortRangeMax)),
			)
		}
	}

	securityGroupHighestPriority, _ := strconv.Atoi(util.SecurityGroupHighestPriority)
	priority := securityGroupHighestPriority - rule.Priority
	exists, err := c.ACLExists(pgName, direction, strconv.Itoa(priority), match.String(), tier)
	if err != nil {
		err = fmt.Errorf("failed to check acl rule for security group %s: %w", sgName, err)
		klog.Error(err)
		return false, err
	}

	if !exists {
		return true, nil
	}
	return false, nil
}

func (c *OVNNbClient) SGLostACL(sg *fabricv1.SecurityGroup) (bool, error) {
	ingressRules := sg.Spec.IngressRules
	for _, rule := range ingressRules {
		no, err := c.sgRuleNoACL(sg.Name, ovnnb.ACLDirectionToLport, rule, util.ConvertSGTierToOvnTier(sg.Spec.Tier))
		if err != nil {
			klog.Error(err)
			return false, err
		}
		if no {
			klog.Infof("security group %s lost ingress rule: %v", sg.Name, rule)
			return true, nil
		}
	}
	egressRules := sg.Spec.EgressRules
	for _, rule := range egressRules {
		no, err := c.sgRuleNoACL(sg.Name, ovnnb.ACLDirectionFromLport, rule, util.ConvertSGTierToOvnTier(sg.Spec.Tier))
		if err != nil {
			klog.Error(err)
			return false, err
		}
		if no {
			klog.Infof("security group %s lost egress rule: %v", sg.Name, rule)
			return true, nil
		}
	}
	return false, nil
}

func (c *OVNNbClient) UpdateAnpRuleACLOps(pgName, asName, protocol, aclName string, priority int, aclAction ovnnb.ACLAction, logACLActions []ovnnb.ACLAction, rulePorts []v1alpha1.AdminNetworkPolicyPort, isIngress, isBanp bool) ([]ovsdb.Operation, error) {
	acls := make([]*ovnnb.ACL, 0, 10)

	options := func(acl *ovnnb.ACL) {
		setACLName(acl, aclName)

		if acl.ExternalIDs == nil {
			acl.ExternalIDs = make(map[string]string)
		}
		acl.ExternalIDs[aclParentKey] = pgName

		if acl.Options == nil {
			acl.Options = make(map[string]string)
		}
		acl.Options["apply-after-lb"] = "true"

		if slices.Contains(logACLActions, aclAction) {
			acl.Log = true
			if aclAction == ovnnb.ACLActionDrop {
				acl.Severity = ptr.To(ovnnb.ACLSeverityWarning)
			}
		}
	}

	var direction ovnnb.ACLDirection
	if isIngress {
		direction = ovnnb.ACLDirectionToLport
	} else {
		direction = ovnnb.ACLDirectionFromLport
	}

	var tier int
	if isBanp {
		tier = util.BanpACLTier
	} else {
		tier = util.AnpACLTier
	}

	matches := newAnpACLMatch(pgName, asName, protocol, direction, rulePorts)
	for _, m := range matches {
		strPriority := strconv.Itoa(priority)
		setACL, err := c.newACLWithoutCheck(pgName, direction, strPriority, m, aclAction, tier, options)
		if err != nil {
			klog.Error(err)
			return nil, fmt.Errorf("new ingress acl for port group %s: %w", pgName, err)
		}

		acls = append(acls, setACL)
	}

	ops, err := c.CreateAclsOps(pgName, portGroupKey, acls...)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	return ops, nil
}

func (c *OVNNbClient) UpdateCnpRuleACLOps(pgName, asName, protocol, aclName string, priority int, aclAction ovnnb.ACLAction, logACLActions []ovnnb.ACLAction, rulePorts []v1alpha2.ClusterNetworkPolicyPort, isIngress bool, tier int) ([]ovsdb.Operation, error) {
	acls := make([]*ovnnb.ACL, 0, 10)

	options := func(acl *ovnnb.ACL) {
		setACLName(acl, aclName)

		if acl.ExternalIDs == nil {
			acl.ExternalIDs = make(map[string]string)
		}
		acl.ExternalIDs[aclParentKey] = pgName

		if acl.Options == nil {
			acl.Options = make(map[string]string)
		}
		acl.Options["apply-after-lb"] = "true"

		if slices.Contains(logACLActions, aclAction) {
			acl.Log = true
			if aclAction == ovnnb.ACLActionDrop {
				acl.Severity = ptr.To(ovnnb.ACLSeverityWarning)
			}
		}
	}

	var direction ovnnb.ACLDirection
	if isIngress {
		direction = ovnnb.ACLDirectionToLport
	} else {
		direction = ovnnb.ACLDirectionFromLport
	}

	matches := newCnpACLMatch(pgName, asName, protocol, direction, rulePorts)
	for _, m := range matches {
		strPriority := strconv.Itoa(priority)
		setACL, err := c.newACLWithoutCheck(pgName, direction, strPriority, m, aclAction, tier, options)
		if err != nil {
			klog.Error(err)
			return nil, fmt.Errorf("new acl for port group %s: %w", pgName, err)
		}

		acls = append(acls, setACL)
	}

	ops, err := c.CreateAclsOps(pgName, portGroupKey, acls...)
	if err != nil {
		klog.Error(err)
		return nil, err
	}

	return ops, nil
}

func newAnpACLMatch(pgName, asName, protocol, direction string, rulePorts []v1alpha1.AdminNetworkPolicyPort) []string {
	ipSuffix := "ip4"
	if protocol == fabricv1.ProtocolIPv6 {
		ipSuffix = "ip6"
	}

	srcOrDst, portDirection := "src", "outport"
	if direction == ovnnb.ACLDirectionFromLport {
		srcOrDst = "dst"
		portDirection = "inport"
	}

	ipKey := ipSuffix + "." + srcOrDst

	allIPMatch := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("ip", "", "", ""),
	)

	selectIPMatch := NewAndACLMatch(
		allIPMatch,
		NewACLMatch(ipKey, "==", "$"+asName, ""),
	)
	if len(rulePorts) == 0 {
		return []string{selectIPMatch.String()}
	}

	matches := make([]string, 0, 10)
	for _, port := range rulePorts {
		switch {
		case port.PortNumber != nil:
			protocol := strings.ToLower(string(port.PortNumber.Protocol))
			protocolKey := protocol + ".dst"

			oneMatch := NewAndACLMatch(
				selectIPMatch,
				NewACLMatch(protocolKey, "==", strconv.Itoa(int(port.PortNumber.Port)), ""),
			)
			matches = append(matches, oneMatch.String())
		case port.PortRange != nil:
			protocol := strings.ToLower(string(port.PortRange.Protocol))
			protocolKey := protocol + ".dst"

			severalMatch := NewAndACLMatch(
				selectIPMatch,
				NewACLMatch(protocolKey, "<=", strconv.Itoa(int(port.PortRange.Start)), strconv.Itoa(int(port.PortRange.End))),
			)
			matches = append(matches, severalMatch.String())
		default:
			klog.Errorf("failed to check port for anp ingress rule, pg %s, as %s", pgName, asName)
		}
	}
	return matches
}

func newCnpACLMatch(pgName, asName, protocol, direction string, rulePorts []v1alpha2.ClusterNetworkPolicyPort) []string {
	ipSuffix := "ip4"
	if protocol == fabricv1.ProtocolIPv6 {
		ipSuffix = "ip6"
	}

	srcOrDst, portDirection := "src", "outport"
	if direction == ovnnb.ACLDirectionFromLport {
		srcOrDst = "dst"
		portDirection = "inport"
	}

	ipKey := ipSuffix + "." + srcOrDst

	allIPMatch := NewAndACLMatch(
		NewACLMatch(portDirection, "==", "@"+pgName, ""),
		NewACLMatch("ip", "", "", ""),
	)

	selectIPMatch := NewAndACLMatch(
		allIPMatch,
		NewACLMatch(ipKey, "==", "$"+asName, ""),
	)
	if len(rulePorts) == 0 {
		return []string{selectIPMatch.String()}
	}

	matches := make([]string, 0, 10)
	for _, port := range rulePorts {
		switch {
		case port.PortNumber != nil:
			protocol := strings.ToLower(string(port.PortNumber.Protocol))
			protocolKey := protocol + ".dst"

			oneMatch := NewAndACLMatch(
				selectIPMatch,
				NewACLMatch(protocolKey, "==", strconv.Itoa(int(port.PortNumber.Port)), ""),
			)
			matches = append(matches, oneMatch.String())
		case port.PortRange != nil:
			protocol := strings.ToLower(string(port.PortRange.Protocol))
			protocolKey := protocol + ".dst"

			severalMatch := NewAndACLMatch(
				selectIPMatch,
				NewACLMatch(protocolKey, "<=", strconv.Itoa(int(port.PortRange.Start)), strconv.Itoa(int(port.PortRange.End))),
			)
			matches = append(matches, severalMatch.String())
		default:
			klog.Errorf("failed to check port for cnp ingress rule, pg %s, as %s", pgName, asName)
		}
	}
	return matches
}

func (c *OVNNbClient) MigrateACLTier() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	var aclList []ovnnb.ACL
	if err := c.ovsDbClient.WhereCache(func(acl *ovnnb.ACL) bool { return acl.Tier == 0 }).List(ctx, &aclList); err != nil {
		err = fmt.Errorf("failed to list acls with tier 0: %w", err)
		klog.Error(err)
		return err
	}

	ops := make([]ovsdb.Operation, 0, len(aclList))
	for _, acl := range aclList {
		acl.Tier = util.NetpolACLTier
		op, err := c.Where(&acl).Update(&acl, &acl.Tier)
		if err != nil {
			klog.Error(err)
			return fmt.Errorf("failed to generate operations for updating acl %s tier: %w", acl.UUID, err)
		}
		ops = append(ops, op...)
	}
	if len(ops) == 0 {
		return nil
	}

	if err := c.Transact("acl-migrate-tier", ops); err != nil {
		klog.Error(err)
		return fmt.Errorf("failed to migrate acl tier: %w", err)
	}

	return nil
}

func (c *OVNNbClient) CleanNoParentKeyAcls() error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	var aclList []ovnnb.ACL
	if err := c.ovsDbClient.WhereCache(func(acl *ovnnb.ACL) bool {
		if len(acl.ExternalIDs) == 0 {
			return false
		}

		if acl.ExternalIDs["vendor"] != util.VendorTag {
			return false
		}

		_, hasParent := acl.ExternalIDs[aclParentKey]
		return !hasParent
	}).List(ctx, &aclList); err != nil {
		err = fmt.Errorf("failed to list fabric acls without parent: %w", err)
		klog.Error(err)
		return err
	}

	ops := make([]ovsdb.Operation, 0, len(aclList))
	for _, acl := range aclList {
		var portGroups []ovnnb.PortGroup
		if err := c.ovsDbClient.WhereCache(func(pg *ovnnb.PortGroup) bool {
			return slices.Contains(pg.ACLs, acl.UUID)
		}).List(ctx, &portGroups); err == nil {
			for _, pg := range portGroups {
				op, err := c.portGroupUpdateACLOp(pg.Name, []string{acl.UUID}, ovsdb.MutateOperationDelete)
				if err == nil {
					ops = append(ops, op...)
				}
			}
		}
		var logicalSwitches []ovnnb.LogicalSwitch
		if err := c.ovsDbClient.WhereCache(func(ls *ovnnb.LogicalSwitch) bool {
			return slices.Contains(ls.ACLs, acl.UUID)
		}).List(ctx, &logicalSwitches); err == nil {
			for _, ls := range logicalSwitches {
				op, err := c.logicalSwitchUpdateACLOp(ls.Name, []string{acl.UUID}, ovsdb.MutateOperationDelete)
				if err == nil {
					ops = append(ops, op...)
				}
			}
		}
		delOp, err := c.Where(&acl).Delete()
		if err == nil {
			ops = append(ops, delOp...)
		}
	}
	if len(ops) == 0 {
		return nil
	}

	if err := c.Transact("acl-clean-no-parent", ops); err != nil {
		klog.Error(err)
		return fmt.Errorf("failed to clean fabric acls without parent: %w", err)
	}

	return nil
}
