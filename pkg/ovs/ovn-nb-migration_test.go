package ovs

import (
	"testing"

	"github.com/stretchr/testify/require"

	ovsclient "github.com/cloudyfolks-labs/fabric/pkg/ovsdb/client"
	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
	"github.com/cloudyfolks-labs/fabric/versions"
)

func ensureNbGlobalExists(t *testing.T, nbClient *OVNNbClient) {
	_, err := nbClient.GetNbGlobal()
	if err != nil {
		nbGlobal := &ovnnb.NBGlobal{
			Options: map[string]string{},
		}
		err = nbClient.CreateNbGlobal(nbGlobal)
		require.NoError(t, err)
	}
}

func (suite *OvnClientTestSuite) testMigrateVendorExternalIDs() {
	t := suite.T()

	nbClient := suite.ovnNBClient
	lrName := "test-migrate-lr"
	lsName := "test-migrate-ls"

	t.Cleanup(func() {
		_ = nbClient.DeleteNbGlobal()
	})

	ensureNbGlobalExists(t, nbClient)

	nbGlobal, err := nbClient.GetNbGlobal()
	require.NoError(t, err)
	if nbGlobal.ExternalIDs != nil {
		delete(nbGlobal.ExternalIDs, fabricVersionKey)
		err = nbClient.UpdateNbGlobal(nbGlobal, &nbGlobal.ExternalIDs)
		require.NoError(t, err)
	}

	err = nbClient.CreateLogicalRouter(lrName)
	require.NoError(t, err)

	err = nbClient.CreateBareLogicalSwitch(lsName)
	require.NoError(t, err)

	lrpName := lrName + "-" + lsName
	lrp := &ovnnb.LogicalRouterPort{
		UUID:     ovsclient.NamedUUID(),
		Name:     lrpName,
		MAC:      util.GenerateMac(),
		Networks: []string{"10.0.0.1/24"},
		ExternalIDs: map[string]string{
			logicalRouterKey: lrName,
		},
	}
	ops, err := nbClient.CreateLogicalRouterPortOp(lrp, lrName)
	require.NoError(t, err)
	err = nbClient.Transact("test-lrp-add", ops)
	require.NoError(t, err)

	sgPgName := "ovn.sg.test.security.group"
	pg := &ovnnb.PortGroup{
		UUID: ovsclient.NamedUUID(),
		Name: sgPgName,
		ExternalIDs: map[string]string{
			sgKey: "test-sg",
		},
	}
	ops, err = nbClient.Create(pg)
	require.NoError(t, err)
	err = nbClient.Transact("test-pg-add", ops)
	require.NoError(t, err)

	asName := "ovn.sg.test.sg.associated.v4"
	as := &ovnnb.AddressSet{
		UUID: ovsclient.NamedUUID(),
		Name: asName,
		ExternalIDs: map[string]string{
			sgKey: "test-sg",
		},
	}
	ops, err = nbClient.Create(as)
	require.NoError(t, err)
	err = nbClient.Transact("test-as-add", ops)
	require.NoError(t, err)

	lbName := "cluster-tcp-loadbalancer"
	lb := &ovnnb.LoadBalancer{
		UUID:     ovsclient.NamedUUID(),
		Name:     lbName,
		Protocol: &[]string{"tcp"}[0],
	}
	ops, err = nbClient.Create(lb)
	require.NoError(t, err)
	err = nbClient.Transact("test-lb-add", ops)
	require.NoError(t, err)

	err = nbClient.MigrateVendorExternalIDs()
	require.NoError(t, err)

	migratedLrp, err := nbClient.GetLogicalRouterPort(lrpName, false)
	require.NoError(t, err)
	require.Equal(t, util.VendorTag, migratedLrp.ExternalIDs["vendor"])

	migratedPg, err := nbClient.GetPortGroup(sgPgName, false)
	require.NoError(t, err)
	require.Equal(t, util.VendorTag, migratedPg.ExternalIDs["vendor"])

	migratedLb, err := nbClient.GetLoadBalancer(lbName, false)
	require.NoError(t, err)
	require.Equal(t, util.VendorTag, migratedLb.ExternalIDs["vendor"])

	storedVersion, err := nbClient.GetFabricVersion()
	require.NoError(t, err)
	require.Equal(t, versions.VERSION, storedVersion)
}

func (suite *OvnClientTestSuite) testMigrateVendorExternalIDsIdempotent() {
	t := suite.T()

	nbClient := suite.ovnNBClient
	lrName := "test-migrate-idempotent-lr"

	t.Cleanup(func() {
		_ = nbClient.DeleteNbGlobal()
	})

	ensureNbGlobalExists(t, nbClient)

	err := nbClient.CreateLogicalRouter(lrName)
	require.NoError(t, err)

	nbGlobal, err := nbClient.GetNbGlobal()
	require.NoError(t, err)
	if nbGlobal.ExternalIDs != nil {
		delete(nbGlobal.ExternalIDs, fabricVersionKey)
		err = nbClient.UpdateNbGlobal(nbGlobal, &nbGlobal.ExternalIDs)
		require.NoError(t, err)
	}

	err = nbClient.MigrateVendorExternalIDs()
	require.NoError(t, err)

	storedVersion, err := nbClient.GetFabricVersion()
	require.NoError(t, err)
	require.Equal(t, versions.VERSION, storedVersion)

	for range 3 {
		err = nbClient.MigrateVendorExternalIDs()
		require.NoError(t, err)
	}

	storedVersion, err = nbClient.GetFabricVersion()
	require.NoError(t, err)
	require.Equal(t, versions.VERSION, storedVersion)
}

func (suite *OvnClientTestSuite) testMigrateSkipsWhenVersionSet() {
	t := suite.T()

	nbClient := suite.ovnNBClient

	t.Cleanup(func() {
		_ = nbClient.DeleteNbGlobal()
	})

	ensureNbGlobalExists(t, nbClient)

	err := nbClient.SetFabricVersion(versions.VERSION)
	require.NoError(t, err)

	needsMigration, err := nbClient.needsVendorMigration()
	require.NoError(t, err)
	require.False(t, needsMigration, "migration should not be needed when current version is set")
}

func (suite *OvnClientTestSuite) testMigrateRunsWhenOldVersion() {
	t := suite.T()

	nbClient := suite.ovnNBClient

	t.Cleanup(func() {
		_ = nbClient.DeleteNbGlobal()
	})

	ensureNbGlobalExists(t, nbClient)

	err := nbClient.SetFabricVersion("v1.14.0")
	require.NoError(t, err)

	needsMigration, err := nbClient.needsVendorMigration()
	require.NoError(t, err)
	require.True(t, needsMigration, "migration should be needed when old version is stored")
}

func (suite *OvnClientTestSuite) testMigrateVendorExternalIDsSkipsNonFabric() {
	t := suite.T()

	nbClient := suite.ovnNBClient

	t.Cleanup(func() {
		_ = nbClient.DeleteNbGlobal()
	})

	ensureNbGlobalExists(t, nbClient)

	neutronPgName := "neutron.security.group.123"
	pg := &ovnnb.PortGroup{
		UUID: ovsclient.NamedUUID(),
		Name: neutronPgName,
		ExternalIDs: map[string]string{
			"neutron:security_group_id": "123",
		},
	}
	ops, err := nbClient.Create(pg)
	require.NoError(t, err)
	err = nbClient.Transact("test-neutron-pg", ops)
	require.NoError(t, err)

	err = nbClient.MigrateVendorExternalIDs()
	require.NoError(t, err)

	migratedPg, err := nbClient.GetPortGroup(neutronPgName, false)
	require.NoError(t, err)

	require.NotEqual(t, util.VendorTag, migratedPg.ExternalIDs["vendor"])
}

func TestSecurityGroupPatterns(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		expected bool
	}{
		{"ovn.sg.default", true},
		{"ovn.sg.my.security.group", true},
		{"ovn.sg.test.with.many.dots", true},
		{"ovn.sg.", false},
		{"ovn.other.thing", false},
		{"neutron.sg.something", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := sgPortGroupPattern.MatchString(tc.name)
			if result != tc.expected {
				t.Errorf("sgPortGroupPattern.MatchString(%q) = %v, want %v", tc.name, result, tc.expected)
			}
		})
	}
}

func TestLoadBalancerPatterns(t *testing.T) {
	t.Parallel()

	clusterCases := []struct {
		name     string
		expected bool
	}{
		{"cluster-tcp-loadbalancer", true},
		{"cluster-udp-loadbalancer", true},
		{"cluster-sctp-loadbalancer", true},
		{"cluster-tcp-session-loadbalancer", true},
		{"cluster-udp-session-loadbalancer", true},
		{"cluster-sctp-session-loadbalancer", true},
		{"other-tcp-loadbalancer", false},
		{"cluster-http-loadbalancer", false},
	}

	for _, tc := range clusterCases {
		t.Run("cluster/"+tc.name, func(t *testing.T) {
			result := clusterLBPattern.MatchString(tc.name)
			if result != tc.expected {
				t.Errorf("clusterLBPattern.MatchString(%q) = %v, want %v", tc.name, result, tc.expected)
			}
		})
	}

	vpcCases := []struct {
		name     string
		expected bool
	}{
		{"vpc-default-tcp-load", true},
		{"vpc-default-udp-load", true},
		{"vpc-default-sctp-load", true},
		{"vpc-default-tcp-sess-load", true},
		{"vpc-my-custom-vpc-udp-sess-load", true},
		{"vpc-custom-sctp-load", true},
		{"vpc-default-http-load", false},
		{"cluster-tcp-loadbalancer", false},
	}

	for _, tc := range vpcCases {
		t.Run("vpc/"+tc.name, func(t *testing.T) {
			result := vpcLBPattern.MatchString(tc.name)
			if result != tc.expected {
				t.Errorf("vpcLBPattern.MatchString(%q) = %v, want %v", tc.name, result, tc.expected)
			}
		})
	}
}
