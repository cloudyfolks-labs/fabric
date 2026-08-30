package frr

import (
	"fmt"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func purgeStaleOvnRoutes(owned map[uint32]struct{}) (int, error) {
	dump, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return 0, fmt.Errorf("failed to list the kernel routes: %w", err)
	}
	routes := make([]tableRoute, 0, len(dump))
	byKey := make(map[tableRoute]netlink.Route, len(dump))
	for _, r := range dump {
		var dst string
		if r.Dst != nil {
			dst = r.Dst.String()
		}
		key := tableRoute{Table: uint32(r.Table), Protocol: int(r.Protocol), Dst: dst}
		routes = append(routes, key)
		byKey[key] = r
	}
	plan := planRoutePurge(routes, owned)
	for _, key := range plan.Stale {
		route := byKey[key]
		if err := netlink.RouteDel(&route); err != nil {
			return 0, fmt.Errorf("failed to delete the stale route %s of table %d: %w", key.Dst, key.Table, err)
		}
	}
	for _, key := range plan.Reassert {
		route := byKey[key]
		if err := netlink.RouteReplace(&route); err != nil {
			return 0, fmt.Errorf("failed to re-assert the route %s of table %d: %w", key.Dst, key.Table, err)
		}
	}
	return len(plan.Stale), nil
}
