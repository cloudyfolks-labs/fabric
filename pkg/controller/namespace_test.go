package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fabricv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/fabric/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func Test_handleAddNamespace_orphanedSubnet(t *testing.T) {
	const nsName = "test-ns"
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: nsName},
	}

	orphanSubnet := &fabricv1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "orphan-subnet"},
		Spec: fabricv1.SubnetSpec{
			Vpc:       "ghost-vpc",
			CIDRBlock: "10.16.0.0/16",
		},
	}

	validSubnet := &fabricv1.Subnet{
		ObjectMeta: metav1.ObjectMeta{Name: "valid-subnet"},
		Spec: fabricv1.SubnetSpec{
			Namespaces: []string{nsName},
			CIDRBlock:  "10.17.0.0/16",
		},
	}

	fakeCtrl, err := newFakeControllerWithOptions(t, &FakeControllerOptions{
		Namespaces: []*corev1.Namespace{ns},
		Subnets:    []*fabricv1.Subnet{orphanSubnet, validSubnet},
	})
	require.NoError(t, err)
	ctrl := fakeCtrl.fakeController

	require.NoError(t, ctrl.handleAddNamespace(nsName))

	got, err := ctrl.config.KubeClient.CoreV1().Namespaces().Get(context.Background(), nsName, metav1.GetOptions{})
	require.NoError(t, err)

	lss := strings.Split(got.Annotations[util.LogicalSwitchAnnotation], ",")
	require.Contains(t, lss, validSubnet.Name, "valid subnet must be bound even when an orphaned subnet is present")
	require.NotContains(t, lss, orphanSubnet.Name, "broken subnet must not be bound to the namespace")
}
