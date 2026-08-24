package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ovn-kubernetes/libovsdb/ovsdb"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
)

func makeDnsZone(name, vpc string, records []kubeovnv1.DnsZoneRecord) *kubeovnv1.DnsZone {
	return &kubeovnv1.DnsZone{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kubeovnv1.DnsZoneSpec{
			Vpc:     vpc,
			Records: records,
		},
	}
}

func Test_dnsZoneRecords(t *testing.T) {
	t.Parallel()

	records, err := dnsZoneRecords(makeDnsZone("zone1", "vpc1", []kubeovnv1.DnsZoneRecord{
		{Name: "Db.Internal.", IPs: []string{"10.0.0.5"}},
		{Name: "api.internal", IPs: []string{"10.0.1.7", "fd00::7"}},
	}))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"db.internal":  "10.0.0.5",
		"api.internal": "10.0.1.7 fd00::7",
	}, records)

	_, err = dnsZoneRecords(makeDnsZone("zone1", "vpc1", []kubeovnv1.DnsZoneRecord{
		{Name: "bad", IPs: []string{"not-an-ip"}},
	}))
	require.Error(t, err)
}

func Test_handleAddOrUpdateDnsZone(t *testing.T) {
	t.Parallel()

	t.Run("missing vpc requeues and reports condition", func(t *testing.T) {
		zone := makeDnsZone("zone1", "missing-vpc", nil)
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			DnsZones: []*kubeovnv1.DnsZone{zone},
		})
		require.NoError(t, err)
		assert.Error(t, fc.fakeController.handleAddOrUpdateDnsZone("zone1"))
	})

	t.Run("zone is programmed into every switch of the vpc", func(t *testing.T) {
		zone := makeDnsZone("zone1", "vpc1", []kubeovnv1.DnsZoneRecord{
			{Name: "db.internal", IPs: []string{"10.0.0.5"}},
		})
		vpc := &kubeovnv1.Vpc{ObjectMeta: metav1.ObjectMeta{Name: "vpc1"}}
		vpc.Status.Subnets = []string{"subnet-a", "subnet-b"}
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			DnsZones: []*kubeovnv1.DnsZone{zone},
			Vpcs:     []*kubeovnv1.Vpc{vpc},
		})
		require.NoError(t, err)

		fc.mockOvnClient.EXPECT().
			EnsureDnsZone("zone1", map[string]string{"db.internal": "10.0.0.5"}).
			Return("uuid-1", nil)
		fc.mockOvnClient.EXPECT().
			LogicalSwitchUpdateDnsRecords("subnet-a", "uuid-1", ovsdb.MutateOperationInsert).
			Return(nil)
		fc.mockOvnClient.EXPECT().
			LogicalSwitchUpdateDnsRecords("subnet-b", "uuid-1", ovsdb.MutateOperationInsert).
			Return(nil)

		require.NoError(t, fc.fakeController.handleAddOrUpdateDnsZone("zone1"))
	})

	t.Run("delete removes the nb dns row", func(t *testing.T) {
		fc, err := newFakeControllerWithOptions(t, nil)
		require.NoError(t, err)
		fc.mockOvnClient.EXPECT().DeleteDnsZone("zone1").Return(nil)
		require.NoError(t, fc.fakeController.handleDelDnsZone("zone1"))
	})
}
