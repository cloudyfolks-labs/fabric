package netconf

import "github.com/cloudyfolks-labs/fabric/pkg/request"

type IPAMConf struct {
	Type         string          `json:"type"`
	ServerSocket string          `json:"server_socket,omitempty"`
	Provider     string          `json:"provider,omitempty"`
	Routes       []request.Route `json:"routes,omitempty"`
}
