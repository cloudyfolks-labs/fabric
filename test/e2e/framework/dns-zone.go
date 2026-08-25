package framework

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/kubeovn/v1"
	v1 "github.com/cloudyfolks-labs/fabric/pkg/client/clientset/versioned/typed/kubeovn/v1"
)

type DNSZoneClient struct {
	f *Framework
	v1.DNSZoneInterface
}

func (f *Framework) DNSZoneClient() *DNSZoneClient {
	return &DNSZoneClient{
		f:                f,
		DNSZoneInterface: f.KubeOVNClientSet.FabricV1().DNSZones(),
	}
}

func (c *DNSZoneClient) Get(name string) *apiv1.DNSZone {
	ginkgo.GinkgoHelper()
	zone, err := c.DNSZoneInterface.Get(context.TODO(), name, metav1.GetOptions{})
	ExpectNoError(err)
	return zone
}

func (c *DNSZoneClient) Create(zone *apiv1.DNSZone) *apiv1.DNSZone {
	ginkgo.GinkgoHelper()
	created, err := c.DNSZoneInterface.Create(context.TODO(), zone, metav1.CreateOptions{})
	ExpectNoError(err, "Error creating dns zone")
	return created.DeepCopy()
}

func (c *DNSZoneClient) Update(zone *apiv1.DNSZone) *apiv1.DNSZone {
	ginkgo.GinkgoHelper()
	updated, err := c.DNSZoneInterface.Update(context.TODO(), zone, metav1.UpdateOptions{})
	ExpectNoError(err, "Error updating dns zone")
	return updated.DeepCopy()
}

func (c *DNSZoneClient) Delete(name string) {
	ginkgo.GinkgoHelper()
	err := c.DNSZoneInterface.Delete(context.TODO(), name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		ExpectNoError(err, "Error deleting dns zone")
	}
}

func MakeDNSZone(name, vpc string, records map[string][]string) *apiv1.DNSZone {
	zone := &apiv1.DNSZone{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiv1.DNSZoneSpec{
			Vpc: vpc,
		},
	}
	for recordName, ips := range records {
		zone.Spec.Records = append(zone.Spec.Records, apiv1.DNSZoneRecord{Name: recordName, IPs: ips})
	}
	return zone
}
