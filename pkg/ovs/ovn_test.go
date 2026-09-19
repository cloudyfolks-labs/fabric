package ovs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnnb"
	"github.com/cloudyfolks-labs/fabric/pkg/ovsdb/ovnsb"
)

func TestNewLegacyClient(t *testing.T) {
	timeout := 30
	client := NewLegacyClient(timeout)
	require.NotNil(t, client)
	require.Equal(t, timeout, client.OvnTimeout)
}

func (suite *OvnClientTestSuite) testNewOvnNbClient() {
	t := suite.T()

	ovnNbTimeout := 5
	ovsDbConTimeout := 10
	ovsDbInactivityTimeout := 20

	clientSchema := ovnnb.Schema()
	clientDBModel, err := ovnnb.FullDatabaseModel()
	require.NoError(suite.T(), err)

	_, sock := newOVSDBServer(suite.T(), "test-nb-client", clientDBModel, clientSchema)
	endpoint := "unix:" + sock
	require.FileExists(suite.T(), sock)

	t.Run("successful client creation", func(t *testing.T) {
		client, err := NewOvnNbClient(endpoint, ovnNbTimeout, ovsDbConTimeout, ovsDbInactivityTimeout, 1)
		require.NoError(t, err)
		require.NotNil(t, client)
		require.Equal(t, time.Duration(ovnNbTimeout)*time.Second, client.Timeout)
	})

	t.Run("ovsdb client error with max retry", func(t *testing.T) {
		client, err := NewOvnNbClient("invalid addr", 5, 10, 20, 1)
		require.Error(t, err)
		require.Nil(t, client)
	})
}

func (suite *OvnClientTestSuite) testNewOvnSbClient() {
	t := suite.T()

	ovnSbTimeout := 5
	ovsDbConTimeout := 10
	ovsDbInactivityTimeout := 20

	clientSchema := ovnsb.Schema()
	clientDBModel, err := ovnsb.FullDatabaseModel()
	require.NoError(suite.T(), err)

	_, sock := newOVSDBServer(suite.T(), "test-sb-client", clientDBModel, clientSchema)
	endpoint := "unix:" + sock
	require.FileExists(suite.T(), sock)

	t.Run("successful client creation", func(t *testing.T) {
		client, err := NewOvnSbClient(endpoint, ovnSbTimeout, ovsDbConTimeout, ovsDbInactivityTimeout, 1)
		require.NoError(t, err)
		require.NotNil(t, client)
		require.Equal(t, time.Duration(ovnSbTimeout)*time.Second, client.Timeout)
	})

	t.Run("ovsdb client error with max retry", func(t *testing.T) {
		client, err := NewOvnSbClient("invalid addr", 5, 10, 20, 1)
		require.Error(t, err)
		require.Nil(t, client)
	})
}
