package ovs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func (suite *OvnClientTestSuite) testUpdateGatewayChassises() {
	t := suite.T()
	t.Parallel()

	nbClient := suite.ovnNBClient
	lrName := "test-update-gateway-chassises-lr"
	lrpName := "test-update-gateway-chassises-lrp"
	chassises := []string{"c7efec70-9519-4b03-8b67-057f2a95e5c7", "4a0891b6-fe81-4986-a367-aad0ea7ca9f3", "dcc2eda3-b3ea-4d53-afe0-7b6eaf7917ba"}

	err := nbClient.CreateLogicalRouter(lrName)
	require.NoError(t, err)

	err = nbClient.CreateLogicalRouterPort(lrName, lrpName, "00:11:22:37:af:62", []string{"fd00::c0a8:1001/120"})
	require.NoError(t, err)

	err = nbClient.UpdateGatewayChassises(lrpName, chassises)
	require.NoError(t, err)

	lrp, err := nbClient.GetLogicalRouterPort(lrpName, false)
	require.NoError(t, err)
	require.Len(t, lrp.GatewayChassis, len(chassises))
	for i, chassisName := range chassises {
		gwChassis, err := nbClient.GetGatewayChassis(lrpName+"-"+chassisName, false)
		require.NoError(t, err)
		require.Equal(t, chassisName, gwChassis.ChassisName)
		require.Equal(t, 100-i, gwChassis.Priority)
		require.Contains(t, lrp.GatewayChassis, gwChassis.UUID)
	}

	err = nbClient.UpdateGatewayChassises(lrpName, chassises)
	require.NoError(t, err)
	lrp, err = nbClient.GetLogicalRouterPort(lrpName, false)
	require.NoError(t, err)
	require.Len(t, lrp.GatewayChassis, len(chassises))

	reordered := []string{chassises[2], chassises[0]}
	err = nbClient.UpdateGatewayChassises(lrpName, reordered)
	require.NoError(t, err)

	lrp, err = nbClient.GetLogicalRouterPort(lrpName, false)
	require.NoError(t, err)
	require.Len(t, lrp.GatewayChassis, len(reordered))
	for i, chassisName := range reordered {
		gwChassis, err := nbClient.GetGatewayChassis(lrpName+"-"+chassisName, false)
		require.NoError(t, err)
		require.Equal(t, 100-i, gwChassis.Priority)
		require.Contains(t, lrp.GatewayChassis, gwChassis.UUID)
	}
	dropped, err := nbClient.GetGatewayChassis(lrpName+"-"+chassises[1], true)
	require.NoError(t, err)
	require.Nil(t, dropped)

	err = nbClient.UpdateGatewayChassises(lrpName, nil)
	require.NoError(t, err)
	lrp, err = nbClient.GetLogicalRouterPort(lrpName, false)
	require.NoError(t, err)
	require.Empty(t, lrp.GatewayChassis)
}

func (suite *OvnClientTestSuite) testDeleteGatewayChassisByChassisName() {
	t := suite.T()
	t.Parallel()

	nbClient := suite.ovnNBClient
	lrName := "test-del-gateway-chassis-lr"
	lrpName := "test-del-gateway-chassis-lrp"
	stale := "0a9d8f52-2c1e-4f2f-9a70-1e3a4ba1d000"
	kept := "1b8e7d43-3b2d-4e3e-8b61-2f4b5ca2e111"

	err := nbClient.CreateLogicalRouter(lrName)
	require.NoError(t, err)

	err = nbClient.CreateLogicalRouterPort(lrName, lrpName, "00:11:22:37:af:63", []string{"fd00::c0a8:1101/120"})
	require.NoError(t, err)

	err = nbClient.UpdateGatewayChassises(lrpName, []string{stale, kept})
	require.NoError(t, err)

	err = nbClient.DeleteGatewayChassisByChassisName(stale)
	require.NoError(t, err)

	lrp, err := nbClient.GetLogicalRouterPort(lrpName, false)
	require.NoError(t, err)
	require.Len(t, lrp.GatewayChassis, 1)

	gone, err := nbClient.GetGatewayChassis(lrpName+"-"+stale, true)
	require.NoError(t, err)
	require.Nil(t, gone)

	err = nbClient.DeleteGatewayChassisByChassisName(stale)
	require.NoError(t, err)
}

func (suite *OvnClientTestSuite) testUpdateGatewayChassis() {
	t := suite.T()
	t.Parallel()

	nbClient := suite.ovnNBClient
	lrName := "test-gateway-chassis-update-lr"
	lrpName := "test-gateway-chassis-update-lrp"
	chassis := "6c322ce8-02b7-42b3-925b-ae24020272a9"
	gwChassisName := lrpName + "-" + chassis

	err := nbClient.CreateLogicalRouter(lrName)
	require.NoError(t, err)

	err = nbClient.CreateLogicalRouterPort(lrName, lrpName, "00:11:22:37:af:62", []string{"fd00::c0a8:1001/120"})
	require.NoError(t, err)

	err = nbClient.UpdateGatewayChassises(lrpName, []string{chassis})
	require.NoError(t, err)

	gwChassis, err := nbClient.GetGatewayChassis(gwChassisName, false)
	require.NoError(t, err)
	require.NotNil(t, gwChassis)

	gwChassis.Priority = 100
	err = nbClient.UpdateGatewayChassis(gwChassis, &gwChassis.Priority)
	require.NoError(t, err)

	gwChassis, err = nbClient.GetGatewayChassis(gwChassisName, false)
	require.NoError(t, err)
	require.NotNil(t, gwChassis)
	require.Equal(t, 100, gwChassis.Priority)

	err = nbClient.UpdateGatewayChassis(gwChassis, nil)
	require.ErrorContains(t, err, "failed to generate operations for gateway chassis")
}

func TestGatewayChassisPriorities(t *testing.T) {
	t.Parallel()

	require.Empty(t, gatewayChassisPriorities(nil))
	require.Equal(t, map[string]int{"a": 100, "b": 99, "c": 98}, gatewayChassisPriorities([]string{"a", "b", "c"}))
}
