package frr

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	bgpPeerUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "fabric_frr_bgp_peer_up",
		Help: "1 when the BGP session to the peer is Established, 0 otherwise.",
	}, []string{"peer", "node"})
	bgpPeerPrefixesAdvertised = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "fabric_frr_bgp_peer_prefixes_advertised",
		Help: "Number of prefixes advertised to the peer.",
	}, []string{"peer", "node"})
	vpcAdvertisedPrefixes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "fabric_frr_vpc_advertised_prefixes",
		Help: "Number of prefixes in the BGP table whose next hop is the external gateway LRP of the VPC.",
	}, []string{"vpc", "node"})
	vpcTableRoutes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "fabric_frr_vpc_table_routes",
		Help: "Number of IPv4 routes in the kernel table of the VPC.",
	}, []string{"vpc", "table", "node"})
)

// RegisterMetrics adds the agent gauges to the registry served by the metrics server.
func RegisterMetrics() {
	metrics.Registry.MustRegister(bgpPeerUp, bgpPeerPrefixesAdvertised, vpcAdvertisedPrefixes, vpcTableRoutes)
}

type metricsInput struct {
	node        string
	neighbors   []string
	vpcs        []VpcAdvertisement
	peers       map[string]BgpPeer
	prefixes    map[string]int
	tableRoutes func(uint32) (int, error)
}

func setMetrics(in metricsInput) {
	bgpPeerUp.Reset()
	bgpPeerPrefixesAdvertised.Reset()
	vpcAdvertisedPrefixes.Reset()
	vpcTableRoutes.Reset()

	for _, address := range in.neighbors {
		peer := in.peers[address]
		up := 0.0
		if peer.Established {
			up = 1
		}
		bgpPeerUp.WithLabelValues(address, in.node).Set(up)
		bgpPeerPrefixesAdvertised.WithLabelValues(address, in.node).Set(float64(peer.PrefixesSent))
	}
	for _, vpc := range in.vpcs {
		vpcAdvertisedPrefixes.WithLabelValues(vpc.VpcName, in.node).Set(float64(in.prefixes[vpc.LrpIP]))
		routes, err := in.tableRoutes(vpc.TableID)
		if err != nil {
			klog.Warningf("vpc %s: failed to count routes of table %d: %v", vpc.VpcName, vpc.TableID, err)
			continue
		}
		vpcTableRoutes.WithLabelValues(vpc.VpcName, formatTableID(vpc.TableID), in.node).Set(float64(routes))
	}
}

func formatTableID(tableID uint32) string {
	return strconv.FormatUint(uint64(tableID), 10)
}
