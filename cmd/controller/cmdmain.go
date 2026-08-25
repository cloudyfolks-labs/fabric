package main

import (
	"os"
	"path/filepath"

	"github.com/cloudyfolks-labs/fabric/cmd/pinger"
	"github.com/cloudyfolks-labs/fabric/pkg/util"
	"github.com/cloudyfolks-labs/fabric/pkg/util/profiling"
)

const (
	CmdController = "fabric-controller"
	CmdPinger     = "fabric-pinger"
)

func main() {
	cmd := filepath.Base(os.Args[0])
	switch cmd {
	case CmdController:
		profiling.DumpProfile()
		CmdMain()
	case CmdPinger:
		profiling.DumpProfile()
		pinger.CmdMain()
	default:
		util.LogFatalAndExit(nil, "%s is an unknown command", cmd)
	}
}
