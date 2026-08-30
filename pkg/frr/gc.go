package frr

import "github.com/cloudyfolks-labs/fabric/pkg/util"

const ovnRouteProtocol = 84

type tableRoute struct {
	Table    uint32
	Protocol int
	Dst      string
}

type routePurge struct {
	Stale    []tableRoute
	Reassert []tableRoute
}

func planRoutePurge(routes []tableRoute, owned map[uint32]struct{}) routePurge {
	var plan routePurge
	staleDst := make(map[string]struct{})
	for _, r := range routes {
		if r.Table == 0 || r.Table > util.MaxTableDirectID {
			continue
		}
		if r.Table >= util.ReservedRoutingTableIDStart && r.Table <= util.ReservedRoutingTableIDEnd {
			continue
		}
		if _, ok := owned[r.Table]; ok {
			continue
		}
		if r.Protocol != ovnRouteProtocol {
			continue
		}
		plan.Stale = append(plan.Stale, r)
		staleDst[r.Dst] = struct{}{}
	}
	if len(staleDst) == 0 {
		return plan
	}
	for _, r := range routes {
		if _, owns := owned[r.Table]; !owns {
			continue
		}
		if _, stale := staleDst[r.Dst]; stale {
			plan.Reassert = append(plan.Reassert, r)
		}
	}
	return plan
}
