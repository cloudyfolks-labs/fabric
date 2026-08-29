package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOvnFipNats(t *testing.T) {
	t.Run("one pair per address family", func(t *testing.T) {
		nats := ovnFipNats("10.0.0.1", "fd00::1", "192.168.0.2", "fc00::2")
		require.Equal(t, []ovnFipNat{
			{externalIP: "10.0.0.1", logicalIP: "192.168.0.2"},
			{externalIP: "fd00::1", logicalIP: "fc00::2"},
		}, nats)
	})

	t.Run("v4 internal behind a v6 eip", func(t *testing.T) {
		nats := ovnFipNats("", "fd00::1", "192.168.0.2", "")
		require.Equal(t, []ovnFipNat{{externalIP: "fd00::1", logicalIP: "192.168.0.2"}}, nats)
	})

	t.Run("v6 internal behind a v4 eip", func(t *testing.T) {
		nats := ovnFipNats("10.0.0.1", "", "", "fc00::2")
		require.Equal(t, []ovnFipNat{{externalIP: "10.0.0.1", logicalIP: "fc00::2"}}, nats)
	})

	t.Run("no pair without an eip", func(t *testing.T) {
		require.Empty(t, ovnFipNats("", "", "192.168.0.2", "fc00::2"))
	})
}
