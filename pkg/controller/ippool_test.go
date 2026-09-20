package controller

import (
	"testing"

	"k8s.io/utils/set"

	"github.com/stretchr/testify/require"

	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func TestIPPoolAddressSetName(t *testing.T) {
	require.Equal(t, "foo.bar", util.IPPoolAddressSetName("foo-bar"))
	require.Equal(t, "123pool", util.IPPoolAddressSetName("123pool"))
}

func TestNormalizeAddressSetEntries(t *testing.T) {
	t.Run("Standard OVN format", func(t *testing.T) {
		result := util.NormalizeAddressSetEntries(`"10.0.0.1/32" "10.0.0.2/32"`)
		require.Equal(t, set.New("10.0.0.1/32", "10.0.0.2/32"), result)
	})

	t.Run("Extra whitespace", func(t *testing.T) {
		result := util.NormalizeAddressSetEntries(`  "10.0.0.1/32"   "10.0.0.2/32"  `)
		require.Equal(t, set.New("10.0.0.1/32", "10.0.0.2/32"), result)
	})

	t.Run("Empty input", func(t *testing.T) {
		result := util.NormalizeAddressSetEntries("")
		require.Empty(t, result)
	})

	t.Run("No quotes", func(t *testing.T) {
		result := util.NormalizeAddressSetEntries("10.0.0.1/32 10.0.0.2/32")
		require.Equal(t, set.New("10.0.0.1/32", "10.0.0.2/32"), result)
	})

	t.Run("Mixed formats", func(t *testing.T) {
		result := util.NormalizeAddressSetEntries(`"192.168.1.0/24" "2001:db8::1/128"`)
		require.Equal(t, set.New("192.168.1.0/24", "2001:db8::1/128"), result)
	})
}

func TestNormalizeIP(t *testing.T) {
	t.Run("Standard IPv4", func(t *testing.T) {
		ip, err := util.NormalizeIP("192.168.1.1")
		require.NoError(t, err)
		require.Equal(t, "192.168.1.1", ip.String())
		require.NotNil(t, ip.To4())
	})

	t.Run("Standard IPv6", func(t *testing.T) {
		ip, err := util.NormalizeIP("2001:db8::1")
		require.NoError(t, err)
		require.Equal(t, "2001:db8::1", ip.String())
		require.Nil(t, ip.To4())
	})

	t.Run("IPv6 full form", func(t *testing.T) {
		ip, err := util.NormalizeIP("2001:0db8:0000:0000:0000:0000:0000:0001")
		require.NoError(t, err)
		require.Equal(t, "2001:db8::1", ip.String())
	})

	t.Run("Whitespace handling", func(t *testing.T) {
		ip, err := util.NormalizeIP("  10.0.0.1  ")
		require.NoError(t, err)
		require.Equal(t, "10.0.0.1", ip.String())
	})

	t.Run("Invalid IP", func(t *testing.T) {
		_, err := util.NormalizeIP("999.999.999.999")
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid IP address")
	})

	t.Run("Not an IP", func(t *testing.T) {
		_, err := util.NormalizeIP("hostname")
		require.Error(t, err)
	})

	t.Run("IPv4 loopback", func(t *testing.T) {
		ip, err := util.NormalizeIP("127.0.0.1")
		require.NoError(t, err)
		require.Equal(t, "127.0.0.1", ip.String())
	})

	t.Run("IPv6 loopback", func(t *testing.T) {
		ip, err := util.NormalizeIP("::1")
		require.NoError(t, err)
		require.Equal(t, "::1", ip.String())
	})
}

func TestExpandIPPoolAddressesForOVNIntegration(t *testing.T) {
	t.Run("Mixed IPv4 and IPv6 - should fail", func(t *testing.T) {
		_, err := util.ExpandIPPoolAddressesForOVN([]string{
			"10.0.0.1",
			"2001:db8::1",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "mixed IPv4 and IPv6 addresses are not supported")
	})

	t.Run("Pure IPv4 pool", func(t *testing.T) {
		addresses, err := util.ExpandIPPoolAddressesForOVN([]string{
			"192.168.1.0/30",
			"10.0.0.1..10.0.0.5",
		})
		require.NoError(t, err)
		require.NotEmpty(t, addresses)

		for _, addr := range addresses {
			require.NotContains(t, addr, ":")
		}

		require.Contains(t, addresses, "192.168.1.0/30")
	})

	t.Run("Pure IPv6 pool", func(t *testing.T) {
		addresses, err := util.ExpandIPPoolAddressesForOVN([]string{
			"2001:db8::/126",
			"fd00::1..fd00::3",
		})
		require.NoError(t, err)
		require.NotEmpty(t, addresses)

		for _, addr := range addresses {
			require.Contains(t, addr, ":")
		}

		require.Contains(t, addresses, "2001:db8::/126")
	})

	t.Run("Single IPs are simplified", func(t *testing.T) {
		addresses, err := util.ExpandIPPoolAddressesForOVN([]string{
			"10.0.0.1",
			"10.0.0.2",
		})
		require.NoError(t, err)
		require.Len(t, addresses, 2)

		require.Contains(t, addresses, "10.0.0.1")
		require.Contains(t, addresses, "10.0.0.2")
		require.NotContains(t, addresses, "10.0.0.1/32")
		require.NotContains(t, addresses, "10.0.0.2/32")
	})

	t.Run("Mixed in range notation", func(t *testing.T) {
		_, err := util.ExpandIPPoolAddressesForOVN([]string{
			"10.0.0.1..10.0.0.5",
			"fd00::1",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "mixed IPv4 and IPv6")
	})

	t.Run("Mixed in CIDR notation", func(t *testing.T) {
		_, err := util.ExpandIPPoolAddressesForOVN([]string{
			"192.168.1.0/24",
			"2001:db8::/64",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "mixed IPv4 and IPv6")
	})

	t.Run("Range expansion with simplification", func(t *testing.T) {
		addresses, err := util.ExpandIPPoolAddressesForOVN([]string{
			"10.0.0.1..10.0.0.1",
		})
		require.NoError(t, err)
		require.Len(t, addresses, 1)
		require.Equal(t, "10.0.0.1", addresses[0])
		require.NotContains(t, addresses[0], "/32")
	})

	t.Run("Range with multiple CIDRs - some simplified", func(t *testing.T) {
		addresses, err := util.ExpandIPPoolAddressesForOVN([]string{
			"10.0.0.1..10.0.0.5",
		})
		require.NoError(t, err)
		require.NotEmpty(t, addresses)

		require.Contains(t, addresses, "10.0.0.1")
		require.Contains(t, addresses, "10.0.0.2/31")
		require.Contains(t, addresses, "10.0.0.4/31")
	})

	t.Run("Empty input", func(t *testing.T) {
		addresses, err := util.ExpandIPPoolAddressesForOVN([]string{})
		require.NoError(t, err)
		require.Empty(t, addresses)
	})

	t.Run("Only whitespace entries", func(t *testing.T) {
		addresses, err := util.ExpandIPPoolAddressesForOVN([]string{"", "  ", "\t"})
		require.NoError(t, err)
		require.Empty(t, addresses)
	})

	t.Run("IPv6 single IP simplified", func(t *testing.T) {
		addresses, err := util.ExpandIPPoolAddressesForOVN([]string{"2001:db8::1"})
		require.NoError(t, err)
		require.Len(t, addresses, 1)
		require.Equal(t, "2001:db8::1", addresses[0])
		require.NotContains(t, addresses[0], "/128")
	})

	t.Run("IPv6 range with /128 simplified", func(t *testing.T) {
		addresses, err := util.ExpandIPPoolAddressesForOVN([]string{"fd00::1..fd00::1"})
		require.NoError(t, err)
		require.Len(t, addresses, 1)
		require.Equal(t, "fd00::1", addresses[0])
	})

	t.Run("Duplicate IPs deduplicated and simplified", func(t *testing.T) {
		addresses, err := util.ExpandIPPoolAddressesForOVN([]string{
			"10.0.0.1",
			"10.0.0.1",
			" 10.0.0.1 ",
		})
		require.NoError(t, err)
		require.Len(t, addresses, 1)
		require.Equal(t, "10.0.0.1", addresses[0])
	})

	t.Run("CIDR normalization preserved", func(t *testing.T) {
		addresses, err := util.ExpandIPPoolAddressesForOVN([]string{"192.168.1.5/24"})
		require.NoError(t, err)
		require.Len(t, addresses, 1)
		require.Equal(t, "192.168.1.0/24", addresses[0])
	})
}
