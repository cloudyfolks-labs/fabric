package frr

import (
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSetMetricsReportsEveryConfiguredPeerAndVpc(t *testing.T) {
	peers, err := ParseBgpSummary([]byte(bgpSummarySample))
	if err != nil {
		t.Fatal(err)
	}
	prefixes, err := CountPrefixesByNextHop([]byte(bgpRoutesSample))
	if err != nil {
		t.Fatal(err)
	}
	tables := map[uint32]int{1001: 2, 1002: 0}
	setMetrics(metricsInput{
		node:      "node-1",
		neighbors: []string{"10.0.0.254", "10.0.0.253", "10.0.0.252"},
		vpcs: []VpcAdvertisement{
			{VpcName: "vpc-a", TableID: 1001, LrpIP: "10.0.0.21"},
			{VpcName: "vpc-b", TableID: 1002, LrpIP: "10.0.0.23"},
			{VpcName: "vpc-c", TableID: 1003, LrpIP: "10.0.0.22"},
		},
		peers:    peers,
		prefixes: prefixes,
		tableRoutes: func(id uint32) (int, error) {
			routes, ok := tables[id]
			if !ok {
				return 0, errors.New("no such table")
			}
			return routes, nil
		},
	})

	peerUp := map[string]float64{"10.0.0.254": 1, "10.0.0.253": 0, "10.0.0.252": 0}
	for peer, want := range peerUp {
		if got := testutil.ToFloat64(bgpPeerUp.WithLabelValues(peer, "node-1")); got != want {
			t.Errorf("peer %s: expected up %v, got %v", peer, want, got)
		}
	}
	if got := testutil.ToFloat64(bgpPeerPrefixesAdvertised.WithLabelValues("10.0.0.254", "node-1")); got != 3 {
		t.Errorf("expected 3 prefixes advertised to the established peer, got %v", got)
	}
	if got := testutil.ToFloat64(bgpPeerPrefixesAdvertised.WithLabelValues("10.0.0.252", "node-1")); got != 0 {
		t.Errorf("expected 0 prefixes advertised to a peer FRR does not report, got %v", got)
	}

	advertised := map[string]float64{"vpc-a": 2, "vpc-b": 0, "vpc-c": 1}
	for vpc, want := range advertised {
		if got := testutil.ToFloat64(vpcAdvertisedPrefixes.WithLabelValues(vpc, "node-1")); got != want {
			t.Errorf("vpc %s: expected %v advertised prefixes, got %v", vpc, want, got)
		}
	}
	if got := testutil.ToFloat64(vpcTableRoutes.WithLabelValues("vpc-a", "1001", "node-1")); got != 2 {
		t.Errorf("expected 2 routes in table 1001, got %v", got)
	}
	if got := testutil.ToFloat64(vpcTableRoutes.WithLabelValues("vpc-b", "1002", "node-1")); got != 0 {
		t.Errorf("expected 0 routes in table 1002, got %v", got)
	}
	if got := testutil.CollectAndCount(vpcTableRoutes); got != 2 {
		t.Errorf("expected no series for a table that cannot be read, got %d series", got)
	}
}

func TestSetMetricsDropsSeriesOfRemovedPeers(t *testing.T) {
	setMetrics(metricsInput{
		node:        "node-1",
		neighbors:   []string{"10.0.0.254"},
		peers:       map[string]BgpPeer{"10.0.0.254": {Established: true}},
		tableRoutes: func(uint32) (int, error) { return 0, nil },
	})
	setMetrics(metricsInput{
		node:        "node-1",
		neighbors:   []string{"10.0.0.253"},
		tableRoutes: func(uint32) (int, error) { return 0, nil },
	})
	if got := testutil.CollectAndCount(bgpPeerUp); got != 1 {
		t.Errorf("expected only the current peer to have a series, got %d", got)
	}
	if got := testutil.ToFloat64(bgpPeerUp.WithLabelValues("10.0.0.253", "node-1")); got != 0 {
		t.Errorf("expected the new peer to be down, got %v", got)
	}
}
