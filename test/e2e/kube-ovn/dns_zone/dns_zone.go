package dns_zone

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/cloudyfolks-labs/fabric/pkg/apis/kubeovn/v1"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
	"github.com/cloudyfolks-labs/fabric/test/e2e/framework"
)

var _ = framework.Describe("[group:dns-zone]", func() {
	f := framework.NewDefaultFramework("dns-zone")

	var vpc1Name, vpc2Name, subnet1Name, subnet2Name, pod1Name, pod2Name, zone1Name, zone2Name string

	ginkgo.BeforeEach(func() {
		f.SkipVersionPriorTo(1, 15, "dns zones were introduced with fabric")
		vpc1Name = "vpc1-" + framework.RandomSuffix()
		vpc2Name = "vpc2-" + framework.RandomSuffix()
		subnet1Name = "subnet1-" + framework.RandomSuffix()
		subnet2Name = "subnet2-" + framework.RandomSuffix()
		pod1Name = "pod1-" + framework.RandomSuffix()
		pod2Name = "pod2-" + framework.RandomSuffix()
		zone1Name = "zone1-" + framework.RandomSuffix()
		zone2Name = "zone2-" + framework.RandomSuffix()
	})

	setupVpcPod := func(vpcName, subnetName, podName, cidr string) *corev1.Pod {
		ginkgo.By("Creating vpc " + vpcName)
		vpc := framework.MakeVpc(vpcName, "", false, false, nil)
		_ = f.VpcClient().CreateSync(vpc)
		ginkgo.DeferCleanup(func() {
			f.VpcClient().DeleteSync(vpcName)
		})

		ginkgo.By("Creating subnet " + subnetName)
		subnet := framework.MakeSubnet(subnetName, "", cidr, "", vpcName, "", nil, nil, nil)
		_ = f.SubnetClient().CreateSync(subnet)
		ginkgo.DeferCleanup(func() {
			f.SubnetClient().DeleteSync(subnetName)
		})

		ginkgo.By("Creating pod " + podName)
		annotations := map[string]string{util.LogicalSwitchAnnotation: subnetName}
		pod := framework.MakePod(f.Namespace.Name, podName, nil, annotations, f.KubeOVNImage, []string{"sleep", "infinity"}, nil)
		created := f.PodClient().CreateSync(pod)
		ginkgo.DeferCleanup(func() {
			f.PodClient().DeleteSync(podName)
		})
		return created
	}

	lookup := func(pod *corev1.Pod, name string) string {
		queryType := "a"
		if f.IsIPv6() {
			queryType = "aaaa"
		}
		stdout, _, err := framework.ExecCommandInContainer(f, pod.Namespace, pod.Name, pod.Spec.Containers[0].Name,
			"sh", "-c", fmt.Sprintf("nslookup -type=%s -timeout=2 %s. 2>&1 || true", queryType, name))
		if err != nil {
			return ""
		}
		return stdout
	}

	waitLookup := func(pod *corev1.Pod, name, expected string) {
		ginkgo.GinkgoHelper()
		framework.WaitUntil(2*time.Second, 2*time.Minute, func(_ context.Context) (bool, error) {
			return strings.Contains(lookup(pod, name), expected), nil
		}, fmt.Sprintf("pod %s resolves %s to %s", pod.Name, name, expected))
	}

	framework.ConformanceIt("should serve split-horizon records from the datapath", func() {
		cidr, answer1, answer2, answer3 := "10.60.0.0/24", "10.60.0.101", "10.60.0.202", "10.60.0.111"
		if f.IsIPv6() {
			cidr, answer1, answer2, answer3 = "fd00:10:60::/120", "fd00:10:60::101", "fd00:10:60::202", "fd00:10:60::111"
		}
		pod1 := setupVpcPod(vpc1Name, subnet1Name, pod1Name, cidr)
		pod2 := setupVpcPod(vpc2Name, subnet2Name, pod2Name, cidr)

		ginkgo.By("Creating dns zone " + zone1Name + " for vpc " + vpc1Name)
		zone1 := framework.MakeDNSZone(zone1Name, vpc1Name, map[string][]string{
			"db.internal": {answer1},
		})
		_ = f.DNSZoneClient().Create(zone1)
		ginkgo.DeferCleanup(func() {
			f.DNSZoneClient().Delete(zone1Name)
		})

		ginkgo.By("Creating dns zone " + zone2Name + " for vpc " + vpc2Name + " with the same name and a different answer")
		zone2 := framework.MakeDNSZone(zone2Name, vpc2Name, map[string][]string{
			"db.internal": {answer2},
		})
		_ = f.DNSZoneClient().Create(zone2)
		ginkgo.DeferCleanup(func() {
			f.DNSZoneClient().Delete(zone2Name)
		})

		ginkgo.By("Verifying each vpc sees its own answer")
		waitLookup(pod1, "db.internal", answer1)
		waitLookup(pod2, "db.internal", answer2)

		ginkgo.By("Updating the record of zone " + zone1Name)
		updated := f.DNSZoneClient().Get(zone1Name)
		updated.Spec.Records = []apiv1.DNSZoneRecord{{Name: "db.internal", IPs: []string{answer3}}}
		_ = f.DNSZoneClient().Update(updated)
		waitLookup(pod1, "db.internal", answer3)

		ginkgo.By("Verifying the other vpc still sees its own answer")
		waitLookup(pod2, "db.internal", answer2)

		ginkgo.By("Deleting zone " + zone1Name + " and verifying its answer disappears")
		f.DNSZoneClient().Delete(zone1Name)
		framework.WaitUntil(2*time.Second, 2*time.Minute, func(_ context.Context) (bool, error) {
			return !strings.Contains(lookup(pod1, "db.internal"), answer3), nil
		}, "the record of the deleted zone is gone")
	})
})
