package fabric

import (
	"flag"
	"testing"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/config"

	"github.com/onsi/ginkgo/v2"

	// Import tests.
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/crd"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/dns_zone"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/ipam"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/kubectl-ko"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/network-policy"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/node"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/pod"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/qos"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/router_lb_rule"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/service"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/service_cidr"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/subnet"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/switch_lb_rule"
	_ "github.com/cloudyfolks-labs/fabric/test/e2e/fabric/underlay"
)

func init() {
	klog.SetOutput(ginkgo.GinkgoWriter)

	// Register flags.
	config.CopyFlags(config.Flags, flag.CommandLine)
	framework.RegisterCommonFlags(flag.CommandLine)
	framework.RegisterClusterFlags(flag.CommandLine)
}

func TestE2E(t *testing.T) {
	framework.AfterReadingAllFlags(&framework.TestContext)
	e2e.RunE2ETests(t)
}
