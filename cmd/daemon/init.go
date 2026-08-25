package main

import (
	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"
	"kernel.org/pub/linux/libs/security/libcap/cap"

	"github.com/cloudyfolks-labs/fabric/pkg/daemon"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
)

func printCaps() {
	currentCaps := cap.GetProc()
	klog.Infof("current capabilities: %s", currentCaps.String())
}

func initForOS() error {
	if _, err := netlink.LinkByName(util.GeneveNic); err != nil {
		if _, ok := err.(netlink.LinkNotFoundError); ok {
			return nil
		}
		klog.Errorf("failed to get link %s: %v", util.GeneveNic, err)
		return err
	}

	// disable checksum for geneve_sys_6081 as default
	return daemon.TurnOffNicTxChecksum(util.GeneveNic)
}
