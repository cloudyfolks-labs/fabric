package frr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	bgpSummaryFileName = ".fabric-frr-bgp-summary.json"
	bgpRoutesFileName  = ".fabric-frr-bgp-routes.json"
	snapshotMaxAge     = 30 * time.Second
)

// BgpPeer is the session state of one neighbour of the ipv4 unicast
// address family as reported by "show bgp summary json".
type BgpPeer struct {
	Established  bool
	PrefixesSent int
}

type bgpSummary struct {
	IPv4Unicast struct {
		Peers map[string]struct {
			State  string `json:"state"`
			PfxSnt int    `json:"pfxSnt"`
		} `json:"peers"`
	} `json:"ipv4Unicast"`
}

// ParseBgpSummary reads the peers of the ipv4 unicast address family from
// the output of "show bgp summary json", keyed by neighbour address.
func ParseBgpSummary(data []byte) (map[string]BgpPeer, error) {
	var summary bgpSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return nil, fmt.Errorf("failed to parse bgp summary: %w", err)
	}
	peers := make(map[string]BgpPeer, len(summary.IPv4Unicast.Peers))
	for address, peer := range summary.IPv4Unicast.Peers {
		peers[address] = BgpPeer{
			Established:  peer.State == "Established",
			PrefixesSent: peer.PfxSnt,
		}
	}
	return peers, nil
}

type bgpRoutes struct {
	Routes map[string][]struct {
		Nexthops []struct {
			IP string `json:"ip"`
		} `json:"nexthops"`
	} `json:"routes"`
}

// CountPrefixesByNextHop counts, per next hop address, the prefixes of
// "show bgp ipv4 unicast json" that carry at least one path through it.
func CountPrefixesByNextHop(data []byte) (map[string]int, error) {
	var rib bgpRoutes
	if err := json.Unmarshal(data, &rib); err != nil {
		return nil, fmt.Errorf("failed to parse bgp routes: %w", err)
	}
	counts := make(map[string]int)
	for _, paths := range rib.Routes {
		seen := make(map[string]struct{})
		for _, path := range paths {
			for _, nexthop := range path.Nexthops {
				if _, ok := seen[nexthop.IP]; ok {
					continue
				}
				seen[nexthop.IP] = struct{}{}
				counts[nexthop.IP]++
			}
		}
	}
	return counts, nil
}

func readSnapshot(frrDir, name string) ([]byte, bool) {
	path := filepath.Join(frrDir, name)
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > snapshotMaxAge {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}
