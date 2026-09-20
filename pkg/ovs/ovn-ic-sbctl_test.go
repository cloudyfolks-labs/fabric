package ovs

import (
	"github.com/stretchr/testify/require"
)

func (suite *OvnClientTestSuite) testOvnIcSbCommand() {
	t := suite.T()
	t.Parallel()

	ovnLegacyClient := suite.ovnLegacyClient
	cmd := []string{"--format=csv", "--data=bare", "--no-heading", "--columns=name", "list", "Chassis"}
	output, err := ovnLegacyClient.ovnIcSbCommand(cmd...)

	require.Error(t, err)
	require.Empty(t, output)
}

func (suite *OvnClientTestSuite) testFindUUIDWithAttrInTable() {
	t := suite.T()
	t.Parallel()

	ovnLegacyClient := suite.ovnLegacyClient
	uuids, err := ovnLegacyClient.FindUUIDWithAttrInTable("availability_zone", "xx_uuid", "gateway")

	require.Error(t, err)
	require.Empty(t, uuids)
}

func (suite *OvnClientTestSuite) testDestroyTableWithUUID() {
	t := suite.T()
	t.Parallel()

	ovnLegacyClient := suite.ovnLegacyClient
	err := ovnLegacyClient.DestroyTableWithUUID("uuid", "gateway")

	require.Error(t, err)
}

func (suite *OvnClientTestSuite) testGetAzUUID() {
	t := suite.T()
	t.Parallel()

	ovnLegacyClient := suite.ovnLegacyClient
	uuid, err := ovnLegacyClient.GetAzUUID("az1")

	require.Error(t, err)
	require.Empty(t, uuid)
}

func (suite *OvnClientTestSuite) testGetGatewayUUIDsInOneAZ() {
	t := suite.T()
	t.Parallel()

	ovnLegacyClient := suite.ovnLegacyClient
	uuids, err := ovnLegacyClient.GetGatewayUUIDsInOneAZ("uuid")

	require.Error(t, err)
	require.Empty(t, uuids)
}

func (suite *OvnClientTestSuite) testGetRouteUUIDsInOneAZ() {
	t := suite.T()
	t.Parallel()

	ovnLegacyClient := suite.ovnLegacyClient
	uuids, err := ovnLegacyClient.GetRouteUUIDsInOneAZ("uuid")

	require.Error(t, err)
	require.Empty(t, uuids)
}

func (suite *OvnClientTestSuite) testGetPortBindingUUIDsInOneAZ() {
	t := suite.T()
	t.Parallel()

	ovnLegacyClient := suite.ovnLegacyClient
	uuids, err := ovnLegacyClient.GetPortBindingUUIDsInOneAZ("uuid")

	require.Error(t, err)
	require.Empty(t, uuids)
}

func (suite *OvnClientTestSuite) testDestroyGateways() {
	t := suite.T()
	t.Parallel()

	ovnLegacyClient := suite.ovnLegacyClient
	err := ovnLegacyClient.DestroyGateways([]string{"uuid"})

	require.Error(t, err)
}

func (suite *OvnClientTestSuite) testDestroyRoutes() {
	t := suite.T()
	t.Parallel()

	ovnLegacyClient := suite.ovnLegacyClient
	err := ovnLegacyClient.DestroyRoutes([]string{"uuid"})

	require.Error(t, err)
}

func (suite *OvnClientTestSuite) testDestroyPortBindings() {
	t := suite.T()
	t.Parallel()

	ovnLegacyClient := suite.ovnLegacyClient
	err := ovnLegacyClient.DestroyPortBindings([]string{"uuid"})

	require.Error(t, err)
}

func (suite *OvnClientTestSuite) testDestroyChassis() {
	t := suite.T()
	t.Parallel()

	ovnLegacyClient := suite.ovnLegacyClient
	err := ovnLegacyClient.DestroyChassis("uuid")

	require.Error(t, err)
}
