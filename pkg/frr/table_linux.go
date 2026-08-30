package frr

import "github.com/vishvananda/netlink"

func tableRouteCount(tableID uint32) (int, error) {
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: int(tableID)}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return 0, err
	}
	return len(routes), nil
}
