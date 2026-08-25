package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ovn-kubernetes/libovsdb/ovsdb"

	kubeovnv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
)

func makeDNSZone(name, vpc string, records []kubeovnv1.DNSZoneRecord) *kubeovnv1.DNSZone {
	return &kubeovnv1.DNSZone{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: kubeovnv1.DNSZoneSpec{
			Vpc:     vpc,
			Records: records,
		},
	}
}

func Test_dnsZoneRecords(t *testing.T) {
	t.Parallel()

	records, err := dnsZoneRecords(makeDNSZone("zone1", "vpc1", []kubeovnv1.DNSZoneRecord{
		{Name: "Db.Internal.", IPs: []string{"10.0.0.5"}},
		{Name: "api.internal", IPs: []string{"10.0.1.7", "fd00::7"}},
	}))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"db.internal":  "10.0.0.5",
		"api.internal": "10.0.1.7 fd00::7",
	}, records)

	_, err = dnsZoneRecords(makeDNSZone("zone1", "vpc1", []kubeovnv1.DNSZoneRecord{
		{Name: "bad", IPs: []string{"not-an-ip"}},
	}))
	require.Error(t, err)
}

func Test_handleAddOrUpdateDNSZone(t *testing.T) {
	t.Parallel()

	t.Run("missing vpc requeues and reports condition", func(t *testing.T) {
		zone := makeDNSZone("zone1", "missing-vpc", nil)
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			DNSZones: []*kubeovnv1.DNSZone{zone},
		})
		require.NoError(t, err)
		assert.Error(t, fc.fakeController.handleAddOrUpdateDNSZone("zone1"))
	})

	t.Run("zone is programmed into every switch of the vpc", func(t *testing.T) {
		zone := makeDNSZone("zone1", "vpc1", []kubeovnv1.DNSZoneRecord{
			{Name: "db.internal", IPs: []string{"10.0.0.5"}},
		})
		vpc := &kubeovnv1.Vpc{ObjectMeta: metav1.ObjectMeta{Name: "vpc1"}}
		subnetA := &kubeovnv1.Subnet{ObjectMeta: metav1.ObjectMeta{Name: "subnet-a"}, Spec: kubeovnv1.SubnetSpec{Vpc: "vpc1", CIDRBlock: "10.60.0.0/24"}}
		subnetB := &kubeovnv1.Subnet{ObjectMeta: metav1.ObjectMeta{Name: "subnet-b"}, Spec: kubeovnv1.SubnetSpec{Vpc: "vpc1", CIDRBlock: "10.60.1.0/24"}}
		fc, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
			DNSZones: []*kubeovnv1.DNSZone{zone},
			Vpcs:     []*kubeovnv1.Vpc{vpc},
			Subnets:  []*kubeovnv1.Subnet{subnetA, subnetB},
		})
		require.NoError(t, err)

		fc.mockOvnClient.EXPECT().
			EnsureDNSZone("zone1", map[string]string{"db.internal": "10.0.0.5"}).
			Return("uuid-1", nil)
		fc.mockOvnClient.EXPECT().
			LogicalSwitchUpdateDNSRecords("subnet-a", "uuid-1", ovsdb.MutateOperationInsert).
			Return(nil)
		fc.mockOvnClient.EXPECT().
			LogicalSwitchUpdateDNSRecords("subnet-b", "uuid-1", ovsdb.MutateOperationInsert).
			Return(nil)

		require.NoError(t, fc.fakeController.handleAddOrUpdateDNSZone("zone1"))
	})

	t.Run("delete removes the nb dns row", func(t *testing.T) {
		fc, err := newFakeControllerWithOptions(t, nil)
		require.NoError(t, err)
		fc.mockOvnClient.EXPECT().DeleteDNSZone("zone1").Return(nil)
		require.NoError(t, fc.fakeController.handleDelDNSZone("zone1"))
	})
}
