package main

import (
	"os"
	"path/filepath"

	"github.com/cloudyfolks-labs/fabric/cmd/frr"
	"github.com/cloudyfolks-labs/fabric/cmd/ovn_ic_controller"
	"github.com/cloudyfolks-labs/fabric/cmd/ovn_leader_checker"
	"github.com/cloudyfolks-labs/fabric/cmd/ovn_monitor"
	"github.com/cloudyfolks-labs/fabric/cmd/webhook"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
	"github.com/cloudyfolks-labs/fabric/pkg/util/profiling"
)

const (
	CmdMonitor          = "fabric-monitor"
	CmdWebhook          = "fabric-webhook"
	CmdOvnLeaderChecker = "fabric-leader-checker"
	CmdOvnICController  = "fabric-ic-controller"
	CmdFrr              = "fabric-frr"
)

func main() {
	cmd := filepath.Base(os.Args[0])
	switch cmd {
	case CmdMonitor:
		profiling.DumpProfile()
		ovn_monitor.CmdMain()
	case CmdWebhook:
		webhook.CmdMain()
	case CmdOvnLeaderChecker:
		ovn_leader_checker.CmdMain()
	case CmdOvnICController:
		ovn_ic_controller.CmdMain()
	case CmdFrr:
		if len(os.Args) > 1 && os.Args[1] == "init" {
			frr.InitMain()
			return
		}
		profiling.DumpProfile()
		frr.AgentMain()
	default:
		util.LogFatalAndExit(nil, "%s is an unknown command", cmd)
	}
}
