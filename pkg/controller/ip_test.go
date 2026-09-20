package controller

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func Test_handleUpdateIP_deletedSubnet(t *testing.T) {
	t.Parallel()

	now := metav1.Now()
	ip := &fabricv1.IP{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-ip",
			DeletionTimestamp: &now,
			Finalizers:        []string{util.FabricControllerFinalizer},
		},
		Spec: fabricv1.IPSpec{
			Subnet:    "deleted-subnet",
			Namespace: "default",
			PodName:   "test-pod",
		},
	}

	fakeCtrl, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		IPs: []*fabricv1.IP{ip},
	})
	require.NoError(t, err)

	ctrl := fakeCtrl.fakeController

	t.Cleanup(func() {
		ctrl.updateSubnetStatusQueue.ShutDown()
		ctrl.syncVirtualPortsQueue.ShutDown()
	})

	err = ctrl.handleUpdateIP("test-ip")
	require.NoError(t, err)

	require.Equal(t, 1, ctrl.updateSubnetStatusQueue.Len())
}
