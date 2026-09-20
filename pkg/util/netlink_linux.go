package util

import (
	"fmt"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

func AddrList(link netlink.Link, family int) ([]netlink.Addr, error) {
	if link == nil {
		return netlink.AddrList(link, family)
	}

	h, err := netlink.NewHandle(unix.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("failed to create netlink handle: %w", err)
	}
	defer h.Close()

	if err := h.SetStrictCheck(true); err != nil {
		klog.V(5).Infof("NETLINK_GET_STRICT_CHK not supported (%v); falling back to package-level AddrList", err)
		return netlink.AddrList(link, family)
	}
	return h.AddrList(link, family)
}
