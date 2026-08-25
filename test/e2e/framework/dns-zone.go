package framework

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/kubeovn/kube-ovn/pkg/apis/kubeovn/v1"
	v1 "github.com/kubeovn/kube-ovn/pkg/client/clientset/versioned/typed/kubeovn/v1"
)

type DnsZoneClient struct {
	f *Framework
	v1.DnsZoneInterface
}

func (f *Framework) DnsZoneClient() *DnsZoneClient {
	return &DnsZoneClient{
		f:                f,
		DnsZoneInterface: f.KubeOVNClientSet.FabricV1().DnsZones(),
	}
}

func (c *DnsZoneClient) Get(name string) *apiv1.DnsZone {
	ginkgo.GinkgoHelper()
	zone, err := c.DnsZoneInterface.Get(context.TODO(), name, metav1.GetOptions{})
	ExpectNoError(err)
	return zone
}

func (c *DnsZoneClient) Create(zone *apiv1.DnsZone) *apiv1.DnsZone {
	ginkgo.GinkgoHelper()
	created, err := c.DnsZoneInterface.Create(context.TODO(), zone, metav1.CreateOptions{})
	ExpectNoError(err, "Error creating dns zone")
	return created.DeepCopy()
}

func (c *DnsZoneClient) Update(zone *apiv1.DnsZone) *apiv1.DnsZone {
	ginkgo.GinkgoHelper()
	updated, err := c.DnsZoneInterface.Update(context.TODO(), zone, metav1.UpdateOptions{})
	ExpectNoError(err, "Error updating dns zone")
	return updated.DeepCopy()
}

func (c *DnsZoneClient) Delete(name string) {
	ginkgo.GinkgoHelper()
	err := c.DnsZoneInterface.Delete(context.TODO(), name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		ExpectNoError(err, "Error deleting dns zone")
	}
}

func MakeDnsZone(name, vpc string, records map[string][]string) *apiv1.DnsZone {
	zone := &apiv1.DnsZone{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiv1.DnsZoneSpec{
			Vpc: vpc,
		},
	}
	for recordName, ips := range records {
		zone.Spec.Records = append(zone.Spec.Records, apiv1.DnsZoneRecord{Name: recordName, IPs: ips})
	}
	return zone
}
