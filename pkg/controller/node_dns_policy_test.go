package controller

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func pgAs(nodePortName string, af int) string {
	return strings.ReplaceAll(fmt.Sprintf("%s_ip%d", nodePortName, af), "-", ".")
}

func TestAddPolicyRouteForLocalDNSCacheOnNode_DualStackCrossDeletion(t *testing.T) {
	t.Parallel()

	fc := newFakeController(t)
	ctrl := fc.fakeController
	mockOvnClient := fc.mockOvnClient

	const (
		nodeName     = "test-node"
		nodePortName = "test-node"
		nodeIPv4     = "10.0.0.1"
		nodeIPv6     = "fd00::1"
		dnsIPv4      = "169.254.20.10"
		dnsIPv6      = "fd00::ffff:1"
	)

	matchV4 := fmt.Sprintf("ip4.src == $%s && ip4.dst == %s", pgAs(nodePortName, 4), dnsIPv4)
	matchV6 := fmt.Sprintf("ip6.src == $%s && ip6.dst == %s", pgAs(nodePortName, 6), dnsIPv6)

	existingV4Policy := &ovnnb.LogicalRouterPolicy{
		UUID:     "uuid-v4-dns",
		Priority: util.NodeRouterPolicyPriority,
		Match:    matchV4,
		Action:   string(fabricv1.PolicyRouteActionReroute),
		Nexthops: []string{nodeIPv4},
		ExternalIDs: map[string]string{
			"vendor":          util.VendorTag,
			"node":            nodeName,
			"address-family":  "4",
			"isLocalDNSCache": "true",
		},
	}

	existingV6Policy := &ovnnb.LogicalRouterPolicy{
		UUID:     "uuid-v6-dns",
		Priority: util.NodeRouterPolicyPriority,
		Match:    matchV6,
		Action:   string(fabricv1.PolicyRouteActionReroute),
		Nexthops: []string{nodeIPv6},
		ExternalIDs: map[string]string{
			"vendor":          util.VendorTag,
			"node":            nodeName,
			"address-family":  "6",
			"isLocalDNSCache": "true",
		},
	}

	t.Run("af4_call_should_not_delete_af6_policy", func(t *testing.T) {
		mockOvnClient.EXPECT().
			ListLogicalRouterPolicies(ctrl.config.ClusterRouter, -1, map[string]string{
				"vendor":          util.VendorTag,
				"node":            nodeName,
				"address-family":  "4",
				"isLocalDNSCache": "true",
			}, true).
			Return([]*ovnnb.LogicalRouterPolicy{existingV4Policy}, nil)

		err := ctrl.addPolicyRouteForLocalDNSCacheOnNode(
			[]string{dnsIPv4}, nodePortName, nodeIPv4, nodeName, 4,
		)
		require.NoError(t, err)
	})

	t.Run("af6_call_should_not_delete_af4_policy", func(t *testing.T) {
		mockOvnClient.EXPECT().
			ListLogicalRouterPolicies(ctrl.config.ClusterRouter, -1, map[string]string{
				"vendor":          util.VendorTag,
				"node":            nodeName,
				"address-family":  "6",
				"isLocalDNSCache": "true",
			}, true).
			Return([]*ovnnb.LogicalRouterPolicy{existingV6Policy}, nil)

		err := ctrl.addPolicyRouteForLocalDNSCacheOnNode(
			[]string{dnsIPv6}, nodePortName, nodeIPv6, nodeName, 6,
		)
		require.NoError(t, err)
	})
}

func TestAddPolicyRouteForLocalDNSCacheOnNode_DualStackFullSimulation(t *testing.T) {
	t.Parallel()

	fc := newFakeController(t)
	ctrl := fc.fakeController
	mockOvnClient := fc.mockOvnClient

	const (
		nodeName     = "dualnode"
		nodePortName = "dualnode"
		nodeIPv4     = "10.0.0.2"
		nodeIPv6     = "fd00::2"
		dnsIPv4      = "169.254.20.10"
		dnsIPv6      = "fd00::ffff:1"
	)

	matchV4 := fmt.Sprintf("ip4.src == $%s && ip4.dst == %s", pgAs(nodePortName, 4), dnsIPv4)
	matchV6 := fmt.Sprintf("ip6.src == $%s && ip6.dst == %s", pgAs(nodePortName, 6), dnsIPv6)

	externalIDsV4 := map[string]string{
		"vendor":          util.VendorTag,
		"node":            nodeName,
		"address-family":  "4",
		"isLocalDNSCache": "true",
	}
	externalIDsV6 := map[string]string{
		"vendor":          util.VendorTag,
		"node":            nodeName,
		"address-family":  "6",
		"isLocalDNSCache": "true",
	}

	t.Run("step1_af4_creates_policy", func(t *testing.T) {
		mockOvnClient.EXPECT().
			ListLogicalRouterPolicies(ctrl.config.ClusterRouter, -1, externalIDsV4, true).
			Return(nil, nil)

		mockOvnClient.EXPECT().
			AddLogicalRouterPolicy(
				ctrl.config.ClusterRouter,
				util.NodeRouterPolicyPriority,
				matchV4,
				string(fabricv1.PolicyRouteActionReroute),
				[]string{nodeIPv4},
				([]string)(nil),
				externalIDsV4,
			).Return(nil)

		err := ctrl.addPolicyRouteForLocalDNSCacheOnNode(
			[]string{dnsIPv4}, nodePortName, nodeIPv4, nodeName, 4,
		)
		require.NoError(t, err)
	})

	t.Run("step2_af6_creates_without_deleting_af4", func(t *testing.T) {
		mockOvnClient.EXPECT().
			ListLogicalRouterPolicies(ctrl.config.ClusterRouter, -1, externalIDsV6, true).
			Return(nil, nil)

		mockOvnClient.EXPECT().
			AddLogicalRouterPolicy(
				ctrl.config.ClusterRouter,
				util.NodeRouterPolicyPriority,
				matchV6,
				string(fabricv1.PolicyRouteActionReroute),
				[]string{nodeIPv6},
				([]string)(nil),
				externalIDsV6,
			).Return(nil)

		err := ctrl.addPolicyRouteForLocalDNSCacheOnNode(
			[]string{dnsIPv6}, nodePortName, nodeIPv6, nodeName, 6,
		)
		require.NoError(t, err)
	})
}

func TestAddPolicyRouteForLocalDNSCacheOnNode_DeletesStalePolicy(t *testing.T) {
	t.Parallel()

	fc := newFakeController(t)
	ctrl := fc.fakeController
	mockOvnClient := fc.mockOvnClient

	const (
		nodeName     = "node1"
		nodePortName = "node1"
		nodeIPv4     = "10.0.0.3"
		oldDNSIPv4   = "169.254.20.10"
		newDNSIPv4   = "169.254.20.11"
	)

	oldMatchV4 := fmt.Sprintf("ip4.src == $%s && ip4.dst == %s", pgAs(nodePortName, 4), oldDNSIPv4)
	newMatchV4 := fmt.Sprintf("ip4.src == $%s && ip4.dst == %s", pgAs(nodePortName, 4), newDNSIPv4)

	externalIDsV4 := map[string]string{
		"vendor":          util.VendorTag,
		"node":            nodeName,
		"address-family":  strconv.Itoa(4),
		"isLocalDNSCache": "true",
	}

	stalePolicy := &ovnnb.LogicalRouterPolicy{
		UUID:        "uuid-stale",
		Priority:    util.NodeRouterPolicyPriority,
		Match:       oldMatchV4,
		Action:      string(fabricv1.PolicyRouteActionReroute),
		Nexthops:    []string{nodeIPv4},
		ExternalIDs: externalIDsV4,
	}

	mockOvnClient.EXPECT().
		ListLogicalRouterPolicies(ctrl.config.ClusterRouter, -1, externalIDsV4, true).
		Return([]*ovnnb.LogicalRouterPolicy{stalePolicy}, nil)

	mockOvnClient.EXPECT().
		DeleteLogicalRouterPolicyByUUID(ctrl.config.ClusterRouter, "uuid-stale").
		Return(nil)

	mockOvnClient.EXPECT().
		AddLogicalRouterPolicy(
			ctrl.config.ClusterRouter,
			util.NodeRouterPolicyPriority,
			newMatchV4,
			string(fabricv1.PolicyRouteActionReroute),
			[]string{nodeIPv4},
			([]string)(nil),
			externalIDsV4,
		).Return(nil)

	err := ctrl.addPolicyRouteForLocalDNSCacheOnNode(
		[]string{newDNSIPv4}, nodePortName, nodeIPv4, nodeName, 4,
	)
	require.NoError(t, err)
}
