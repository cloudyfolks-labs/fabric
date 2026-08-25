package ovs

import "github.com/cloudyfolks-labs/fabric/pkg/util"

func CmdSSLArgs() []string {
	return []string{
		"-C", util.SslCACert,
		"-p", util.SslKeyPath,
		"-c", util.SslCertPath,
	}
}
